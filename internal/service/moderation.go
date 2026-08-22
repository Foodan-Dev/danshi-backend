package service

import (
	"context"
	"errors"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/model"
)

// ModerationTarget 标识审核请求的对象类别。
type ModerationTarget string

// 当前 Post 与 Comment 域会提交的审核对象。
const (
	ModerationTargetPost    ModerationTarget = "post"
	ModerationTargetTag     ModerationTarget = "tag"
	ModerationTargetComment ModerationTarget = "comment"
	ModerationTargetUser    ModerationTarget = "user"
)

// ModerationRequest 是与具体供应商无关的文本审核请求。
type ModerationRequest struct {
	Target ModerationTarget
	Field  *model.ModerationField
	Text   string
}

// ModerationResult 是一次可以落入 moderation_records 的供应商结论。
type ModerationResult struct {
	Provider      model.ModerationProvider
	ProviderJobID *string
	Verdict       model.ModerationVerdict
	Labels        []string
}

// ContentModerator 是可替换的内容审核端口。
type ContentModerator interface {
	Review(ctx context.Context, request ModerationRequest) (ModerationResult, error)
}

// UserModerationAlert 是昵称或简介违规后交给管理员告警适配器的稳定载荷。
type UserModerationAlert struct {
	UserID  uint64
	Field   model.ModerationField
	Verdict model.ModerationVerdict
	Labels  []string
}

// UserModerationAlerter 是用户内容违规告警端口；具体飞书适配器由 moderation 域实现。
type UserModerationAlerter interface {
	AlertUserContent(ctx context.Context, alert UserModerationAlert)
}

// DiscardUserModerationAlerter 用于尚未装配管理员告警适配器的环境。
type DiscardUserModerationAlerter struct{}

// AlertUserContent 明确丢弃告警；审核流水仍是人工队列的数据真源。
func (DiscardUserModerationAlerter) AlertUserContent(context.Context, UserModerationAlert) {}

// DirectPassContentModerator 是 dev/test 使用的同步直接放行实现。
type DirectPassContentModerator struct{}

// Review 返回可追溯的 dev_allow 通过结论。
func (DirectPassContentModerator) Review(
	context.Context,
	ModerationRequest,
) (ModerationResult, error) {
	return ModerationResult{
		Provider: model.ModerationProvider("dev_allow"), Verdict: model.ModerationVerdictPass,
		Labels: []string{},
	}, nil
}

// UnavailableContentModerator 是生产环境未装配真实供应商时的 fail-closed 实现。
type UnavailableContentModerator struct{}

// Review 明确拒绝伪装审核成功。
func (UnavailableContentModerator) Review(
	context.Context,
	ModerationRequest,
) (ModerationResult, error) {
	return ModerationResult{}, apierr.ServiceUnavailable("内容审核暂时不可用，请稍后再试").
		WithCause(errors.New("content moderation provider is not configured"))
}
