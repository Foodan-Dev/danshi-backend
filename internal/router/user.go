package router

import (
	"github.com/cloudwego/hertz/pkg/route"

	"github.com/jingyijun/danshi_backend_go/internal/handler"
	"github.com/jingyijun/danshi_backend_go/internal/router/middleware"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

func registerUser(api *route.RouterGroup, deps Deps) {
	moderator := deps.ContentModerator
	if moderator == nil {
		if deps.Config.IsProd() {
			moderator = service.UnavailableContentModerator{}
		} else {
			moderator = service.DirectPassContentModerator{}
		}
	}
	alerter := deps.UserModerationAlerter
	if alerter == nil {
		alerter = service.DiscardUserModerationAlerter{}
	}
	userHandler := handler.NewUser(service.NewUserService(moderator, alerter))
	authService := service.NewAuthService(deps.Config, service.UnavailableVerificationEmailSender{})
	requireAuth := middleware.RequireAuth(authService)
	users := api.Group("/users")

	users.GET("/:user_id", requireAuth, userHandler.Profile)
	users.PUT("/:user_id", requireAuth, userHandler.Update)
	users.GET("/:user_id/posts", requireAuth, userHandler.Posts)
	users.GET("/:user_id/favorites", requireAuth, userHandler.Favorites)
	users.POST("/:user_id/follow", requireAuth, userHandler.Follow)
	users.DELETE("/:user_id/follow", requireAuth, userHandler.Unfollow)
	users.GET("/:user_id/following", requireAuth, userHandler.Following)
	users.GET("/:user_id/followers", requireAuth, userHandler.Followers)
}
