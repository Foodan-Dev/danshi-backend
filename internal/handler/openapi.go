package handler

import (
	"encoding/json"
	"net/http"

	"github.com/jingyijun/danshi_backend_go/internal/apicontract"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

// OpenAPIBindings 在 handler 包内绑定私有请求 DTO，不改变它们的可见性。
func OpenAPIBindings() []apicontract.TypedRoute {
	return []apicontract.TypedRoute{
		binding(http.MethodPost, "/api/v2/auth/email-verification-codes", sendVerificationCodeRequest{}, nil),
		binding(http.MethodPost, "/api/v2/auth/register", registerRequest{}, service.AuthResult{}),
		binding(http.MethodPost, "/api/v2/auth/login", loginRequest{}, service.AuthResult{}),
		binding(http.MethodPost, "/api/v2/auth/refresh", refreshRequest{}, service.TokenResult{}),
		binding(http.MethodGet, "/api/v2/auth/me", nil, currentUserResponse{}),
		binding(http.MethodPost, "/api/v2/auth/logout", nil, nil),
		binding(http.MethodPost, "/api/v2/auth/logout-all", nil, nil),
		binding(http.MethodGet, "/api/v2/auth/sessions", nil, sessionsResponse{}),
		binding(http.MethodDelete, "/api/v2/auth/sessions/:id", nil, nil),

		binding(http.MethodGet, "/api/v2/users/:user_id", nil, service.UserProfile{}),
		binding(http.MethodPut, "/api/v2/users/:user_id", updateUserRequest{}, service.UserUpdateResult{}),
		binding(http.MethodGet, "/api/v2/users/:user_id/posts", nil, service.PostList{}),
		binding(http.MethodGet, "/api/v2/users/:user_id/favorites", nil, service.PostList{}),
		binding(http.MethodPost, "/api/v2/users/:user_id/follow", nil, service.FollowActionResult{}),
		binding(http.MethodDelete, "/api/v2/users/:user_id/follow", nil, service.FollowActionResult{}),
		binding(http.MethodGet, "/api/v2/users/:user_id/following", nil, service.UserFollowList{}),
		binding(http.MethodGet, "/api/v2/users/:user_id/followers", nil, service.UserFollowList{}),

		binding(http.MethodGet, "/api/v2/posts", nil, service.PostList{}),
		binding(http.MethodGet, "/api/v2/posts/:post_id", nil, service.PostDetail{}),
		binding(http.MethodPost, "/api/v2/posts", createPostRequest{}, service.PostCreateResult{}),
		binding(http.MethodPut, "/api/v2/posts/:post_id", updatePostRequest{}, service.PostCreateResult{}),
		binding(http.MethodDelete, "/api/v2/posts/:post_id", nil, nil),
		binding(http.MethodGet, "/api/v2/posts/:post_id/history", nil, service.PostHistoryList{}),
		binding(http.MethodPost, "/api/v2/posts/:post_id/like", nil, service.PostLikeResult{}),
		binding(http.MethodDelete, "/api/v2/posts/:post_id/like", nil, service.PostLikeResult{}),
		binding(http.MethodPost, "/api/v2/posts/:post_id/favorite", nil, service.PostFavoriteResult{}),
		binding(http.MethodDelete, "/api/v2/posts/:post_id/favorite", nil, service.PostFavoriteResult{}),

		binding(http.MethodGet, "/api/v2/posts/:post_id/comments", nil, service.CommentList{}),
		binding(http.MethodPost, "/api/v2/posts/:post_id/comments", createCommentRequest{}, service.CommentMutationResult{}),
		binding(http.MethodGet, "/api/v2/comments/:comment_id/replies", nil, service.CommentReplies{}),
		binding(http.MethodPut, "/api/v2/comments/:comment_id", updateCommentRequest{}, service.CommentMutationResult{}),
		binding(http.MethodGet, "/api/v2/comments/:comment_id/history", nil, service.CommentHistoryList{}),
		binding(http.MethodPost, "/api/v2/comments/:comment_id/like", nil, service.CommentLikeResult{}),
		binding(http.MethodDelete, "/api/v2/comments/:comment_id/like", nil, service.CommentLikeResult{}),
		binding(http.MethodDelete, "/api/v2/comments/:comment_id", nil, nil),

		binding(http.MethodGet, "/api/v2/notifications", nil, service.NotificationList{}),
		binding(http.MethodGet, "/api/v2/notifications/unread-count", nil, service.NotificationStats{}),
		binding(http.MethodPut, "/api/v2/notifications/read-all", nil, service.NotificationMarked{}),
		binding(http.MethodPut, "/api/v2/notifications/:notification_id/read", nil, nil),

		binding(http.MethodGet, "/api/v2/search/posts", nil, service.SearchPostList{}),
		binding(http.MethodGet, "/api/v2/search/users", nil, service.SearchUserList{}),
		binding(http.MethodPost, "/api/v2/uploads/presign", uploadPresignRequest{}, service.UploadPresignResult{}),
		binding(http.MethodPost, "/api/v2/uploads/:upload_id/complete", nil, service.UploadCompleteResult{}),
		binding(http.MethodGet, "/api/v2/config", nil, service.ExploreConfig{}),

		binding(http.MethodPost, "/api/v2/dictionary-suggestions", createSuggestionRequest{}, service.SuggestionView{}),
		binding(http.MethodGet, "/api/v2/dictionary-suggestions/mine", nil, service.SuggestionList{}),
		binding(http.MethodGet, "/api/v2/admin/dictionary-suggestions", nil, service.SuggestionList{}),
		binding(http.MethodPost, "/api/v2/admin/dictionary-suggestions/:suggestion_id/approve", approveSuggestionRequest{}, service.SuggestionView{}),
		binding(http.MethodPost, "/api/v2/admin/dictionary-suggestions/:suggestion_id/reject", rejectSuggestionRequest{}, service.SuggestionView{}),
		binding(http.MethodPost, "/api/v2/admin/flavors", createDictionaryItemRequest{}, service.DictionaryItemView{}),
		binding(http.MethodPatch, "/api/v2/admin/flavors/:flavor_id", updateDictionaryItemRequest{}, service.DictionaryItemView{}),
		binding(http.MethodDelete, "/api/v2/admin/flavors/:flavor_id", nil, service.DictionaryDeleteResult{}),
		binding(http.MethodPost, "/api/v2/admin/cuisines", createDictionaryItemRequest{}, service.DictionaryItemView{}),
		binding(http.MethodPatch, "/api/v2/admin/cuisines/:cuisine_id", updateDictionaryItemRequest{}, service.DictionaryItemView{}),
		binding(http.MethodDelete, "/api/v2/admin/cuisines/:cuisine_id", nil, service.DictionaryDeleteResult{}),
		binding(http.MethodPost, "/api/v2/admin/canteens", createCanteenRequest{}, service.DictionaryCanteenView{}),
		binding(http.MethodPatch, "/api/v2/admin/canteens/:canteen_id", updateCanteenRequest{}, service.DictionaryCanteenView{}),
		binding(http.MethodDelete, "/api/v2/admin/canteens/:canteen_id", nil, service.DictionaryDeleteResult{}),
		binding(http.MethodPost, "/api/v2/admin/canteens/:canteen_id/windows", createWindowRequest{}, service.DictionaryWindowView{}),
		binding(http.MethodPatch, "/api/v2/admin/canteen-windows/:window_id", updateWindowRequest{}, service.DictionaryWindowView{}),
		binding(http.MethodDelete, "/api/v2/admin/canteen-windows/:window_id", nil, service.DictionaryDeleteResult{}),

		binding(http.MethodPost, "/api/v2/moderation/tencent-ci/callback", json.RawMessage{}, service.ImageModerationApplyResult{}),

		binding(http.MethodGet, "/api/v2/admin/posts/pending", nil, service.AdminPostList{}),
		binding(http.MethodPut, "/api/v2/admin/posts/:post_id/review", adminPostReviewRequest{}, service.AdminPostReviewResult{}),
		binding(http.MethodGet, "/api/v2/admin/posts", nil, service.AdminPostList{}),
		binding(http.MethodDelete, "/api/v2/admin/posts/:post_id", nil, service.AdminPostDeleteResult{}),
		binding(http.MethodPut, "/api/v2/admin/posts/:post_id/restore", nil, service.AdminPostRestoreResult{}),
		binding(http.MethodGet, "/api/v2/admin/users", nil, service.AdminUserList{}),
		binding(http.MethodPut, "/api/v2/admin/users/:user_id/status", adminUserStatusRequest{}, service.AdminUserStatusResult{}),
		binding(http.MethodPut, "/api/v2/admin/users/:user_id/role", adminUserRoleRequest{}, service.AdminUserRoleResult{}),
		binding(http.MethodGet, "/api/v2/admin/admins", nil, service.AdminUserList{}),
		binding(http.MethodGet, "/api/v2/admin/super-admins", nil, service.AdminUserList{}),
		binding(http.MethodGet, "/api/v2/admin/comments", nil, service.AdminCommentList{}),
		binding(http.MethodDelete, "/api/v2/admin/comments/:comment_id", nil, service.AdminCommentDeleteResult{}),
		binding(http.MethodPut, "/api/v2/admin/comments/:comment_id/restore", nil, service.AdminCommentRestoreResult{}),
		binding(http.MethodGet, "/api/v2/admin/moderation-records/pending", nil, service.AdminModerationList{}),
		binding(http.MethodPut, "/api/v2/admin/moderation-records/:moderation_record_id/review", adminManualReviewRequest{}, service.AdminReviewResult{}),
	}
}

func binding(method, path string, request, response any) apicontract.TypedRoute {
	return apicontract.TypedRoute{
		Method: method, Path: path,
		TypeBinding: apicontract.TypeBinding{Request: request, Response: response},
	}
}
