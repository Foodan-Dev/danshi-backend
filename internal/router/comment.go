package router

import (
	"github.com/cloudwego/hertz/pkg/route"

	"github.com/jingyijun/danshi_backend_go/internal/handler"
	"github.com/jingyijun/danshi_backend_go/internal/router/middleware"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

func registerComment(api *route.RouterGroup, deps Deps) {
	moderator := deps.ContentModerator
	if moderator == nil {
		if deps.Config.IsProd() {
			moderator = service.UnavailableContentModerator{}
		} else {
			moderator = service.DirectPassContentModerator{}
		}
	}
	commentHandler := handler.NewComment(service.NewCommentService(moderator))
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
