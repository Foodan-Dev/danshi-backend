package router

import (
	"github.com/cloudwego/hertz/pkg/route"

	"github.com/Foodan-Dev/danshi-backend/internal/authz"
	"github.com/Foodan-Dev/danshi-backend/internal/handler"
	"github.com/Foodan-Dev/danshi-backend/internal/middleware"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

func registerAdmin(api *route.RouterGroup, deps Deps) {
	adminService := service.NewAdminService(
		deps.ModerationAlerter,
		afterCommitImageAccessController{
			storage: deps.ImageStorage, purger: deps.ImageCachePurger, log: deps.Log,
		},
		deps.ImageStorage,
		signedImageTTL(deps),
	).WithTagCursorSecret(deps.Config.JWTSecretKey)
	adminHandler := handler.NewAdmin(adminService)
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
	admin.GET("/images/:image_asset_id", requireAuth, requireReviewContent, adminHandler.Image)

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

	admin.GET("/tags", requireAuth, requireManageContent, adminHandler.Tags)
	admin.GET("/tags/hot", requireAuth, requireManageContent, adminHandler.HotTags)
	admin.PATCH("/tags/:tag_id", requireAuth, requireManageContent, adminHandler.RenameTag)
	admin.POST("/tags/:tag_id/merge", requireAuth, requireManageContent, adminHandler.MergeTag)
	admin.DELETE("/tags/:tag_id", requireAuth, requireReviewContent, adminHandler.DeleteTag)
	admin.POST("/tags/:tag_id/restore", requireAuth, requireReviewContent, adminHandler.RestoreTag)

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
