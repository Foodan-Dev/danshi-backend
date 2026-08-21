package router

import (
	"github.com/cloudwego/hertz/pkg/route"

	"github.com/jingyijun/danshi_backend_go/internal/handler"
	"github.com/jingyijun/danshi_backend_go/internal/router/middleware"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

func registerAuth(api *route.RouterGroup, deps Deps) {
	sender := deps.EmailSender
	if sender == nil {
		if deps.Config.IsProd() {
			sender = service.UnavailableVerificationEmailSender{}
		} else {
			sender = service.NewLogVerificationEmailSender(deps.Log)
		}
	}
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
