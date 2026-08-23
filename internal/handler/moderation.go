package handler

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/envelope"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

// Moderation 接收供应商异步审核回调。
type Moderation struct {
	service *service.ModerationService
	decoder service.ImageCallbackDecoder
	token   string
}

// NewModeration 创建回调 handler。
func NewModeration(
	moderationService *service.ModerationService,
	decoder service.ImageCallbackDecoder,
	token string,
) *Moderation {
	return &Moderation{service: moderationService, decoder: decoder, token: token}
}

// TencentCICallback 校验共享回调令牌、解码腾讯 CI 载荷并幂等写入结论。
func (h *Moderation) TencentCICallback(ctx context.Context, c *app.RequestContext) {
	if strings.TrimSpace(h.token) == "" || h.decoder == nil {
		failService(ctx, c, apierr.ServiceUnavailable("审核回调暂时不可用"))
		return
	}
	query, err := bindQuery[moderationCallbackQuery](c)
	if err != nil {
		failService(ctx, c, err)
		return
	}
	provided := query.Token
	providedDigest := sha256.Sum256([]byte(provided))
	expectedDigest := sha256.Sum256([]byte(h.token))
	if subtle.ConstantTimeCompare(providedDigest[:], expectedDigest[:]) != 1 {
		failService(ctx, c, apierr.Forbidden(
			apierr.BizModerationCallbackInvalid, "审核回调凭证无效",
		))
		return
	}
	callback, err := h.decoder.DecodeImageCallback(c.Request.Body())
	if err != nil {
		failService(ctx, c, apierr.BadRequest(
			apierr.BizModerationCallbackInvalid, "审核回调载荷无效",
		).WithCause(err))
		return
	}
	result, err := h.service.ApplyImageCallback(ctx, callback)
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("审核回调已处理", result))
}
