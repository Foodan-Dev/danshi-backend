package tencentcloud

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	tencenterrors "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/errors"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	teo "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/teo/v20220901"

	"github.com/Foodan-Dev/danshi-backend/internal/config"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/obs"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

const (
	edgeOneEndpoint            = "teo.tencentcloudapi.com"
	edgeOneRequestTimeout      = 5 * time.Second
	edgeOnePurgeOverallTimeout = 12 * time.Second
	edgeOnePurgeMaxAttempts    = 2
	edgeOnePurgeRetryDelay     = 200 * time.Millisecond
	edgeOnePurgeRequestSpacing = 100 * time.Millisecond // 单实例最多 10 QPS，低于官方 20 QPS 限制。
	edgeOneInstrumentationName = "github.com/Foodan-Dev/danshi-backend/internal/infra/tencentcloud/edgeone"
)

var (
	errEdgeOnePurgeInvalidTarget = errors.New("edgeone cache purge target is invalid")
	errEdgeOnePurgeRequest       = errors.New("edgeone cache purge request failed")
	errEdgeOnePurgeResponse      = errors.New("edgeone cache purge response is invalid")
)

type edgeOnePurgeClient interface {
	CreatePurgeTaskWithContext(
		ctx context.Context,
		request *teo.CreatePurgeTaskRequest,
	) (*teo.CreatePurgeTaskResponse, error)
}

// EdgeOnePurger 只提交一张原图对应的三个精确且属于 COS_IMG_DOMAIN 的 HTTPS URL。
// 它不会接受目录、hostname、全站或跨站点刷新，避免把审核回调扩大成高危缓存操作。
type EdgeOnePurger struct {
	client      edgeOnePurgeClient
	zoneID      string
	imageOrigin *url.URL
	gate        *edgeOneRateGate
	maxAttempts int
	retryDelay  time.Duration
	overallTTL  time.Duration
}

// NewEdgeOnePurger 使用官方腾讯云 Go SDK 创建 EdgeOne 精确 URL 清除适配器。
func NewEdgeOnePurger(cfg config.Config) (*EdgeOnePurger, error) {
	if !cfg.EdgeOneConfigured() {
		return nil, errors.New("EdgeOne URL 刷新配置不完整")
	}
	clientProfile := profile.NewClientProfile()
	clientProfile.HttpProfile.Endpoint = edgeOneEndpoint
	clientProfile.HttpProfile.ReqTimeout = int(edgeOneRequestTimeout / time.Second)
	// Purge 没有 ClientToken；由本适配器实施可审计的单层有限重试，禁止 SDK 再叠加重试。
	clientProfile.NetworkFailureMaxRetries = 0
	clientProfile.RateLimitExceededMaxRetries = 0
	clientProfile.UnsafeRetryOnConnectionFailure = false
	client, err := teo.NewClient(
		common.NewCredential(cfg.TencentSecretID, cfg.TencentSecretKey),
		"",
		clientProfile,
	)
	if err != nil {
		return nil, fmt.Errorf("创建腾讯云 EdgeOne 客户端: %w", err)
	}
	return newEdgeOnePurger(cfg, client, edgeOnePurgeRequestSpacing), nil
}

func newEdgeOnePurger(
	cfg config.Config,
	client edgeOnePurgeClient,
	requestSpacing time.Duration,
) *EdgeOnePurger {
	origin, _ := url.Parse(cfg.COSImageDomain)
	return &EdgeOnePurger{
		client: client, zoneID: cfg.EdgeOneZoneID, imageOrigin: origin,
		gate: newEdgeOneRateGate(requestSpacing), maxAttempts: edgeOnePurgeMaxAttempts,
		retryDelay: edgeOnePurgeRetryDelay, overallTTL: edgeOnePurgeOverallTimeout,
	}
}

// PurgeURL 在总调用方 context 内完成限流、最多两次尝试和固定低基数追踪。
func (p *EdgeOnePurger) PurgeURL(ctx context.Context, publicURL string) (err error) {
	target, err := p.validateCanonicalTarget(publicURL)
	if err != nil {
		return err
	}
	targets := service.ImageCacheURLs(target)
	for _, exactTarget := range targets {
		if err := p.validateExactTarget(exactTarget); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, p.overallTTL)
	defer cancel()
	ctx, span := obs.StartExternalCall(
		ctx, edgeOneInstrumentationName, "tencent_edgeone", "CreatePurgeTask",
	)
	defer func() { obs.EndExternalCall(span, err) }()

	for attempt := 1; attempt <= p.maxAttempts; attempt++ {
		if err = p.gate.Wait(ctx); err != nil {
			return err
		}
		request := teo.NewCreatePurgeTaskRequest()
		request.ZoneId = common.StringPtr(p.zoneID)
		request.Type = common.StringPtr("purge_url")
		request.Targets = common.StringPtrs(targets)
		request.EncodeUrl = common.BoolPtr(false)

		response, requestErr := p.client.CreatePurgeTaskWithContext(ctx, request)
		if requestErr == nil {
			if validEdgeOnePurgeResponse(response) {
				return nil
			}
			return errEdgeOnePurgeResponse
		}
		if attempt == p.maxAttempts || !retryableEdgeOneError(requestErr) {
			return fmt.Errorf("%w after %d attempt(s)", errEdgeOnePurgeRequest, attempt)
		}
		if err = waitContext(ctx, p.retryDelay); err != nil {
			return err
		}
	}
	return errEdgeOnePurgeRequest
}

func (p *EdgeOnePurger) validateCanonicalTarget(raw string) (string, error) {
	if p == nil || p.client == nil || p.imageOrigin == nil ||
		p.zoneID == "" || p.zoneID == "*" {
		return "", errEdgeOnePurgeInvalidTarget
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" ||
		u.Path == "" || u.Path == "/" ||
		!strings.EqualFold(u.Host, p.imageOrigin.Host) {
		return "", errEdgeOnePurgeInvalidTarget
	}
	return u.String(), nil
}

func (p *EdgeOnePurger) validateExactTarget(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		u.Opaque != "" || u.Fragment != "" || u.Path == "" || u.Path == "/" ||
		!strings.EqualFold(u.Host, p.imageOrigin.Host) {
		return errEdgeOnePurgeInvalidTarget
	}
	return nil
}

func validEdgeOnePurgeResponse(response *teo.CreatePurgeTaskResponse) bool {
	return response != nil && response.Response != nil &&
		response.Response.JobId != nil && strings.TrimSpace(*response.Response.JobId) != "" &&
		len(response.Response.FailedList) == 0
}

func retryableEdgeOneError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var sdkErr *tencenterrors.TencentCloudSDKError
	if errors.As(err, &sdkErr) {
		switch sdkErr.GetCode() {
		case "InternalError.BackendError", "InternalError.ProxyServer",
			"InternalError.QuotaSystem", "RequestLimitExceeded":
			return true
		default:
			return false
		}
	}
	// CreatePurgeTask 没有幂等令牌。网络错误可能发生在服务端已受理、客户端未读到响应之后，
	// 此时重放会重复消耗任务配额，所以只重试腾讯云明确返回的瞬态错误码。
	return false
}

func waitContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// edgeOneRateGate 是 burst=1 的进程内间隔限制器。等待 slot 和等待间隔都响应 context，
// 取消的调用不会预约未来时隙；多副本仍须保证副本数 × 10 QPS 不超过账号总配额。
type edgeOneRateGate struct {
	slot     chan struct{}
	last     time.Time
	interval time.Duration
}

func newEdgeOneRateGate(interval time.Duration) *edgeOneRateGate {
	gate := &edgeOneRateGate{slot: make(chan struct{}, 1), interval: interval}
	gate.slot <- struct{}{}
	return gate
}

func (g *edgeOneRateGate) Wait(ctx context.Context) error {
	if g == nil || g.interval <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g.slot:
	}
	defer func() { g.slot <- struct{}{} }()
	delay := time.Until(g.last.Add(g.interval))
	if err := waitContext(ctx, delay); err != nil {
		return err
	}
	g.last = time.Now()
	return nil
}

var _ service.ImageCachePurger = (*EdgeOnePurger)(nil)
