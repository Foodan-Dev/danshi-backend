package router

import (
	"github.com/cloudwego/hertz/pkg/route"

	"github.com/Foodan-Dev/danshi-backend/internal/handler"
	"github.com/Foodan-Dev/danshi-backend/internal/middleware"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

func registerUpload(api *route.RouterGroup, deps Deps) {
	maxImageBytes, presignTTL := uploadLimits(deps)
	moderationService := newModerationService(deps)
	uploadService := service.NewUploadService(
		deps.ImageStorage, deps.ImageModerator, moderationService, maxImageBytes, presignTTL,
	)
	uploadHandler := handler.NewUpload(uploadService)
	authService := service.NewAuthService(deps.Config, service.UnavailableVerificationEmailSender{})
	requireAuth := middleware.RequireAuth(authService)
	uploads := api.Group("/uploads")

	uploads.POST("/presign", requireAuth, uploadHandler.Presign)
	uploads.POST("/:upload_id/complete", requireAuth, uploadHandler.Complete)
}
