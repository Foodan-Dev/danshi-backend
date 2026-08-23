package router

import (
	"github.com/cloudwego/hertz/pkg/route"

	"github.com/jingyijun/danshi_backend_go/internal/authz"
	"github.com/jingyijun/danshi_backend_go/internal/handler"
	"github.com/jingyijun/danshi_backend_go/internal/router/middleware"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

func registerAdmin(api *route.RouterGroup, deps Deps) {
	adminHandler := handler.NewAdmin(service.NewAdminService(deps.ModerationAlerter))
	authService := service.NewAuthService(deps.Config, service.UnavailableVerificationEmailSender{})
	requireAuth := middleware.RequireAuth(authService)
	admin := api.Group("/admin")

	requireReviewContent := middleware.RequireCapability(authz.CapReviewContent)
	requireManageContent := middleware.RequireCapability(authz.CapManageContent)
	requireViewUserEvidence := middleware.RequireCapability(authz.CapViewUserEvidence)
	requireBanUser := middleware.RequireCapability(authz.CapBanUser)
	requireListUsers := middleware.RequireCapability(authz.CapListUsers)
	requireManageUserRoles := middleware.RequireCapability(authz.CapManageUserRoles)
	requireListAdmins := middleware.RequireCapability(authz.CapListAdmins)

	admin.GET("/posts/pending", requireAuth, requireReviewContent, adminHandler.PendingPosts)
	admin.PUT("/posts/:post_id/review", requireAuth, requireReviewContent, adminHandler.ReviewPost)
	admin.GET("/posts", requireAuth, requireManageContent, adminHandler.Posts)
	admin.DELETE("/posts/:post_id", requireAuth, requireManageContent, adminHandler.DeletePost)
	admin.PUT("/posts/:post_id/restore", requireAuth, requireManageContent, adminHandler.RestorePost)

	admin.GET("/users", requireAuth, requireListUsers, adminHandler.Users)
	admin.GET("/users/:user_id", requireAuth, requireViewUserEvidence, adminHandler.User)
	admin.GET("/users/:user_id/posts", requireAuth, requireViewUserEvidence, adminHandler.UserPosts)
	admin.PUT("/users/:user_id/status", requireAuth, requireBanUser, adminHandler.UpdateUserStatus)
	admin.PUT("/users/:user_id/role", requireAuth, requireManageUserRoles, adminHandler.UpdateUserRole)
	admin.GET("/admins", requireAuth, requireListAdmins, adminHandler.Admins)
	admin.GET("/super-admins", requireAuth, requireListAdmins, adminHandler.SuperAdmins)

	admin.GET("/comments", requireAuth, requireManageContent, adminHandler.Comments)
	admin.DELETE("/comments/:comment_id", requireAuth, requireManageContent, adminHandler.DeleteComment)
	admin.PUT("/comments/:comment_id/restore", requireAuth, requireManageContent, adminHandler.RestoreComment)

	admin.GET(
		"/moderation-records/pending", requireAuth, requireReviewContent, adminHandler.PendingModeration,
	)
	admin.PUT(
		"/moderation-records/:moderation_record_id/review",
		requireAuth,
		requireReviewContent,
		adminHandler.ManualReview,
	)
}
