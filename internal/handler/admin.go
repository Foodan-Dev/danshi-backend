package handler

import (
	"context"
	"encoding/json"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/shopspring/decimal"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/httpx"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/envelope"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/pagination"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/ptime"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

// Admin 处理管理端 HTTP 请求。
type Admin struct {
	service *service.AdminService
}

// NewAdmin 创建管理端 handler。
func NewAdmin(adminService *service.AdminService) *Admin { return &Admin{service: adminService} }

type adminPostReviewRequest struct {
	Status   string  `json:"status"`
	Feedback *string `json:"feedback"`
}

type adminUserStatusRequest struct {
	IsActive       *bool       `json:"is_active"`
	BanIsPermanent *bool       `json:"ban_is_permanent"`
	BannedUntil    *ptime.Time `json:"banned_until"`
	BanReason      *string     `json:"ban_reason"`
	Reason         *string     `json:"reason"`
}

type adminUserRoleRequest struct {
	Role   model.UserRole       `json:"role"`
	Action model.UserRoleAction `json:"action"`
}

type adminManualReviewRequest struct {
	Verdict     model.ModerationVerdict `json:"verdict"`
	Labels      []string                `json:"labels"`
	Score       *decimal.Decimal        `json:"score"`
	RawResponse json.RawMessage         `json:"raw_response"`
}

type renameAdminTagRequest struct {
	Name string `json:"name"`
}

type mergeAdminTagRequest struct {
	TargetTagID uint64 `json:"target_tag_id"`
}

// PendingPosts 返回待审帖子。
func (h *Admin) PendingPosts(ctx context.Context, c *app.RequestContext) {
	query, err := bindQuery[paginationQuery](c)
	params, paramsErr := query.params()
	if err == nil {
		err = paramsErr
	}
	var result *service.AdminPostList
	if err == nil {
		result, err = h.service.PendingPosts(ctx, params)
	}
	respondAdmin(ctx, c, "请求成功", result, err)
}

// ReviewPost 裁决指定待审帖子。
func (h *Admin) ReviewPost(ctx context.Context, c *app.RequestContext) {
	postID, principal, err := adminIdentity(c, "post_id")
	var request adminPostReviewRequest
	if err == nil {
		err = bindJSON(c, &request)
	}
	var result *service.AdminPostReviewResult
	if err == nil {
		result, err = h.service.ReviewPost(ctx, postID, principal.User.ID, service.AdminPostReviewInput{
			Status: request.Status, Feedback: request.Feedback,
		})
	}
	respondAdmin(ctx, c, "审核完成", result, err)
}

// Posts 返回全部帖子，包含软删除行。
func (h *Admin) Posts(ctx context.Context, c *app.RequestContext) {
	query, err := bindQuery[adminPostsQuery](c)
	params, paramsErr := query.Pagination.params()
	if err == nil {
		err = paramsErr
	}
	var result *service.AdminPostList
	if err == nil {
		result, err = h.service.Posts(ctx, string(query.Status), string(query.PostType), params)
	}
	respondAdmin(ctx, c, "请求成功", result, err)
}

// DeletePost 以管理员来源软删除帖子。
func (h *Admin) DeletePost(ctx context.Context, c *app.RequestContext) {
	postID, principal, err := adminIdentity(c, "post_id")
	var result *service.AdminPostDeleteResult
	if err == nil {
		result, err = h.service.DeletePost(ctx, postID, principal.User.ID)
	}
	respondAdmin(ctx, c, "删除成功", result, err)
}

// RestorePost 恢复作者、管理员或机审来源的软删除帖子。
func (h *Admin) RestorePost(ctx context.Context, c *app.RequestContext) {
	postID, principal, err := adminIdentity(c, "post_id")
	var result *service.AdminPostRestoreResult
	if err == nil {
		result, err = h.service.RestorePost(ctx, postID, principal.User.ID)
	}
	respondAdmin(ctx, c, "恢复成功", result, err)
}

// Image 返回具备内容审核能力的角色可见的单张图片详情。
func (h *Admin) Image(ctx context.Context, c *app.RequestContext) {
	imageAssetID, _, err := adminIdentity(c, "image_asset_id")
	var result *service.AdminImageView
	if err == nil {
		result, err = h.service.Image(ctx, imageAssetID)
	}
	respondAdmin(ctx, c, "请求成功", result, err)
}

// Users 返回全部用户，包含软删除行。
func (h *Admin) Users(ctx context.Context, c *app.RequestContext) {
	query, err := bindQuery[adminUsersQuery](c)
	params, paramsErr := query.Pagination.params()
	if err == nil {
		err = paramsErr
	}
	var active *bool
	if err == nil {
		active, err = optionalBool(query.IsActive)
	}
	var result *service.AdminUserList
	if err == nil {
		result, err = h.service.Users(ctx, string(query.Role), active, params)
	}
	respondAdmin(ctx, c, "请求成功", result, err)
}

// User 返回单个用户详情与历史封禁记录。
func (h *Admin) User(ctx context.Context, c *app.RequestContext) {
	userID, _, err := adminIdentity(c, "user_id")
	var result *service.AdminUserDetail
	if err == nil {
		result, err = h.service.User(ctx, userID)
	}
	respondAdmin(ctx, c, "请求成功", result, err)
}

// UserPosts 返回目标用户全部帖子，包含未通过与软删除内容。
func (h *Admin) UserPosts(ctx context.Context, c *app.RequestContext) {
	userID, _, err := adminIdentity(c, "user_id")
	query, queryErr := bindQuery[adminPostsQuery](c)
	if err == nil {
		err = queryErr
	}
	params, paramsErr := query.Pagination.params()
	if err == nil {
		err = paramsErr
	}
	var result *service.AdminPostList
	if err == nil {
		result, err = h.service.UserPosts(
			ctx, userID, string(query.Status), string(query.PostType), params,
		)
	}
	respondAdmin(ctx, c, "请求成功", result, err)
}

// UpdateUserStatus 永久封禁、限时封禁或解封用户。
func (h *Admin) UpdateUserStatus(ctx context.Context, c *app.RequestContext) {
	userID, principal, err := adminIdentity(c, "user_id")
	var request adminUserStatusRequest
	if err == nil {
		err = bindJSON(c, &request)
	}
	var result *service.AdminUserStatusResult
	if err == nil {
		until := request.BannedUntil
		input := service.UpdateAdminUserStatusInput{
			IsActive: request.IsActive, BanIsPermanent: request.BanIsPermanent,
			BanReason: request.BanReason, LegacyReason: request.Reason,
		}
		if until != nil {
			value := until.Std()
			input.BannedUntil = &value
		}
		result, err = h.service.UpdateUserStatus(ctx, userID, principal.User.ID, input)
	}
	respondAdmin(ctx, c, "用户状态更新成功", result, err)
}

// UpdateUserRole 调整用户角色。
func (h *Admin) UpdateUserRole(ctx context.Context, c *app.RequestContext) {
	userID, principal, err := adminIdentity(c, "user_id")
	var request adminUserRoleRequest
	if err == nil {
		err = bindJSON(c, &request)
	}
	var result *service.AdminUserRoleResult
	if err == nil {
		result, err = h.service.UpdateUserRole(
			ctx, userID, principal.User.ID, request.Role, request.Action,
		)
	}
	respondAdmin(ctx, c, "权限更新成功", result, err)
}

// Admins 返回 admin 角色用户。
func (h *Admin) Admins(ctx context.Context, c *app.RequestContext) {
	query, err := bindQuery[paginationQuery](c)
	params, paramsErr := query.params()
	if err == nil {
		err = paramsErr
	}
	var result *service.AdminUserList
	if err == nil {
		result, err = h.service.Admins(ctx, params)
	}
	respondAdmin(ctx, c, "请求成功", result, err)
}

// SuperAdmins 返回 super_admin 角色用户。
func (h *Admin) SuperAdmins(ctx context.Context, c *app.RequestContext) {
	query, err := bindQuery[paginationQuery](c)
	params, paramsErr := query.params()
	if err == nil {
		err = paramsErr
	}
	var result *service.AdminUserList
	if err == nil {
		result, err = h.service.SuperAdmins(ctx, params)
	}
	respondAdmin(ctx, c, "请求成功", result, err)
}

// Comments 返回全部评论，包含软删除行。
func (h *Admin) Comments(ctx context.Context, c *app.RequestContext) {
	query, err := bindQuery[adminCommentsQuery](c)
	params, paramsErr := query.Pagination.params()
	if err == nil {
		err = paramsErr
	}
	var postID *uint64
	if err == nil && query.PostID != "" {
		value, parseErr := positivePathID(query.PostID, "post_id")
		postID, err = &value, parseErr
	}
	var result *service.AdminCommentList
	if err == nil {
		result, err = h.service.Comments(ctx, postID, params)
	}
	respondAdmin(ctx, c, "请求成功", result, err)
}

// DeleteComment 以管理员来源软删除评论。
func (h *Admin) DeleteComment(ctx context.Context, c *app.RequestContext) {
	commentID, principal, err := adminIdentity(c, "comment_id")
	var result *service.AdminCommentDeleteResult
	if err == nil {
		result, err = h.service.DeleteComment(ctx, commentID, principal.User.ID)
	}
	respondAdmin(ctx, c, "删除成功", result, err)
}

// RestoreComment 恢复机审误杀评论。
func (h *Admin) RestoreComment(ctx context.Context, c *app.RequestContext) {
	commentID, principal, err := adminIdentity(c, "comment_id")
	var result *service.AdminCommentRestoreResult
	if err == nil {
		result, err = h.service.RestoreComment(ctx, commentID, principal.User.ID)
	}
	respondAdmin(ctx, c, "恢复成功", result, err)
}

// Tags 返回支持名称、审核状态和下架状态筛选的话题标签管理列表。
func (h *Admin) Tags(ctx context.Context, c *app.RequestContext) {
	query, err := bindQuery[adminTagsQuery](c)
	params, paramsErr := query.Pagination.params()
	if err == nil {
		err = paramsErr
	}
	var isDeleted *bool
	if err == nil {
		isDeleted, err = optionalBoolField(query.IsDeleted, "is_deleted")
	}
	var result *service.AdminTagList
	if err == nil {
		result, err = h.service.Tags(ctx, service.AdminTagListInput{
			Name: query.Name, Moderation: string(query.Moderation), IsDeleted: isDeleted,
			Pagination: params,
		})
	}
	respondAdmin(ctx, c, "请求成功", result, err)
}

// RenameTag 重命名话题标签。
func (h *Admin) RenameTag(ctx context.Context, c *app.RequestContext) {
	tagID, principal, err := adminIdentity(c, "tag_id")
	var request renameAdminTagRequest
	if err == nil {
		err = bindJSON(c, &request)
	}
	var result *service.AdminTagView
	if err == nil {
		result, err = h.service.RenameTag(
			ctx, tagID, principal.User.ID, service.RenameAdminTagInput{Name: request.Name},
		)
	}
	respondAdmin(ctx, c, "标签已重命名", result, err)
}

// MergeTag 把源标签合并到目标标签。
func (h *Admin) MergeTag(ctx context.Context, c *app.RequestContext) {
	tagID, principal, err := adminIdentity(c, "tag_id")
	var request mergeAdminTagRequest
	if err == nil {
		err = bindJSON(c, &request)
	}
	var result *service.AdminTagMergeResult
	if err == nil {
		result, err = h.service.MergeTag(ctx, tagID, principal.User.ID, service.MergeAdminTagInput{
			TargetTagID: request.TargetTagID,
		})
	}
	respondAdmin(ctx, c, "标签已合并", result, err)
}

// DeleteTag 下架标签但保留帖子关联。
func (h *Admin) DeleteTag(ctx context.Context, c *app.RequestContext) {
	tagID, _, err := adminIdentity(c, "tag_id")
	var result *service.AdminTagView
	if err == nil {
		result, err = h.service.DeleteTag(ctx, tagID)
	}
	respondAdmin(ctx, c, "标签已下架", result, err)
}

// RestoreTag 恢复标签及其原有帖子关联的展示。
func (h *Admin) RestoreTag(ctx context.Context, c *app.RequestContext) {
	tagID, _, err := adminIdentity(c, "tag_id")
	var result *service.AdminTagView
	if err == nil {
		result, err = h.service.RestoreTag(ctx, tagID)
	}
	respondAdmin(ctx, c, "标签已恢复", result, err)
}

// HotTags 返回实时热门标签 TopN。
func (h *Admin) HotTags(ctx context.Context, c *app.RequestContext) {
	query, err := bindQuery[hotTagsQuery](c)
	rawLimit := query.Limit
	if rawLimit == "" {
		rawLimit = "10"
	}
	request, paramsErr := pagination.ParseCursorRequest("", rawLimit)
	if err == nil {
		err = paramsErr
	}
	var result *service.AdminHotTagList
	if err == nil {
		result, err = h.service.HotTags(ctx, request.Limit)
	}
	respondAdmin(ctx, c, "请求成功", result, err)
}

// PendingModeration 返回通用待人工复核队列。
func (h *Admin) PendingModeration(ctx context.Context, c *app.RequestContext) {
	query, err := bindQuery[moderationPendingQuery](c)
	params, paramsErr := query.Pagination.params()
	if err == nil {
		err = paramsErr
	}
	var result *service.AdminModerationList
	if err == nil {
		result, err = h.service.PendingModeration(ctx, query.Label, params)
	}
	respondAdmin(ctx, c, "请求成功", result, err)
}

// ManualReview 追加任意目标类型的人工裁决行。
func (h *Admin) ManualReview(ctx context.Context, c *app.RequestContext) {
	recordID, principal, err := adminIdentity(c, "moderation_record_id")
	var request adminManualReviewRequest
	if err == nil {
		err = bindJSON(c, &request)
	}
	var result *service.AdminReviewResult
	if err == nil {
		result, err = h.service.ManualReview(ctx, recordID, principal.User.ID, service.AdminManualReviewInput{
			Verdict: request.Verdict, Labels: request.Labels,
			Score: request.Score, RawResponse: request.RawResponse,
		})
	}
	respondAdmin(ctx, c, "复核完成", result, err)
}

func adminIdentity(c *app.RequestContext, field string) (uint64, *service.Principal, error) {
	id, err := positivePathID(c.Param(field), field)
	if err != nil {
		return 0, nil, err
	}
	principal, err := httpx.CurrentPrincipal(c)
	return id, principal, err
}

func respondAdmin[T any](
	ctx context.Context,
	c *app.RequestContext,
	message string,
	result *T,
	err error,
) {
	if err != nil {
		failService(ctx, c, err)
		return
	}
	if result == nil {
		httpx.Fail(ctx, c, apierr.Internal(nil))
		return
	}
	c.JSON(consts.StatusOK, envelope.OK(message, result))
}
