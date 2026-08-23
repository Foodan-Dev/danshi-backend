package handler

import (
	"encoding/json"
	"net/http"

	"github.com/jingyijun/danshi_backend_go/internal/apicontract"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

// OpenAPIBindings 在 handler 包内绑定私有请求 DTO，不改变它们的可见性。
func OpenAPIBindings() []apicontract.TypedRoute {
	bindings := authAndUserOpenAPIBindings()
	bindings = append(bindings, postAndCommentOpenAPIBindings()...)
	bindings = append(bindings, utilityOpenAPIBindings()...)
	bindings = append(bindings, dictionaryOpenAPIBindings()...)
	bindings = append(bindings, moderationAndAdminOpenAPIBindings()...)
	return bindings
}

func authAndUserOpenAPIBindings() []apicontract.TypedRoute {
	return []apicontract.TypedRoute{
		binding(http.MethodPost, "/api/v2/auth/email-verification-codes", sendVerificationCodeRequest{}, nil,
			http.StatusTooManyRequests, http.StatusServiceUnavailable),
		binding(http.MethodPost, "/api/v2/auth/register", registerRequest{}, service.AuthResult{},
			http.StatusBadRequest, http.StatusConflict),
		binding(http.MethodPost, "/api/v2/auth/login", loginRequest{}, service.AuthResult{},
			http.StatusUnauthorized, http.StatusForbidden),
		binding(http.MethodPost, "/api/v2/auth/refresh", refreshRequest{}, service.TokenResult{},
			http.StatusUnauthorized),
		getBinding("/api/v2/auth/me", apicontract.NoQuery{}, currentUserResponse{}),
		binding(http.MethodPost, "/api/v2/auth/logout", nil, nil),
		binding(http.MethodPost, "/api/v2/auth/logout-all", nil, nil),
		getBinding("/api/v2/auth/sessions", apicontract.NoQuery{}, sessionsResponse{}),
		binding(http.MethodDelete, "/api/v2/auth/sessions/:id", nil, nil),

		getBinding("/api/v2/users/:user_id", apicontract.NoQuery{}, service.UserProfile{}),
		binding(http.MethodPut, "/api/v2/users/:user_id", updateUserRequest{}, service.UserUpdateResult{},
			http.StatusBadRequest, http.StatusForbidden, http.StatusConflict, http.StatusServiceUnavailable),
		getBinding("/api/v2/users/:user_id/posts", userPostsQuery{}, service.PostList{},
			http.StatusForbidden),
		getBinding("/api/v2/users/:user_id/favorites", paginationQuery{}, service.PostList{},
			http.StatusForbidden),
		binding(http.MethodPost, "/api/v2/users/:user_id/follow", nil, service.FollowActionResult{},
			http.StatusBadRequest),
		binding(http.MethodDelete, "/api/v2/users/:user_id/follow", nil, service.FollowActionResult{},
			http.StatusBadRequest),
		getBinding("/api/v2/users/:user_id/following", paginationQuery{}, service.UserFollowList{}),
		getBinding("/api/v2/users/:user_id/followers", paginationQuery{}, service.UserFollowList{}),
	}
}

func postAndCommentOpenAPIBindings() []apicontract.TypedRoute {
	return []apicontract.TypedRoute{
		getBinding("/api/v2/posts", listPostsQuery{}, service.PostFeedList{}, http.StatusUnprocessableEntity),
		getBinding("/api/v2/posts/:post_id", apicontract.NoQuery{}, service.PostDetail{}),
		binding(http.MethodPost, "/api/v2/posts", createPostRequest{}, service.PostCreateResult{},
			http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound,
			http.StatusConflict, http.StatusServiceUnavailable),
		binding(http.MethodPut, "/api/v2/posts/:post_id", updatePostRequest{}, service.PostCreateResult{},
			http.StatusBadRequest, http.StatusForbidden, http.StatusConflict,
			http.StatusServiceUnavailable),
		binding(http.MethodDelete, "/api/v2/posts/:post_id", nil, nil, http.StatusForbidden),
		getBinding("/api/v2/posts/:post_id/history", apicontract.NoQuery{}, service.PostHistoryList{},
			http.StatusForbidden),
		binding(http.MethodPost, "/api/v2/posts/:post_id/like", nil, service.PostLikeResult{},
			http.StatusConflict),
		binding(http.MethodDelete, "/api/v2/posts/:post_id/like", nil, service.PostLikeResult{},
			http.StatusConflict),
		binding(http.MethodPost, "/api/v2/posts/:post_id/favorite", nil, service.PostFavoriteResult{},
			http.StatusConflict),
		binding(http.MethodDelete, "/api/v2/posts/:post_id/favorite", nil, service.PostFavoriteResult{},
			http.StatusConflict),

		getBinding("/api/v2/posts/:post_id/comments", commentListQuery{}, service.CommentList{}),
		binding(http.MethodPost, "/api/v2/posts/:post_id/comments", createCommentRequest{}, service.CommentMutationResult{},
			http.StatusConflict, http.StatusServiceUnavailable),
		getBinding("/api/v2/comments/:comment_id/replies", replyPaginationQuery{}, service.CommentReplies{}),
		binding(http.MethodPut, "/api/v2/comments/:comment_id", updateCommentRequest{}, service.CommentMutationResult{},
			http.StatusForbidden, http.StatusConflict, http.StatusServiceUnavailable),
		getBinding("/api/v2/comments/:comment_id/history", apicontract.NoQuery{}, service.CommentHistoryList{},
			http.StatusForbidden),
		binding(http.MethodPost, "/api/v2/comments/:comment_id/like", nil, service.CommentLikeResult{},
			http.StatusConflict),
		binding(http.MethodDelete, "/api/v2/comments/:comment_id/like", nil, service.CommentLikeResult{},
			http.StatusConflict),
		binding(http.MethodDelete, "/api/v2/comments/:comment_id", nil, nil, http.StatusForbidden),
	}
}

func utilityOpenAPIBindings() []apicontract.TypedRoute {
	return []apicontract.TypedRoute{
		getBinding("/api/v2/notifications", notificationListQuery{}, service.NotificationList{},
			http.StatusUnprocessableEntity),
		getBinding("/api/v2/notifications/unread-count", apicontract.NoQuery{}, service.NotificationStats{}),
		binding(http.MethodPut, "/api/v2/notifications/read-all", nil, service.NotificationMarked{}),
		binding(http.MethodPut, "/api/v2/notifications/:notification_id/read", nil, nil),

		getBinding("/api/v2/search/posts", searchPostsQuery{}, service.SearchPostList{},
			http.StatusUnprocessableEntity),
		getBinding("/api/v2/search/users", searchUsersQuery{}, service.SearchUserList{},
			http.StatusUnprocessableEntity),
		binding(http.MethodPost, "/api/v2/uploads/presign", uploadPresignRequest{}, service.UploadPresignResult{},
			http.StatusServiceUnavailable),
		binding(http.MethodPost, "/api/v2/uploads/:upload_id/complete", nil, service.UploadCompleteResult{},
			http.StatusForbidden, http.StatusConflict, http.StatusServiceUnavailable),
		getBinding("/api/v2/config", apicontract.NoQuery{}, service.ExploreConfig{}),
	}
}

func dictionaryOpenAPIBindings() []apicontract.TypedRoute {
	return []apicontract.TypedRoute{
		binding(http.MethodPost, "/api/v2/dictionary-suggestions", createSuggestionRequest{}, service.SuggestionView{},
			http.StatusForbidden, http.StatusNotFound, http.StatusConflict),
		getBinding("/api/v2/dictionary-suggestions/mine", paginationQuery{}, service.SuggestionList{},
			http.StatusUnprocessableEntity),
		getBinding("/api/v2/admin/dictionary-suggestions", dictionaryPendingQuery{}, service.SuggestionList{},
			http.StatusUnprocessableEntity),
		binding(http.MethodPost, "/api/v2/admin/dictionary-suggestions/:suggestion_id/approve", approveSuggestionRequest{}, service.SuggestionView{},
			http.StatusConflict),
		binding(http.MethodPost, "/api/v2/admin/dictionary-suggestions/:suggestion_id/reject", rejectSuggestionRequest{}, service.SuggestionView{},
			http.StatusConflict),
		binding(http.MethodPost, "/api/v2/admin/flavors", createDictionaryItemRequest{}, service.DictionaryItemView{},
			http.StatusConflict),
		binding(http.MethodPatch, "/api/v2/admin/flavors/:flavor_id", updateDictionaryItemRequest{}, service.DictionaryItemView{},
			http.StatusConflict),
		binding(http.MethodDelete, "/api/v2/admin/flavors/:flavor_id", nil, service.DictionaryDeleteResult{},
			http.StatusConflict),
		binding(http.MethodPost, "/api/v2/admin/cuisines", createDictionaryItemRequest{}, service.DictionaryItemView{},
			http.StatusConflict),
		binding(http.MethodPatch, "/api/v2/admin/cuisines/:cuisine_id", updateDictionaryItemRequest{}, service.DictionaryItemView{},
			http.StatusConflict),
		binding(http.MethodDelete, "/api/v2/admin/cuisines/:cuisine_id", nil, service.DictionaryDeleteResult{},
			http.StatusConflict),
		binding(http.MethodPost, "/api/v2/admin/canteens", createCanteenRequest{}, service.DictionaryCanteenView{},
			http.StatusConflict),
		binding(http.MethodPatch, "/api/v2/admin/canteens/:canteen_id", updateCanteenRequest{}, service.DictionaryCanteenView{},
			http.StatusConflict),
		binding(http.MethodDelete, "/api/v2/admin/canteens/:canteen_id", nil, service.DictionaryDeleteResult{},
			http.StatusConflict),
		binding(http.MethodPost, "/api/v2/admin/canteens/:canteen_id/windows", createWindowRequest{}, service.DictionaryWindowView{},
			http.StatusConflict),
		binding(http.MethodPatch, "/api/v2/admin/canteen-windows/:window_id", updateWindowRequest{}, service.DictionaryWindowView{},
			http.StatusConflict),
		binding(http.MethodDelete, "/api/v2/admin/canteen-windows/:window_id", nil, service.DictionaryDeleteResult{},
			http.StatusConflict),
	}
}

func moderationAndAdminOpenAPIBindings() []apicontract.TypedRoute {
	return []apicontract.TypedRoute{
		queryBinding(http.MethodPost, "/api/v2/moderation/tencent-ci/callback", moderationCallbackQuery{},
			json.RawMessage{}, service.ImageModerationApplyResult{},
			http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound,
			http.StatusConflict, http.StatusServiceUnavailable),

		getBinding("/api/v2/admin/posts/pending", paginationQuery{}, service.AdminPostList{},
			http.StatusUnprocessableEntity),
		binding(http.MethodPut, "/api/v2/admin/posts/:post_id/review", adminPostReviewRequest{}, service.AdminPostReviewResult{},
			http.StatusConflict),
		getBinding("/api/v2/admin/posts", adminPostsQuery{}, service.AdminPostList{},
			http.StatusUnprocessableEntity),
		binding(http.MethodDelete, "/api/v2/admin/posts/:post_id", nil, service.AdminPostDeleteResult{}),
		binding(http.MethodPut, "/api/v2/admin/posts/:post_id/restore", nil, service.AdminPostRestoreResult{},
			http.StatusConflict),
		getBinding("/api/v2/admin/users", adminUsersQuery{}, service.AdminUserList{},
			http.StatusUnprocessableEntity),
		getBinding("/api/v2/admin/users/:user_id", apicontract.NoQuery{}, service.AdminUserDetail{}),
		getBinding("/api/v2/admin/users/:user_id/posts", adminPostsQuery{}, service.AdminPostList{}),
		binding(http.MethodPut, "/api/v2/admin/users/:user_id/status", adminUserStatusRequest{}, service.AdminUserStatusResult{}),
		binding(http.MethodPut, "/api/v2/admin/users/:user_id/role", adminUserRoleRequest{}, service.AdminUserRoleResult{}),
		getBinding("/api/v2/admin/admins", paginationQuery{}, service.AdminUserList{},
			http.StatusUnprocessableEntity),
		getBinding("/api/v2/admin/super-admins", paginationQuery{}, service.AdminUserList{},
			http.StatusUnprocessableEntity),
		getBinding("/api/v2/admin/comments", adminCommentsQuery{}, service.AdminCommentList{},
			http.StatusUnprocessableEntity),
		binding(http.MethodDelete, "/api/v2/admin/comments/:comment_id", nil, service.AdminCommentDeleteResult{}),
		binding(http.MethodPut, "/api/v2/admin/comments/:comment_id/restore", nil, service.AdminCommentRestoreResult{},
			http.StatusConflict),
		getBinding("/api/v2/admin/moderation-records/pending", paginationQuery{}, service.AdminModerationList{},
			http.StatusUnprocessableEntity),
		binding(http.MethodPut, "/api/v2/admin/moderation-records/:moderation_record_id/review", adminManualReviewRequest{}, service.AdminReviewResult{},
			http.StatusConflict),
	}
}

func binding(method, path string, request, response any, additionalErrors ...int) apicontract.TypedRoute {
	return queryBinding(method, path, nil, request, response, additionalErrors...)
}

func getBinding(path string, query, response any, additionalErrors ...int) apicontract.TypedRoute {
	return queryBinding(http.MethodGet, path, query, nil, response, additionalErrors...)
}

func queryBinding(
	method, path string,
	query, request, response any,
	additionalErrors ...int,
) apicontract.TypedRoute {
	return apicontract.TypedRoute{
		Method: method, Path: path,
		TypeBinding: apicontract.TypeBinding{
			Query: query, Request: request, Response: response,
			AdditionalErrorStatuses: additionalErrors,
		},
	}
}
