package router

import (
	"github.com/cloudwego/hertz/pkg/route"

	"github.com/jingyijun/danshi_backend_go/internal/handler"
)

func registerModeration(api *route.RouterGroup, deps Deps) {
	moderationService := newModerationService(deps)
	moderationHandler := handler.NewModeration(
		moderationService, deps.ImageCallbackDecoder, deps.Config.ModerationCallbackToken,
	)
	moderation := api.Group("/moderation")

	moderation.POST("/tencent-ci/callback", moderationHandler.TencentCICallback)
}
