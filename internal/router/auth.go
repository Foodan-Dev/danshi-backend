package router

import (
	"github.com/cloudwego/hertz/pkg/route"

	"github.com/Foodan-Dev/danshi-backend/internal/handler"
	"github.com/Foodan-Dev/danshi-backend/internal/infra/tencentcloud"
	"github.com/Foodan-Dev/danshi-backend/internal/middleware"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

func registerAuth(api *route.RouterGroup, deps Deps) {
	sender := VerificationEmailSender(deps)
	delivery := deps.EmailDeliveryWorker
	if delivery == nil {
		delivery = service.NewVerificationEmailDeliveryWorker(
			deps.DB, sender, deps.Config.EmailVerificationSecret,
			service.VerificationEmailDeliveryWorkerOptions{Log: deps.Log},
		)
	}
	authService := service.NewAuthService(deps.Config, sender, delivery, deps.ContentModerator)
	authHandler := handler.NewAuth(authService, deps.BusinessMetrics)
	auth := api.Group("/auth")

	auth.POST("/email-verification-codes", authHandler.SendVerificationCode)
	auth.POST("/password-reset-codes", authHandler.SendPasswordResetCode)
	auth.POST("/password-resets", authHandler.ResetPassword)
	auth.POST("/register", authHandler.Register)
	auth.POST("/login", authHandler.Login)
	auth.POST("/refresh", authHandler.Refresh)

	requireAuth := middleware.RequireAuth(authService)
	auth.GET("/me", requireAuth, authHandler.Me)
	auth.POST("/logout", requireAuth, authHandler.Logout)
	auth.POST("/logout-all", requireAuth, authHandler.LogoutAll)
	auth.GET("/sessions", requireAuth, authHandler.Sessions)
	auth.DELETE("/sessions/:id", requireAuth, authHandler.KickSession)
}

// VerificationEmailSender 装配路由与后台任务共用的验证码投递器。
func VerificationEmailSender(deps Deps) service.VerificationEmailSender {
	if deps.EmailSender != nil {
		return observeVerificationSender(deps.EmailSender, "unknown", deps.BusinessMetrics)
	}
	if !deps.Config.TencentSESConfigured() {
		if !deps.Config.IsProd() {
			return observeVerificationSender(
				service.NewLogVerificationEmailSender(deps.Log), "log", deps.BusinessMetrics,
			)
		}
		return observeVerificationSender(
			service.UnavailableVerificationEmailSender{}, "none", deps.BusinessMetrics,
		)
	}
	sender, err := tencentcloud.NewSESVerificationEmailSender(deps.Config)
	if err != nil {
		if deps.Log != nil {
			deps.Log.Error("腾讯云 SES 适配器初始化失败", "err", err)
		}
		return observeVerificationSender(
			service.UnavailableVerificationEmailSender{}, "none", deps.BusinessMetrics,
		)
	}
	return observeVerificationSender(sender, "tencent_ses", deps.BusinessMetrics)
}

// verificationEmailSender 保留包内测试与旧调用点的兼容别名。
func verificationEmailSender(deps Deps) service.VerificationEmailSender {
	return VerificationEmailSender(deps)
}
