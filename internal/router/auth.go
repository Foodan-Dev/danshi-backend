package router

import (
	"github.com/cloudwego/hertz/pkg/route"

	"github.com/jingyijun/danshi_backend_go/internal/handler"
	"github.com/jingyijun/danshi_backend_go/internal/infra/tencentcloud"
	"github.com/jingyijun/danshi_backend_go/internal/middleware"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

func registerAuth(api *route.RouterGroup, deps Deps) {
	sender := verificationEmailSender(deps)
	authService := service.NewAuthService(deps.Config, sender)
	authHandler := handler.NewAuth(authService)
	auth := api.Group("/auth")

	auth.POST("/email-verification-codes", authHandler.SendVerificationCode)
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

func verificationEmailSender(deps Deps) service.VerificationEmailSender {
	if deps.EmailSender != nil {
		return deps.EmailSender
	}
	if !deps.Config.IsProd() {
		return service.NewLogVerificationEmailSender(deps.Log)
	}
	if !deps.Config.TencentSESConfigured() {
		return service.UnavailableVerificationEmailSender{}
	}
	sender, err := tencentcloud.NewSESVerificationEmailSender(deps.Config)
	if err != nil {
		if deps.Log != nil {
			deps.Log.Error("腾讯云 SES 适配器初始化失败", "err", err)
		}
		return service.UnavailableVerificationEmailSender{}
	}
	return sender
}
