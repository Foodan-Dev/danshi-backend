// Package tencentcloud 提供 COS 对象存储与数据万象审核适配器。
package tencentcloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	cos "github.com/tencentyun/cos-go-sdk-v5"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/config"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

const providerRequestTimeout = 15 * time.Second

// Provider 同时实现 COS 存储、同步文本审核、异步图片送审和回调解码。
type Provider struct {
	cfg       config.Config
	client    *cos.Client
	bucketURL *url.URL
	now       func() time.Time
}

// NewProvider 按腾讯云配置创建一个共享连接池的供应商适配器。
func NewProvider(cfg config.Config, httpClient *http.Client) (*Provider, error) {
	bucketURL, err := cos.NewBucketURL(cfg.COSBucket, cfg.COSRegion, true)
	if err != nil {
		return nil, fmt.Errorf("构造 COS endpoint: %w", err)
	}
	ciURL, err := url.Parse(fmt.Sprintf("https://%s.ci.%s.myqcloud.com", cfg.COSBucket, cfg.COSRegion))
	if err != nil {
		return nil, fmt.Errorf("构造 CI endpoint: %w", err)
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: providerRequestTimeout}
	} else {
		clone := *httpClient
		httpClient = &clone
		if httpClient.Timeout == 0 {
			httpClient.Timeout = providerRequestTimeout
		}
	}
	transport := httpClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	httpClient.Transport = &cos.AuthorizationTransport{
		SecretID: cfg.TencentSecretID, SecretKey: cfg.TencentSecretKey, Transport: transport,
	}
	client := cos.NewClient(&cos.BaseURL{BucketURL: bucketURL, CIURL: ciURL}, httpClient)
	return &Provider{cfg: cfg, client: client, bucketURL: bucketURL, now: time.Now}, nil
}

// PresignPut 生成把 Content-Type、Content-Length 与 Content-MD5 全部签入的 PUT URL。
func (p *Provider) PresignPut(
	ctx context.Context,
	request service.StoragePresignRequest,
) (service.StorageUploadTicket, error) {
	startedAt := p.now().UTC()
	uploadURL, err := PresignCOSPut(
		ctx, p.bucketURL, p.cfg.TencentSecretID, p.cfg.TencentSecretKey, request, startedAt,
	)
	if err != nil {
		return service.StorageUploadTicket{}, err
	}
	return service.StorageUploadTicket{
		UploadURL: uploadURL, ExpiresAt: startedAt.Add(request.TTL),
	}, nil
}

// PresignCOSPut 是可用固定时间和测试向量独立验证的 COS V5 签名纯函数。
func PresignCOSPut(
	ctx context.Context,
	bucketURL *url.URL,
	secretID string,
	secretKey string,
	request service.StoragePresignRequest,
	startedAt time.Time,
) (string, error) {
	if bucketURL == nil || request.ObjectKey == "" || request.TTL <= 0 {
		return "", errors.New("COS 预签名参数不完整")
	}
	headers := http.Header{
		"Content-Type":   []string{request.ContentType},
		"Content-Length": []string{strconv.FormatInt(request.ContentLength, 10)},
		"Content-Md5":    []string{request.ContentMD5},
	}
	client := cos.NewClient(&cos.BaseURL{BucketURL: bucketURL}, nil)
	end := startedAt.Add(request.TTL)
	presigned, err := client.Object.GetPresignedURL(
		ctx, http.MethodPut, request.ObjectKey, secretID, secretKey,
		request.TTL, &cos.PresignedURLOptions{
			Header: &headers,
			AuthTime: &cos.AuthTime{
				SignStartTime: startedAt, SignEndTime: end,
				KeyStartTime: startedAt, KeyEndTime: end,
			},
		},
	)
	if err != nil {
		return "", err
	}
	return presigned.String(), nil
}

// PresignGet 为私有 COS 对象生成短期 GET URL。
func (p *Provider) PresignGet(ctx context.Context, objectKey string, ttl time.Duration) (string, error) {
	return PresignCOSGet(
		ctx, p.bucketURL, p.cfg.TencentSecretID, p.cfg.TencentSecretKey,
		objectKey, ttl, p.now().UTC(),
	)
}

// PresignCOSGet 是可用固定时间独立验证的 COS V5 GET 签名纯函数。
func PresignCOSGet(
	ctx context.Context,
	bucketURL *url.URL,
	secretID string,
	secretKey string,
	objectKey string,
	ttl time.Duration,
	startedAt time.Time,
) (string, error) {
	if bucketURL == nil || objectKey == "" || ttl <= 0 {
		return "", errors.New("COS 预签名参数不完整")
	}
	client := cos.NewClient(&cos.BaseURL{BucketURL: bucketURL}, nil)
	end := startedAt.Add(ttl)
	presigned, err := client.Object.GetPresignedURL(
		ctx, http.MethodGet, objectKey, secretID, secretKey, ttl,
		&cos.PresignedURLOptions{AuthTime: &cos.AuthTime{
			SignStartTime: startedAt, SignEndTime: end,
			KeyStartTime: startedAt, KeyEndTime: end,
		}},
	)
	if err != nil {
		return "", err
	}
	return presigned.String(), nil
}

// HeadObject 校验对象是否存在并返回实际大小。
func (p *Provider) HeadObject(ctx context.Context, objectKey string) (service.StorageObjectMeta, error) {
	response, err := p.client.Object.Head(ctx, objectKey, nil)
	if err != nil {
		if cos.IsNotFoundError(err) {
			return service.StorageObjectMeta{Exists: false}, nil
		}
		return service.StorageObjectMeta{}, err
	}
	length, err := strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64)
	if err != nil {
		return service.StorageObjectMeta{}, fmt.Errorf("COS Content-Length 无效: %w", err)
	}
	return service.StorageObjectMeta{Exists: true, ContentLength: length}, nil
}

// DeleteObject 幂等删除对象。
func (p *Provider) DeleteObject(ctx context.Context, objectKey string) error {
	_, err := p.client.Object.Delete(ctx, objectKey)
	if err != nil && cos.IsNotFoundError(err) {
		return nil
	}
	return err
}

// SetObjectPublicAccess 通过对象级 ACL 幂等切换公开读；private 可逆且不删除原对象。
func (p *Provider) SetObjectPublicAccess(ctx context.Context, objectKey string, public bool) error {
	acl := "private"
	if public {
		acl = "public-read"
	}
	_, err := p.client.Object.PutACL(ctx, objectKey, &cos.ObjectPutACLOptions{
		Header: &cos.ACLHeaderOptions{XCosACL: acl},
	})
	return err
}

// PublicURL 只在 complete 成功后按配置的公开读域名构造 URL。
func (p *Provider) PublicURL(objectKey string) (string, error) {
	base, err := url.Parse(strings.TrimRight(p.cfg.COSImageDomain, "/") + "/")
	if err != nil || base.Host == "" {
		return "", errors.New("COS_IMG_DOMAIN 无效")
	}
	reference := &url.URL{Path: objectKey}
	return base.ResolveReference(reference).String(), nil
}

// Review 调用腾讯 CI 的同步文本内容审核。
func (p *Provider) Review(
	ctx context.Context,
	request service.ModerationRequest,
) (service.ModerationResult, error) {
	response, _, err := p.client.CI.PutTextAuditingJob(ctx, &cos.PutTextAuditingJobOptions{
		InputContent: base64.StdEncoding.EncodeToString([]byte(request.Text)),
		Conf:         &cos.TextAuditingJobConf{BizType: p.cfg.TencentCIBizType},
	})
	if err != nil {
		return service.ModerationResult{}, unavailableModeration(err)
	}
	if response == nil || response.JobsDetail == nil {
		return service.ModerationResult{}, unavailableModeration(errors.New("腾讯 CI 文本审核缺少 JobsDetail"))
	}
	detail := response.JobsDetail
	verdict, err := verdictFromResult(detail.Result)
	if err != nil {
		return service.ModerationResult{}, unavailableModeration(err)
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return service.ModerationResult{}, fmt.Errorf("编码腾讯 CI 文本结果: %w", err)
	}
	var jobID *string
	if strings.TrimSpace(detail.JobId) != "" {
		value := detail.JobId
		jobID = &value
	}
	score := decimal.NewFromInt(int64(detail.Score))
	return service.ModerationResult{
		Provider: model.ModerationProviderTencentCI, ProviderJobID: jobID,
		Verdict: verdict, Labels: textLabels(detail), Score: &score, RawResponse: raw,
	}, nil
}

// SubmitImage 提交带 DataId 的异步图片审核任务。
func (p *Provider) SubmitImage(
	ctx context.Context,
	request service.ImageModerationRequest,
) (service.ImageModerationSubmission, error) {
	callbackURL, err := p.callbackURL()
	if err != nil {
		return service.ImageModerationSubmission{}, unavailableModeration(err)
	}
	response, _, err := p.client.CI.ImageAuditing(ctx, request.ObjectKey, &cos.ImageRecognitionOptions{
		CIProcess: "sensitive-content-recognition", Async: 1,
		BizType: p.cfg.TencentCIBizType, Callback: callbackURL,
		DataId: fmt.Sprintf("image_asset:%d", request.ImageAssetID),
	})
	if err != nil {
		return service.ImageModerationSubmission{}, unavailableModeration(err)
	}
	if response == nil || strings.TrimSpace(response.JobId) == "" {
		return service.ImageModerationSubmission{}, unavailableModeration(
			errors.New("腾讯 CI 图片审核未返回 JobId"),
		)
	}
	jobID := response.JobId
	return service.ImageModerationSubmission{
		Provider: model.ModerationProviderTencentCI, ProviderJobID: &jobID,
	}, nil
}

func (p *Provider) callbackURL() (string, error) {
	u, err := url.Parse(p.cfg.TencentCICallbackURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || p.cfg.ModerationCallbackToken == "" {
		return "", errors.New("腾讯 CI 回调 URL 或令牌无效")
	}
	query := u.Query()
	query.Set("token", p.cfg.ModerationCallbackToken)
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func unavailableModeration(err error) error {
	return apierr.ServiceUnavailable("内容审核暂时不可用，请稍后再试").WithCause(err)
}

func verdictFromResult(result int) (model.ModerationVerdict, error) {
	switch result {
	case 0:
		return model.ModerationVerdictPass, nil
	case 1:
		return model.ModerationVerdictBlock, nil
	case 2:
		return model.ModerationVerdictReview, nil
	default:
		return "", fmt.Errorf("腾讯 CI 返回未知 Result=%d", result)
	}
}

func textLabels(detail *cos.TextAuditingJobDetail) []string {
	labels := []string{detail.Label}
	infos := []struct {
		name string
		info *cos.TextRecognitionInfo
	}{
		{"porn", detail.PornInfo},
		{"illegal", detail.IllegalInfo},
		{"politics", detail.PoliticsInfo},
		{"ad", detail.AdsInfo},
		{"abuse", detail.AbuseInfo},
		{"terrorism", detail.TerrorismInfo},
		{"spam", detail.SpamInfo},
	}
	for _, item := range infos {
		if item.info != nil && item.info.HitFlag > 0 {
			labels = append(labels, item.name)
		}
	}
	return cleanLabels(labels)
}

func cleanLabels(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	labels := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || value == "normal" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		labels = append(labels, value)
	}
	sort.Strings(labels)
	return labels
}

// CallbackDecoder 是不依赖腾讯云凭证的图片审核回调解码器。
type CallbackDecoder struct{}

// DecodeImageCallback 把腾讯 CI Detail JSON 回调转换为供应商无关结论。
func (CallbackDecoder) DecodeImageCallback(body []byte) (service.ImageModerationCallback, error) {
	var callback tencentImageCallback
	if err := json.Unmarshal(body, &callback); err != nil {
		return service.ImageModerationCallback{}, fmt.Errorf("解析腾讯 CI 图片回调: %w", err)
	}
	if callback.EventName != "ReviewImage" || callback.JobsDetail.State != "Success" {
		return service.ImageModerationCallback{}, errors.New("腾讯 CI 图片回调事件或状态无效")
	}
	imageAssetID, err := parseImageDataID(callback.JobsDetail.DataID)
	if err != nil {
		return service.ImageModerationCallback{}, err
	}
	verdict, err := verdictFromResult(callback.JobsDetail.Result)
	if err != nil {
		return service.ImageModerationCallback{}, err
	}
	score := decimal.NewFromInt(int64(callback.JobsDetail.Score))
	return service.ImageModerationCallback{
		ImageAssetID: imageAssetID, ObjectKey: callback.JobsDetail.Object,
		Provider: model.ModerationProviderTencentCI, ProviderJobID: callback.JobsDetail.JobID,
		Verdict: verdict, Labels: imageLabels(callback.JobsDetail), Score: &score,
		RawResponse: append(json.RawMessage(nil), body...),
	}, nil
}

// DecodeImageCallback 让共享供应商实例也能直接作为回调解码端口注入。
func (*Provider) DecodeImageCallback(body []byte) (service.ImageModerationCallback, error) {
	return CallbackDecoder{}.DecodeImageCallback(body)
}

type tencentImageCallback struct {
	EventName  string                     `json:"EventName"`
	JobsDetail tencentImageCallbackDetail `json:"JobsDetail"`
}

type tencentImageCallbackDetail struct {
	JobID        string                   `json:"JobId"`
	State        string                   `json:"State"`
	Object       string                   `json:"Object"`
	DataID       string                   `json:"DataId"`
	Label        string                   `json:"Label"`
	Category     string                   `json:"Category"`
	SubLabel     string                   `json:"SubLabel"`
	Result       int                      `json:"Result"`
	Score        int                      `json:"Score"`
	PornInfo     *tencentImageSceneResult `json:"PornInfo"`
	AdsInfo      *tencentImageSceneResult `json:"AdsInfo"`
	IllegalInfo  *tencentImageSceneResult `json:"IllegalInfo"`
	PoliticsInfo *tencentImageSceneResult `json:"PoliticsInfo"`
}

type tencentImageSceneResult struct {
	HitFlag  int    `json:"HitFlag"`
	Label    string `json:"Label"`
	Category string `json:"Category"`
	SubLabel string `json:"SubLabel"`
}

func parseImageDataID(value string) (uint64, error) {
	raw, found := strings.CutPrefix(value, "image_asset:")
	if !found {
		return 0, errors.New("腾讯 CI 图片回调 DataId 无效")
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, errors.New("腾讯 CI 图片回调 DataId 无效")
	}
	return id, nil
}

func imageLabels(detail tencentImageCallbackDetail) []string {
	labels := []string{detail.Label, detail.Category, detail.SubLabel}
	infos := []struct {
		name string
		info *tencentImageSceneResult
	}{
		{"porn", detail.PornInfo},
		{"ad", detail.AdsInfo},
		{"illegal", detail.IllegalInfo},
		{"politics", detail.PoliticsInfo},
	}
	for _, item := range infos {
		if item.info == nil || item.info.HitFlag == 0 {
			continue
		}
		labels = append(labels, item.name, item.info.Label, item.info.Category, item.info.SubLabel)
	}
	return cleanLabels(labels)
}
