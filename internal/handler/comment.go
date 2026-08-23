package handler

import (
	"context"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/httpx"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/envelope"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/pagination"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

// Comment 处理 Comment 域 HTTP 请求。
type Comment struct {
	service *service.CommentService
}

// NewComment 创建评论 handler。
func NewComment(commentService *service.CommentService) *Comment {
	return &Comment{service: commentService}
}

type createCommentRequest struct {
	Content          string   `json:"content"`
	ParentID         *uint64  `json:"parent_id"`
	ReplyToUserID    *uint64  `json:"reply_to_user_id"`
	MentionedUserIDs []uint64 `json:"mentioned_user_ids"`
}

type updateCommentRequest struct {
	Content          string   `json:"content"`
	MentionedUserIDs []uint64 `json:"mentioned_user_ids"`
}

// List 返回帖子楼主评论页与每楼最新两条回复预览。
func (h *Comment) List(ctx context.Context, c *app.RequestContext) {
	query, queryErr := bindQuery[commentListQuery](c)
	postID, err := positivePathID(c.Param("post_id"), "post_id")
	if err == nil {
		err = queryErr
	}
	principal, principalErr := httpx.CurrentPrincipal(c)
	if err == nil {
		err = principalErr
	}
	params, paramsErr := query.Pagination.params()
	if err == nil {
		err = paramsErr
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	result, err := h.service.List(ctx, postID, principal.User.ID, query.SortBy, params)
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("请求成功", result))
}

// Replies 返回一楼内拍扁后的全部回复。
func (h *Comment) Replies(ctx context.Context, c *app.RequestContext) {
	query, err := bindQuery[replyPaginationQuery](c)
	var commentID uint64
	var principal *service.Principal
	var params pagination.CursorRequest
	if err == nil {
		commentID, principal, params, err = commentListIdentity(c, query)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	result, err := h.service.Replies(ctx, commentID, principal.User.ID, params)
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("请求成功", result))
}

// Create 发表楼主评论或真实链上的回复。
func (h *Comment) Create(ctx context.Context, c *app.RequestContext) {
	postID, err := positivePathID(c.Param("post_id"), "post_id")
	principal, principalErr := httpx.CurrentPrincipal(c)
	if err == nil {
		err = principalErr
	}
	var request createCommentRequest
	if err == nil {
		err = bindJSON(c, &request)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	result, err := h.service.Create(ctx, postID, service.CreateCommentInput{
		Content: request.Content, ParentID: request.ParentID, ReplyToUserID: request.ReplyToUserID,
		MentionedUserIDs: request.MentionedUserIDs,
	}, principal.User.ID)
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("评论发布成功", result))
}

// Update 编辑作者自己的评论并追加版本。
func (h *Comment) Update(ctx context.Context, c *app.RequestContext) {
	commentID, principal, err := commentIdentity(c)
	var request updateCommentRequest
	if err == nil {
		err = bindJSON(c, &request)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	result, err := h.service.Update(ctx, commentID, service.UpdateCommentInput{
		Content: request.Content, MentionedUserIDs: request.MentionedUserIDs,
	}, principal.User.ID)
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("更新成功", result))
}

// Histories 返回作者自己的评论编辑历史。
func (h *Comment) Histories(ctx context.Context, c *app.RequestContext) {
	commentID, principal, err := commentIdentity(c)
	var result *service.CommentHistoryList
	if err == nil {
		result, err = h.service.Histories(ctx, commentID, principal.User.ID)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("请求成功", result))
}

// Like 幂等点赞评论。
func (h *Comment) Like(ctx context.Context, c *app.RequestContext) {
	commentID, principal, err := commentIdentity(c)
	var result *service.CommentLikeResult
	if err == nil {
		result, err = h.service.Like(ctx, commentID, principal.User.ID)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("点赞成功", result))
}

// Unlike 幂等取消点赞评论。
func (h *Comment) Unlike(ctx context.Context, c *app.RequestContext) {
	commentID, principal, err := commentIdentity(c)
	var result *service.CommentLikeResult
	if err == nil {
		result, err = h.service.Unlike(ctx, commentID, principal.User.ID)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("取消点赞成功", result))
}

// Delete 软删除作者自己的评论。
func (h *Comment) Delete(ctx context.Context, c *app.RequestContext) {
	commentID, principal, err := commentIdentity(c)
	if err == nil {
		err = h.service.Delete(ctx, commentID, principal.User.ID)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK[any]("删除成功", nil))
}

func commentIdentity(c *app.RequestContext) (uint64, *service.Principal, error) {
	commentID, err := positivePathID(c.Param("comment_id"), "comment_id")
	principal, principalErr := httpx.CurrentPrincipal(c)
	if err == nil {
		err = principalErr
	}
	return commentID, principal, err
}

func commentListIdentity(
	c *app.RequestContext,
	query replyPaginationQuery,
) (uint64, *service.Principal, pagination.CursorRequest, error) {
	commentID, principal, err := commentIdentity(c)
	params, paramsErr := query.params()
	if err == nil {
		err = paramsErr
	}
	return commentID, principal, params, err
}

func positivePathID(raw, field string) (uint64, error) {
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, apierr.InvalidField(field, apierr.FieldInvalidFormat, "%s 必须是正整数", field)
	}
	return id, nil
}
