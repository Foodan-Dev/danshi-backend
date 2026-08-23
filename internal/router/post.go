package router

import (
	"github.com/cloudwego/hertz/pkg/route"

	"github.com/jingyijun/danshi_backend_go/internal/handler"
	"github.com/jingyijun/danshi_backend_go/internal/middleware"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

func registerPost(api *route.RouterGroup, deps Deps) {
	postService := service.NewPostService(deps.ContentModerator, deps.ModerationAlerter)
	postHandler := handler.NewPost(postService)
	authService := service.NewAuthService(deps.Config, service.UnavailableVerificationEmailSender{})
	requireAuth := middleware.RequireAuth(authService)
	posts := api.Group("/posts")

	posts.GET("", requireAuth, postHandler.List)
	posts.GET("/:post_id", requireAuth, postHandler.Get)
	posts.POST("", requireAuth, postHandler.Create)
	posts.PUT("/:post_id", requireAuth, postHandler.Update)
	posts.DELETE("/:post_id", requireAuth, postHandler.Delete)
	posts.GET("/:post_id/history", requireAuth, postHandler.Histories)
	posts.POST("/:post_id/like", requireAuth, postHandler.Like)
	posts.DELETE("/:post_id/like", requireAuth, postHandler.Unlike)
	posts.POST("/:post_id/favorite", requireAuth, postHandler.Favorite)
	posts.DELETE("/:post_id/favorite", requireAuth, postHandler.Unfavorite)
}
