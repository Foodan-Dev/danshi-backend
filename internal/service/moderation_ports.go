package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/shopspring/decimal"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/model"
)

// ModerationAlert 是交给告警渠道的供应商无关载荷。
type ModerationAlert struct {
	Target        ModerationTarget
	TargetID      uint64
	Field         *model.ModerationField
	Provider      model.ModerationProvider
	ProviderJobID *string
	Verdict       model.ModerationVerdict
	Labels        []string
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
	a.log.WarnContext(ctx, "内容审核需要管理员关注",
		slog.String("target", string(alert.Target)),
		slog.Uint64("target_id", alert.TargetID),
		slog.String("provider", string(alert.Provider)),
		slog.String("verdict", string(alert.Verdict)),
		slog.Any("labels", alert.Labels))
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
	ImageAssetID uint64
	ObjectKey    string
	PublicURL    string
	Public       bool
}

// ImageAccessController 在事务提交后应用对象 ACL 并刷新公开 URL 的缓存。
// 该端口不返回错误，确保外部存储故障不会回滚已经落库的审核结论。
type ImageAccessController interface {
	Apply(ctx context.Context, change ImageAccessChange)
}

// DiscardImageAccessController 供不涉及对象访问控制的独立服务测试显式使用。
type DiscardImageAccessController struct{}

// Apply 明确丢弃对象访问控制副作用。
func (DiscardImageAccessController) Apply(context.Context, ImageAccessChange) {}

// ImageCachePurger 隔离 CDN 缓存刷新供应商；本批不绑定 EdgeOne SDK。
type ImageCachePurger interface {
	PurgeURL(ctx context.Context, publicURL string) error
}

var errImageCachePurgerUnconfigured = errors.New("image cache purger is not configured")

// UnavailableImageCachePurger 让未装配 CDN 刷新能力显式失败并由调用端记录。
type UnavailableImageCachePurger struct{}

// PurgeURL 拒绝伪装缓存已经刷新。
func (UnavailableImageCachePurger) PurgeURL(context.Context, string) error {
	return errImageCachePurgerUnconfigured
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
