package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
)

// ModerationAlert 是交给告警渠道的供应商无关载荷。
type ModerationAlert struct {
	Kind          ModerationAlertKind
	Target        ModerationTarget
	TargetID      uint64
	Field         *model.ModerationField
	Provider      model.ModerationProvider
	ProviderJobID *string
	Verdict       model.ModerationVerdict
	Labels        []string
	FailureCode   apierr.BizCode
	ErrorID       string
	Occurrences   int
	WindowSeconds int
	QueueDepth    int64
	Threshold     int64
}

// ModerationAlertKind 区分审核结论与需要人工介入的运行异常。
type ModerationAlertKind string

// 审核告警的供应商无关类型。
const (
	ModerationAlertKindVerdict                  ModerationAlertKind = "verdict"
	ModerationAlertKindCallbackAuthFailures     ModerationAlertKind = "callback_auth_failures"
	ModerationAlertKindCallbackPayloadInvalid   ModerationAlertKind = "callback_payload_invalid"
	ModerationAlertKindCallbackTargetInvalid    ModerationAlertKind = "callback_target_invalid"
	ModerationAlertKindCallbackProcessingFailed ModerationAlertKind = "callback_processing_failed"
	ModerationAlertKindReviewBacklog            ModerationAlertKind = "review_backlog"
)

// EffectiveKind 把旧调用点的零值解释为既有审核结论告警。
func (a ModerationAlert) EffectiveKind() ModerationAlertKind {
	if a.Kind == "" {
		return ModerationAlertKindVerdict
	}
	return a.Kind
}

// ModerationAlerter 是飞书、邮件等管理员告警渠道的替换边界。
type ModerationAlerter interface {
	Alert(ctx context.Context, alert ModerationAlert)
}

// LogModerationAlerter 是开发环境使用的结构化日志告警器。
type LogModerationAlerter struct{ log *slog.Logger }

// NewLogModerationAlerter 创建日志告警器。
func NewLogModerationAlerter(log *slog.Logger) *LogModerationAlerter {
	return &LogModerationAlerter{log: log}
}

// Alert 记录需要管理员关注的审核结论，不记录被审原文。
func (a *LogModerationAlerter) Alert(ctx context.Context, alert ModerationAlert) {
	if a == nil || a.log == nil {
		return
	}
	a.log.WarnContext(ctx, "审核事件需要管理员关注",
		slog.String("alert_kind", string(alert.EffectiveKind())),
		slog.String("target", string(alert.Target)),
		slog.Uint64("target_id", alert.TargetID),
		slog.String("provider", string(alert.Provider)),
		slog.String("verdict", string(alert.Verdict)),
		slog.Any("labels", alert.Labels),
		slog.String("failure_code", string(alert.FailureCode)),
		slog.String("error_id", alert.ErrorID),
		slog.Int("occurrences", alert.Occurrences),
		slog.Int("window_seconds", alert.WindowSeconds),
		slog.Int64("queue_depth", alert.QueueDepth),
		slog.Int64("threshold", alert.Threshold))
}

// DiscardModerationAlerter 是测试可显式使用的空告警器。
type DiscardModerationAlerter struct{}

// Alert 明确丢弃告警。
func (DiscardModerationAlerter) Alert(context.Context, ModerationAlert) {}

// GenericUserModerationAlerter 把旧的用户域告警端口桥接到统一告警端口。
type GenericUserModerationAlerter struct{ Alerter ModerationAlerter }

// AlertUserContent 转换用户字段告警载荷。
func (a GenericUserModerationAlerter) AlertUserContent(ctx context.Context, alert UserModerationAlert) {
	if a.Alerter == nil {
		return
	}
	a.Alerter.Alert(ctx, ModerationAlert{
		Kind:   ModerationAlertKindVerdict,
		Target: ModerationTargetUser, TargetID: alert.UserID, Field: &alert.Field,
		Verdict: alert.Verdict, Labels: append([]string{}, alert.Labels...),
	})
}

// ImageModerationRequest 是上传完成后提交给图片审核供应商的稳定输入。
type ImageModerationRequest struct {
	ImageAssetID uint64
	ObjectKey    string
}

// ImageModerationSubmission 表示异步任务已受理，或开发实现已同步给出结论。
type ImageModerationSubmission struct {
	Provider      model.ModerationProvider
	ProviderJobID *string
	Immediate     *ImageModerationCallback
}

// ImageModerator 是可替换的图片送审端口。
type ImageModerator interface {
	SubmitImage(ctx context.Context, request ImageModerationRequest) (ImageModerationSubmission, error)
}

// ImageModerationCallback 是解析供应商回调后的统一结论。
type ImageModerationCallback struct {
	ImageAssetID  uint64
	ObjectKey     string
	Provider      model.ModerationProvider
	ProviderJobID string
	Verdict       model.ModerationVerdict
	Labels        []string
	Score         *decimal.Decimal
	RawResponse   json.RawMessage
}

// ImageCallbackDecoder 把具体供应商回调解码为统一结论。
type ImageCallbackDecoder interface {
	DecodeImageCallback(body []byte) (ImageModerationCallback, error)
}

// ImageAccessChange 描述审核结论要求的对象可见性与 CDN 缓存状态。
type ImageAccessChange struct {
	ImageAssetID             uint64
	SourceModerationRecordID uint64
	Public                   bool
	PurgeRequired            bool
}

// ImageAccessController 在审核事务内持久化访问状态意图；失败必须回滚审核结论。
type ImageAccessController interface {
	Apply(ctx context.Context, change ImageAccessChange) error
}

// DiscardImageAccessController 供不涉及对象访问控制的独立服务测试显式使用。
type DiscardImageAccessController struct{}

// Apply 明确丢弃对象访问控制副作用。
func (DiscardImageAccessController) Apply(context.Context, ImageAccessChange) error { return nil }

// ImageCachePurgeTaskState 是 EdgeOne 三个精确 Target 聚合后的有限状态集合。
type ImageCachePurgeTaskState string

const (
	// ImageCachePurgeProcessing 表示至少一个精确 Target 仍在处理。
	ImageCachePurgeProcessing ImageCachePurgeTaskState = "processing"
	// ImageCachePurgeSuccess 表示全部精确 Target 成功。
	ImageCachePurgeSuccess ImageCachePurgeTaskState = "success"
	// ImageCachePurgeFailed 表示至少一个 Target 失败。
	ImageCachePurgeFailed ImageCachePurgeTaskState = "failed"
	// ImageCachePurgeTimeout 表示至少一个 Target 超时。
	ImageCachePurgeTimeout ImageCachePurgeTaskState = "timeout"
	// ImageCachePurgeCanceled 表示至少一个 Target 被取消。
	ImageCachePurgeCanceled ImageCachePurgeTaskState = "canceled"
	// ImageCachePurgeProtocolUnknown 表示返回集合不满足精确协议。
	ImageCachePurgeProtocolUnknown ImageCachePurgeTaskState = "protocol_unknown"
)

// ImageCachePurgeSubmission 保留已受理 JobId；Partial 表示 Create 即时报告了部分 Target 失败。
type ImageCachePurgeSubmission struct {
	JobID   string
	Partial bool
}

// ImageCachePurgeRecovery 是 response-unknown 窗口的只读对账结果。
// EffectSucceeded 只表示三个精确 Target 的刷新效果已确认，不表示能将效果因果归属到某次提交。
// Ambiguous 表示无法唯一归属提交；它可与 EffectSucceeded 同时为 true。
type ImageCachePurgeRecovery struct {
	Found           bool
	EffectSucceeded bool
	Ambiguous       bool
	JobID           string
	State           ImageCachePurgeTaskState
}

// ImageCachePurgeTaskProvider 暴露 durable worker 所需的提交、终态查询与未知响应对账。
type ImageCachePurgeTaskProvider interface {
	Submit(ctx context.Context, publicURL string) (ImageCachePurgeSubmission, error)
	Describe(
		ctx context.Context,
		publicURL string,
		jobID string,
	) (ImageCachePurgeTaskState, error)
	Recover(
		ctx context.Context,
		publicURL string,
		startedAt time.Time,
		endedAt time.Time,
	) (ImageCachePurgeRecovery, error)
}

// ImageCachePurgeErrorKind 控制 Create 的“是否可能已受理”与 worker 的确定性重试边界。
type ImageCachePurgeErrorKind string

const (
	// ImageCachePurgeErrorUnknown 表示 Create 可能已被受理，禁止直接重放。
	ImageCachePurgeErrorUnknown ImageCachePurgeErrorKind = "unknown"
	// ImageCachePurgeErrorRetryable 表示结构化瞬态拒绝，对账后可按预算重试。
	ImageCachePurgeErrorRetryable ImageCachePurgeErrorKind = "retryable"
	// ImageCachePurgeErrorPermanent 表示参数、权限或协议错误，应立即 dead-letter。
	ImageCachePurgeErrorPermanent ImageCachePurgeErrorKind = "permanent"
)

type imageCachePurgeError struct{ kind ImageCachePurgeErrorKind }

func (e imageCachePurgeError) Error() string { return "image cache purge provider call failed" }

// NewImageCachePurgeError 创建不携带供应商正文的分类错误。
func NewImageCachePurgeError(kind ImageCachePurgeErrorKind) error {
	return imageCachePurgeError{kind: kind}
}

// ClassifyImageCachePurgeError 将任意未分类错误收敛为 permanent，避免无界重试。
func ClassifyImageCachePurgeError(err error) ImageCachePurgeErrorKind {
	var classified imageCachePurgeError
	if errors.As(err, &classified) {
		return classified.kind
	}
	return ImageCachePurgeErrorPermanent
}

// DirectPassImageModerator 是 dev/test 使用的同步图片放行实现。
type DirectPassImageModerator struct{}

// SubmitImage 返回可追溯的 dev_allow 结论。
func (DirectPassImageModerator) SubmitImage(
	_ context.Context,
	request ImageModerationRequest,
) (ImageModerationSubmission, error) {
	callback := &ImageModerationCallback{
		ImageAssetID: request.ImageAssetID, ObjectKey: request.ObjectKey,
		Provider: model.ModerationProvider("dev_allow"), Verdict: model.ModerationVerdictPass,
		Labels: []string{},
	}
	return ImageModerationSubmission{Provider: callback.Provider, Immediate: callback}, nil
}

// UnavailableImageModerator 是生产未装配图片审核时的 fail-closed 实现。
type UnavailableImageModerator struct{}

// SubmitImage 拒绝伪装图片已送审。
func (UnavailableImageModerator) SubmitImage(
	context.Context,
	ImageModerationRequest,
) (ImageModerationSubmission, error) {
	return ImageModerationSubmission{}, apierr.ServiceUnavailable("图片审核暂时不可用，请稍后再试").
		WithCause(errors.New("image moderation provider is not configured"))
}
