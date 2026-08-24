package tencentcloud

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
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
	edgeOneEndpoint             = "teo.tencentcloudapi.com"
	edgeOneRequestTimeout       = 5 * time.Second
	edgeOnePurgeOverallTimeout  = 12 * time.Second
	edgeOnePurgeRequestSpacing  = 100 * time.Millisecond
	edgeOneDescribePageLimit    = int64(1000)
	edgeOneDescribeMaxPages     = 5
	edgeOneRecoverMaxCandidates = 10
	edgeOneInstrumentationName  = "github.com/Foodan-Dev/danshi-backend/internal/infra/tencentcloud/edgeone"
)

var errEdgeOnePurgeInvalidTarget = errors.New("edgeone cache purge target is invalid")

type edgeOnePurgeClient interface {
	CreatePurgeTaskWithContext(context.Context, *teo.CreatePurgeTaskRequest) (*teo.CreatePurgeTaskResponse, error)
	DescribePurgeTasksWithContext(context.Context, *teo.DescribePurgeTasksRequest) (*teo.DescribePurgeTasksResponse, error)
}

// EdgeOnePurger 只提交 COS_IMG_DOMAIN 下 raw/display/thumb 三个精确 HTTPS URL。
// Create 没有幂等 token，SDK 自动重试全部关闭；response unknown 只能由 Recover 只读对账。
type EdgeOnePurger struct {
	client      edgeOnePurgeClient
	zoneID      string
	imageOrigin *url.URL
	gate        *edgeOneRateGate
	overallTTL  time.Duration
}

// NewEdgeOnePurger 使用官方腾讯云 Go SDK 创建 EdgeOne durable task provider。
func NewEdgeOnePurger(cfg config.Config) (*EdgeOnePurger, error) {
	if !cfg.EdgeOneConfigured() {
		return nil, errors.New("EdgeOne URL 刷新配置不完整")
	}
	clientProfile := profile.NewClientProfile()
	clientProfile.HttpProfile.Endpoint = edgeOneEndpoint
	clientProfile.HttpProfile.ReqTimeout = int(edgeOneRequestTimeout / time.Second)
	clientProfile.NetworkFailureMaxRetries = 0
	clientProfile.RateLimitExceededMaxRetries = 0
	clientProfile.UnsafeRetryOnConnectionFailure = false
	client, err := teo.NewClient(
		common.NewCredential(cfg.TencentSecretID, cfg.TencentSecretKey), "", clientProfile,
	)
	if err != nil {
		return nil, fmt.Errorf("创建腾讯云 EdgeOne 客户端: %w", err)
	}
	return newEdgeOnePurger(cfg, client, edgeOnePurgeRequestSpacing), nil
}

func newEdgeOnePurger(cfg config.Config, client edgeOnePurgeClient, spacing time.Duration) *EdgeOnePurger {
	origin, _ := url.Parse(cfg.COSImageDomain)
	return &EdgeOnePurger{
		client: client, zoneID: cfg.EdgeOneZoneID, imageOrigin: origin,
		gate: newEdgeOneRateGate(spacing), overallTTL: edgeOnePurgeOverallTimeout,
	}
}

// Submit 发送且只发送一次 CreatePurgeTask，并返回可持久化 JobId。
func (p *EdgeOnePurger) Submit(
	ctx context.Context,
	publicURL string,
) (submission service.ImageCachePurgeSubmission, err error) {
	targets, err := p.exactTargets(publicURL)
	if err != nil {
		return submission, service.NewImageCachePurgeError(service.ImageCachePurgeErrorPermanent)
	}
	ctx, cancel := context.WithTimeout(ctx, p.overallTTL)
	defer cancel()
	ctx, span := obs.StartExternalCall(ctx, edgeOneInstrumentationName, "tencent_edgeone", "CreatePurgeTask")
	defer func() { obs.EndExternalCall(span, err) }()
	if err = p.gate.Wait(ctx); err != nil {
		return submission, service.NewImageCachePurgeError(service.ImageCachePurgeErrorUnknown)
	}
	request := teo.NewCreatePurgeTaskRequest()
	request.ZoneId = common.StringPtr(p.zoneID)
	request.Type = common.StringPtr("purge_url")
	request.Targets = common.StringPtrs(targets)
	request.EncodeUrl = common.BoolPtr(false)
	response, requestErr := p.client.CreatePurgeTaskWithContext(ctx, request)
	if requestErr != nil {
		return submission, classifiedCreateError(requestErr)
	}
	if response == nil || response.Response == nil || response.Response.JobId == nil ||
		strings.TrimSpace(*response.Response.JobId) == "" {
		return submission, service.NewImageCachePurgeError(service.ImageCachePurgeErrorUnknown)
	}
	return service.ImageCachePurgeSubmission{
		JobID:   strings.TrimSpace(*response.Response.JobId),
		Partial: len(response.Response.FailedList) > 0,
	}, nil
}

// Describe 按 job-id filter 分页读取每个 Target，并聚合三个精确 URL 的整体状态。
func (p *EdgeOnePurger) Describe(
	ctx context.Context,
	publicURL string,
	jobID string,
) (state service.ImageCachePurgeTaskState, err error) {
	targets, err := p.exactTargets(publicURL)
	if err != nil || strings.TrimSpace(jobID) == "" {
		return service.ImageCachePurgeProtocolUnknown,
			service.NewImageCachePurgeError(service.ImageCachePurgeErrorPermanent)
	}
	ctx, cancel := context.WithTimeout(ctx, p.overallTTL)
	defer cancel()
	ctx, span := obs.StartExternalCall(ctx, edgeOneInstrumentationName, "tencent_edgeone", "DescribePurgeTasks")
	defer func() { obs.EndExternalCall(span, err) }()
	tasks, err := p.describePages(ctx, func(offset int64) *teo.DescribePurgeTasksRequest {
		request := teo.NewDescribePurgeTasksRequest()
		request.ZoneId = common.StringPtr(p.zoneID)
		request.Offset = common.Int64Ptr(offset)
		request.Limit = common.Int64Ptr(edgeOneDescribePageLimit)
		request.Filters = []*teo.AdvancedFilter{{
			Name: common.StringPtr("job-id"), Values: common.StringPtrs([]string{jobID}),
			Fuzzy: common.BoolPtr(false),
		}}
		return request
	})
	if err != nil {
		return service.ImageCachePurgeProtocolUnknown, err
	}
	return aggregatePurgeTasks(jobID, targets, tasks), nil
}

// Recover 对 Create response unknown 做窄时间窗只读对账；它从不创建新任务。
func (p *EdgeOnePurger) Recover(
	ctx context.Context,
	publicURL string,
	startedAt time.Time,
	endedAt time.Time,
) (recovery service.ImageCachePurgeRecovery, err error) {
	targets, err := p.exactTargets(publicURL)
	if err != nil || !startedAt.Before(endedAt) || endedAt.Sub(startedAt) > 7*24*time.Hour {
		return recovery, service.NewImageCachePurgeError(service.ImageCachePurgeErrorPermanent)
	}
	ctx, cancel := context.WithTimeout(ctx, p.overallTTL)
	defer cancel()
	ctx, span := obs.StartExternalCall(ctx, edgeOneInstrumentationName, "tencent_edgeone", "DescribePurgeTasksRecover")
	defer func() { obs.EndExternalCall(span, err) }()
	request := teo.NewDescribePurgeTasksRequest()
	request.ZoneId = common.StringPtr(p.zoneID)
	request.StartTime = common.StringPtr(startedAt.UTC().Format(time.RFC3339))
	request.EndTime = common.StringPtr(endedAt.UTC().Format(time.RFC3339))
	request.Offset = common.Int64Ptr(0)
	request.Limit = common.Int64Ptr(edgeOneDescribePageLimit)
	request.Filters = []*teo.AdvancedFilter{{
		Name: common.StringPtr("target"), Values: common.StringPtrs([]string{targets[0]}),
		Fuzzy: common.BoolPtr(false),
	}}
	if err = p.gate.Wait(ctx); err != nil {
		return recovery, service.NewImageCachePurgeError(service.ImageCachePurgeErrorRetryable)
	}
	response, requestErr := p.client.DescribePurgeTasksWithContext(ctx, request)
	if requestErr != nil {
		return recovery, classifiedReadError(requestErr)
	}
	if response == nil || response.Response == nil || response.Response.TotalCount == nil {
		return recovery, service.NewImageCachePurgeError(service.ImageCachePurgeErrorRetryable)
	}
	if *response.Response.TotalCount > uint64(edgeOneDescribePageLimit) {
		return service.ImageCachePurgeRecovery{Ambiguous: true}, nil
	}
	if uint64(len(response.Response.Tasks)) != *response.Response.TotalCount {
		return recovery, service.NewImageCachePurgeError(service.ImageCachePurgeErrorRetryable)
	}
	jobIDs, uncertain := candidateJobIDs(response.Response.Tasks, targets, startedAt, endedAt)
	if uncertain {
		return service.ImageCachePurgeRecovery{Ambiguous: true}, nil
	}
	if len(jobIDs) == 0 {
		return recovery, nil
	}
	if len(jobIDs) > edgeOneRecoverMaxCandidates {
		return service.ImageCachePurgeRecovery{Ambiguous: true}, nil
	}
	type candidate struct {
		jobID string
		state service.ImageCachePurgeTaskState
	}
	candidates := make([]candidate, 0, len(jobIDs))
	effectSucceeded := false
	for _, jobID := range jobIDs {
		state, describeErr := p.Describe(ctx, publicURL, jobID)
		if describeErr != nil {
			return recovery, describeErr
		}
		if state == service.ImageCachePurgeSuccess {
			effectSucceeded = true
		}
		candidates = append(candidates, candidate{jobID: jobID, state: state})
	}
	if len(candidates) == 1 {
		return service.ImageCachePurgeRecovery{
			Found:           true,
			EffectSucceeded: effectSucceeded,
			JobID:           candidates[0].jobID,
			State:           candidates[0].state,
		}, nil
	}
	// CreatePurgeTask 没有幂等 token，多候选时仍不能把某个 Job 因果归属到未知响应，
	// 因此不返回 Found/JobID 并保留 Ambiguous。若任一候选已严格确认三个精确 Target
	// 全部成功，则缓存效果本身已经成立，与无法确定由哪个 Job 造成是两件事。
	return service.ImageCachePurgeRecovery{
		EffectSucceeded: effectSucceeded,
		Ambiguous:       true,
	}, nil
}

func (p *EdgeOnePurger) describePages(
	ctx context.Context,
	requestAt func(int64) *teo.DescribePurgeTasksRequest,
) ([]*teo.Task, error) {
	seen := make(map[string]struct{})
	tasks := make([]*teo.Task, 0, 3)
	var offset int64
	for page := 0; page < edgeOneDescribeMaxPages; page++ {
		if err := p.gate.Wait(ctx); err != nil {
			return nil, service.NewImageCachePurgeError(service.ImageCachePurgeErrorRetryable)
		}
		response, err := p.client.DescribePurgeTasksWithContext(ctx, requestAt(offset))
		if err != nil {
			return nil, classifiedReadError(err)
		}
		if response == nil || response.Response == nil || response.Response.TotalCount == nil {
			return nil, service.NewImageCachePurgeError(service.ImageCachePurgeErrorRetryable)
		}
		pageTasks := response.Response.Tasks
		if len(pageTasks) == 0 && uint64(offset) < *response.Response.TotalCount {
			return nil, service.NewImageCachePurgeError(service.ImageCachePurgeErrorRetryable)
		}
		for _, task := range pageTasks {
			key := taskKey(task)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			tasks = append(tasks, task)
		}
		offset += int64(len(pageTasks))
		if uint64(offset) >= *response.Response.TotalCount {
			return tasks, nil
		}
	}
	return nil, service.NewImageCachePurgeError(service.ImageCachePurgeErrorRetryable)
}

func aggregatePurgeTasks(jobID string, expected []string, tasks []*teo.Task) service.ImageCachePurgeTaskState {
	states := make(map[string]string, len(expected))
	allowed := make(map[string]struct{}, len(expected))
	for _, target := range expected {
		allowed[target] = struct{}{}
	}
	for _, task := range tasks {
		if task == nil || task.JobId == nil || task.Target == nil || task.Type == nil ||
			task.Status == nil || *task.JobId != jobID || *task.Type != "purge_url" {
			return service.ImageCachePurgeProtocolUnknown
		}
		if _, ok := allowed[*task.Target]; !ok {
			return service.ImageCachePurgeProtocolUnknown
		}
		if previous, duplicate := states[*task.Target]; duplicate && previous != *task.Status {
			return service.ImageCachePurgeProtocolUnknown
		}
		states[*task.Target] = *task.Status
	}
	if len(states) != len(expected) {
		return service.ImageCachePurgeProcessing
	}
	hasProcessing := false
	for _, target := range expected {
		switch states[target] {
		case "success":
		case "processing":
			hasProcessing = true
		case "failed":
			return service.ImageCachePurgeFailed
		case "timeout":
			return service.ImageCachePurgeTimeout
		case "canceled":
			return service.ImageCachePurgeCanceled
		default:
			return service.ImageCachePurgeProtocolUnknown
		}
	}
	if hasProcessing {
		return service.ImageCachePurgeProcessing
	}
	return service.ImageCachePurgeSuccess
}

func candidateJobIDs(
	tasks []*teo.Task,
	exactTargets []string,
	startedAt time.Time,
	endedAt time.Time,
) ([]string, bool) {
	allowed := make(map[string]struct{}, len(exactTargets))
	for _, target := range exactTargets {
		allowed[target] = struct{}{}
	}
	// EdgeOne 的 CreateTime 只有秒级精度；查询参数同样按 RFC3339 秒级发送。
	// 因此以查询实际表达的闭区间做本地复核，绝不接受窗口之外的旧任务。
	windowStart := startedAt.UTC().Truncate(time.Second)
	windowEnd := endedAt.UTC().Truncate(time.Second)
	set := make(map[string]struct{})
	for _, task := range tasks {
		if task == nil || task.Target == nil {
			continue
		}
		if _, ok := allowed[*task.Target]; !ok {
			continue
		}
		if task.JobId == nil || task.Type == nil || task.CreateTime == nil ||
			*task.Type != "purge_url" || strings.TrimSpace(*task.JobId) == "" {
			return nil, true
		}
		createdAt, err := time.Parse(time.RFC3339, *task.CreateTime)
		if err != nil {
			return nil, true
		}
		createdAt = createdAt.UTC()
		if createdAt.Before(windowStart) || createdAt.After(windowEnd) {
			continue
		}
		set[strings.TrimSpace(*task.JobId)] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for jobID := range set {
		result = append(result, jobID)
	}
	sort.Strings(result)
	return result, false
}

func taskKey(task *teo.Task) string {
	if task == nil || task.JobId == nil || task.Target == nil {
		return "invalid"
	}
	return *task.JobId + "\x00" + *task.Target
}

func classifiedCreateError(err error) error {
	var sdkErr *tencenterrors.TencentCloudSDKError
	if errors.As(err, &sdkErr) {
		switch sdkErr.GetCode() {
		case "InternalError.BackendError", "InternalError.ProxyServer",
			"InternalError.QuotaSystem", "RequestLimitExceeded":
			return service.NewImageCachePurgeError(service.ImageCachePurgeErrorRetryable)
		default:
			return service.NewImageCachePurgeError(service.ImageCachePurgeErrorPermanent)
		}
	}
	return service.NewImageCachePurgeError(service.ImageCachePurgeErrorUnknown)
}

func classifiedReadError(err error) error {
	var sdkErr *tencenterrors.TencentCloudSDKError
	if errors.As(err, &sdkErr) {
		switch sdkErr.GetCode() {
		case "UnauthorizedOperation.CamUnauthorized", "AuthFailure.SecretIdNotFound",
			"InvalidParameter", "InvalidParameter.ParameterError":
			return service.NewImageCachePurgeError(service.ImageCachePurgeErrorPermanent)
		default:
			return service.NewImageCachePurgeError(service.ImageCachePurgeErrorRetryable)
		}
	}
	return service.NewImageCachePurgeError(service.ImageCachePurgeErrorRetryable)
}

func (p *EdgeOnePurger) exactTargets(raw string) ([]string, error) {
	target, err := p.validateCanonicalTarget(raw)
	if err != nil {
		return nil, err
	}
	targets := service.ImageCacheURLs(target)
	for _, exactTarget := range targets {
		if err := p.validateExactTarget(exactTarget); err != nil {
			return nil, err
		}
	}
	return targets, nil
}

func (p *EdgeOnePurger) validateCanonicalTarget(raw string) (string, error) {
	if p == nil || p.client == nil || p.imageOrigin == nil || p.zoneID == "" || p.zoneID == "*" {
		return "", errEdgeOnePurgeInvalidTarget
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" || u.Path == "" || u.Path == "/" ||
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

var _ service.ImageCachePurgeTaskProvider = (*EdgeOnePurger)(nil)
