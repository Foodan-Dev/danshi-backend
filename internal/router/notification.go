package router

import (
	"github.com/cloudwego/hertz/pkg/route"

	"github.com/Foodan-Dev/danshi-backend/internal/handler"
	"github.com/Foodan-Dev/danshi-backend/internal/middleware"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

func registerNotification(api *route.RouterGroup, deps Deps) {
	notificationHandler := handler.NewNotification(
		service.NewNotificationServiceWithCursorSecret(deps.Config.JWTSecretKey),
	)
	authService := service.NewAuthService(deps.Config, service.UnavailableVerificationEmailSender{})
	requireAuth := middleware.RequireAuth(authService)
	notifications := api.Group("/notifications")

	notifications.GET("", requireAuth, notificationHandler.List)
	notifications.GET("/unread-count", requireAuth, notificationHandler.UnreadCount)
	// 静态段必须先注册，避免被 :notification_id 路由吞掉。
	notifications.PUT("/read-all", requireAuth, notificationHandler.MarkAllRead)
	notifications.PUT("/:notification_id/read", requireAuth, notificationHandler.MarkRead)
}
