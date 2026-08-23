package router

import (
	"github.com/cloudwego/hertz/pkg/route"

	"github.com/Foodan-Dev/danshi-backend/internal/handler"
	"github.com/Foodan-Dev/danshi-backend/internal/middleware"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

func registerComment(api *route.RouterGroup, deps Deps) {
	commentHandler := handler.NewComment(service.NewCommentServiceWithCursorSecret(
		deps.ContentModerator, deps.Config.JWTSecretKey, deps.ModerationAlerter,
	))
	authService := service.NewAuthService(deps.Config, service.UnavailableVerificationEmailSender{})
	requireAuth := middleware.RequireAuth(authService)

	posts := api.Group("/posts")
	posts.GET("/:post_id/comments", requireAuth, commentHandler.List)
	posts.POST("/:post_id/comments", requireAuth, commentHandler.Create)

	comments := api.Group("/comments")
	comments.GET("/:comment_id/replies", requireAuth, commentHandler.Replies)
	comments.PUT("/:comment_id", requireAuth, commentHandler.Update)
	comments.GET("/:comment_id/history", requireAuth, commentHandler.Histories)
	comments.POST("/:comment_id/like", requireAuth, commentHandler.Like)
	comments.DELETE("/:comment_id/like", requireAuth, commentHandler.Unlike)
	comments.DELETE("/:comment_id", requireAuth, commentHandler.Delete)
}
