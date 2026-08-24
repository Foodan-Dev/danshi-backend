package handler

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/httpx"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/envelope"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/obs"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

// Moderation 接收供应商异步审核回调。
type Moderation struct {
	service      *service.ModerationService
	decoder      service.ImageCallbackDecoder
	token        string
	authFailures *service.CallbackAuthFailureMonitor
	metrics      obs.BusinessRecorder
}

// NewModeration 创建回调 handler。
func NewModeration(
	moderationService *service.ModerationService,
	decoder service.ImageCallbackDecoder,
	token string,
	authFailures *service.CallbackAuthFailureMonitor,
	metrics obs.BusinessRecorder,
) *Moderation {
	return &Moderation{
		service: moderationService, decoder: decoder, token: token, authFailures: authFailures,
		metrics: metrics,
	}
}

// TencentCICallback 校验共享回调令牌、解码腾讯 CI 载荷并幂等写入结论。
func (h *Moderation) TencentCICallback(ctx context.Context, c *app.RequestContext) {
	if strings.TrimSpace(h.token) == "" || h.decoder == nil {
		h.recordCallback(ctx, "invalid", "unavailable")
		failService(ctx, c, apierr.ServiceUnavailable("审核回调暂时不可用"))
		return
	}
	query, err := bindQuery[moderationCallbackQuery](c)
	if err != nil {
		h.recordCallback(ctx, "auth_failed", "auth")
		failService(ctx, c, err)
		return
	}
	provided := query.Token
	providedDigest := sha256.Sum256([]byte(provided))
	expectedDigest := sha256.Sum256([]byte(h.token))
	if subtle.ConstantTimeCompare(providedDigest[:], expectedDigest[:]) != 1 {
		h.recordCallback(ctx, "auth_failed", "auth")
		if occurrences, alert := h.authFailures.RecordFailure(); alert {
			httpx.AddModerationFailureAlert(c, service.ModerationAlert{
				Kind:          service.ModerationAlertKindCallbackAuthFailures,
				Provider:      model.ModerationProviderTencentCI,
				FailureCode:   apierr.BizModerationCallbackInvalid,
				Occurrences:   occurrences,
				WindowSeconds: h.authFailures.WindowSeconds(),
			})
		}
		failService(ctx, c, apierr.Forbidden(
			apierr.BizModerationCallbackInvalid, "审核回调凭证无效",
		))
		return
	}
	callback, err := h.decoder.DecodeImageCallback(c.Request.Body())
	if err != nil {
		h.recordCallback(ctx, "invalid", "payload")
		failure := apierr.BadRequest(
			apierr.BizModerationCallbackInvalid, "审核回调载荷无效",
		).WithCause(err)
		httpx.AddModerationFailureAlert(c, service.ModerationAlert{
			Kind:        service.ModerationAlertKindCallbackPayloadInvalid,
			Provider:    model.ModerationProviderTencentCI,
			FailureCode: failure.Code,
		})
		failService(ctx, c, failure)
		return
	}
	result, err := h.service.ApplyImageCallback(ctx, callback)
	if err != nil {
		reason := "target"
		if apierr.As(err).Status >= 500 {
			reason = "processing"
		}
		h.recordCallback(ctx, "invalid", reason)
		httpx.AddModerationFailureAlert(c, callbackFailureAlert(callback, err))
		failService(ctx, c, err)
		return
	}
	reason := "applied"
	if result.Duplicate {
		reason = "duplicate"
	} else if h.metrics != nil {
		h.metrics.RecordModerationTerminal(
			ctx, string(callback.Provider), string(service.ModerationTargetImage),
			moderationMetricOutcome(callback.Verdict, callback.Labels),
		)
	}
	h.recordCallback(ctx, "processed", reason)
	c.JSON(consts.StatusOK, envelope.OK("审核回调已处理", result))
}

func (h *Moderation) recordCallback(ctx context.Context, outcome, reason string) {
	if h.metrics != nil {
		h.metrics.RecordModerationCallback(ctx, "tencent_ci", outcome, reason)
	}
}

func moderationMetricOutcome(verdict model.ModerationVerdict, labels []string) string {
	for _, label := range labels {
		if label == "provider_failed" {
			return "provider_failed"
		}
	}
	return string(verdict)
}

func callbackFailureAlert(
	callback service.ImageModerationCallback,
	err error,
) service.ModerationAlert {
	failure := apierr.As(err)
	kind := service.ModerationAlertKindCallbackTargetInvalid
	if failure.Status >= 500 {
		kind = service.ModerationAlertKindCallbackProcessingFailed
	}
	jobID := callback.ProviderJobID
	return service.ModerationAlert{
		Kind:          kind,
		Target:        service.ModerationTargetImage,
		TargetID:      callback.ImageAssetID,
		Provider:      callback.Provider,
		ProviderJobID: &jobID,
		FailureCode:   failure.Code,
		ErrorID:       failure.ErrorID,
	}
}
