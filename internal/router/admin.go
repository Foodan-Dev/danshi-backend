package router

import (
	"github.com/cloudwego/hertz/pkg/route"

	"github.com/jingyijun/danshi_backend_go/internal/handler"
	"github.com/jingyijun/danshi_backend_go/internal/router/middleware"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

func registerAdmin(api *route.RouterGroup, deps Deps) {
	adminHandler := handler.NewAdmin(service.NewAdminService(deps.ModerationAlerter))
	authService := service.NewAuthService(deps.Config, service.UnavailableVerificationEmailSender{})
	requireAuth := middleware.RequireAuth(authService)
	requireAdmin := middleware.RequireAdmin()
	requireSuperAdmin := middleware.RequireSuperAdmin()
	admin := api.Group("/admin")

	admin.GET("/posts/pending", requireAuth, requireAdmin, adminHandler.PendingPosts)
	admin.PUT("/posts/:post_id/review", requireAuth, requireAdmin, adminHandler.ReviewPost)
	admin.GET("/posts", requireAuth, requireAdmin, adminHandler.Posts)
	admin.DELETE("/posts/:post_id", requireAuth, requireAdmin, adminHandler.DeletePost)
	admin.PUT("/posts/:post_id/restore", requireAuth, requireAdmin, adminHandler.RestorePost)

	admin.GET("/users", requireAuth, requireSuperAdmin, adminHandler.Users)
	admin.PUT("/users/:user_id/status", requireAuth, requireSuperAdmin, adminHandler.UpdateUserStatus)
	admin.PUT("/users/:user_id/role", requireAuth, requireSuperAdmin, adminHandler.UpdateUserRole)
	admin.GET("/admins", requireAuth, requireSuperAdmin, adminHandler.Admins)
	admin.GET("/super-admins", requireAuth, requireSuperAdmin, adminHandler.SuperAdmins)

	admin.GET("/comments", requireAuth, requireAdmin, adminHandler.Comments)
	admin.DELETE("/comments/:comment_id", requireAuth, requireAdmin, adminHandler.DeleteComment)
	admin.PUT("/comments/:comment_id/restore", requireAuth, requireAdmin, adminHandler.RestoreComment)

	admin.GET(
		"/moderation-records/pending", requireAuth, requireAdmin, adminHandler.PendingModeration,
	)
	admin.PUT(
		"/moderation-records/:moderation_record_id/review",
		requireAuth,
		requireAdmin,
		adminHandler.ManualReview,
	)
}
