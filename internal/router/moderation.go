package router

import (
	"github.com/cloudwego/hertz/pkg/route"

	"github.com/Foodan-Dev/danshi-backend/internal/handler"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

func registerModeration(api *route.RouterGroup, deps Deps) {
	moderationService := newModerationService(deps)
	moderationHandler := handler.NewModeration(
		moderationService, deps.ImageCallbackDecoder, deps.Config.ModerationCallbackToken,
		service.NewCallbackAuthFailureMonitor(
			deps.Config.ModerationCallbackAuthFailureThreshold,
			deps.Config.ModerationCallbackAuthFailureWindow(),
		),
		deps.BusinessMetrics,
	)
	moderation := api.Group("/moderation")

	moderation.POST("/tencent-ci/callback", moderationHandler.TencentCICallback)
}
