package service

import (
	"context"
	"errors"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/model"
)

// ModerationTarget 标识审核请求的对象类别。
type ModerationTarget string

// 当前 Post 域会提交的审核对象。
const (
	ModerationTargetPost ModerationTarget = "post"
	ModerationTargetTag  ModerationTarget = "tag"
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
