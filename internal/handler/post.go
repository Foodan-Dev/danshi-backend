package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/httpx"
	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/envelope"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/money"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/pagination"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

// Post 处理 Post 域 HTTP 请求。
type Post struct {
	service *service.PostService
}

// NewPost 创建帖子 handler。
func NewPost(postService *service.PostService) *Post { return &Post{service: postService} }

type postPayloadRequest struct {
	Title           string                    `json:"title"`
	Content         string                    `json:"content"`
	Category        string                    `json:"category"`
	ShareType       *string                   `json:"share_type"`
	CanteenCode     *string                   `json:"canteen_code"`
	CanteenWindowID *uint64                   `json:"canteen_window_id"`
	Cuisine         *string                   `json:"cuisine"`
	Price           json.RawMessage           `json:"price"`
	Flavors         []string                  `json:"flavors"`
	Tags            []string                  `json:"tags"`
	Images          []string                  `json:"images"`
	BudgetRange     *service.BudgetRangeInput `json:"budget_range"`
	Preferences     *service.PreferencesInput `json:"preferences"`
}

type createPostRequest struct {
	postPayloadRequest
	PostType string  `json:"post_type"`
	Status   *string `json:"status"`
}

type updatePostRequest struct {
	postPayloadRequest
	PostType   *string `json:"post_type"`
	Status     *string `json:"status"`
	EditReason *string `json:"edit_reason"`
}

// List 返回公开帖子信息流。
func (h *Post) List(ctx context.Context, c *app.RequestContext) {
	principal, err := httpx.CurrentPrincipal(c)
	if err != nil {
		failService(ctx, c, err)
		return
	}
	query, err := bindQuery[listPostsQuery](c)
	if err != nil {
		httpx.Fail(ctx, c, err)
		return
	}
	input, err := feedListPostsInput(query.Filters, query.SortBy, query.Cursor, query.Pagination)
	if err != nil {
		httpx.Fail(ctx, c, err)
		return
	}
	result, err := h.service.List(ctx, input, principal.User.ID)
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("请求成功", result))
}

// Get 返回帖子详情并增加浏览数。
func (h *Post) Get(ctx context.Context, c *app.RequestContext) {
	postID, principal, err := postRequestIdentity(c)
	if err != nil {
		failService(ctx, c, err)
		return
	}
	result, err := h.service.Get(ctx, postID, principal.User.ID)
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("请求成功", result))
}

// Create 创建草稿或提交帖子审核。
func (h *Post) Create(ctx context.Context, c *app.RequestContext) {
	var request createPostRequest
	if err := bindJSON(c, &request); err != nil {
		httpx.Fail(ctx, c, err)
		return
	}
	principal, err := httpx.CurrentPrincipal(c)
	input, inputErr := createInput(request)
	if err == nil {
		err = inputErr
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	result, err := h.service.Create(ctx, input, principal.User.ID)
	if err != nil {
		failService(ctx, c, err)
		return
	}
	message := "帖子发布成功"
	if request.Status != nil && *request.Status == string(model.PostStatusDraft) {
		message = "草稿保存成功"
	}
	c.JSON(consts.StatusOK, envelope.OK(message, result))
}

// Update 全量编辑帖子并追加新版本。
func (h *Post) Update(ctx context.Context, c *app.RequestContext) {
	postID, principal, err := postRequestIdentity(c)
	var request updatePostRequest
	if err == nil {
		err = bindJSON(c, &request)
	}
	input, inputErr := updateInput(request)
	if err == nil {
		err = inputErr
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	result, err := h.service.Update(ctx, postID, input, principal.User.ID)
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK(updateMessage(request.Status), result))
}

// Delete 软删除作者自己的帖子。
func (h *Post) Delete(ctx context.Context, c *app.RequestContext) {
	postID, principal, err := postRequestIdentity(c)
	if err == nil {
		err = h.service.Delete(ctx, postID, principal.User.ID)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK[any]("删除成功", nil))
}

// Histories 返回作者自己的帖子编辑历史。
func (h *Post) Histories(ctx context.Context, c *app.RequestContext) {
	postID, principal, err := postRequestIdentity(c)
	var result *service.PostHistoryList
	if err == nil {
		result, err = h.service.Histories(ctx, postID, principal.User.ID)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("请求成功", result))
}

// Like 幂等点赞。
func (h *Post) Like(ctx context.Context, c *app.RequestContext) {
	postID, principal, err := postRequestIdentity(c)
	var result *service.PostLikeResult
	if err == nil {
		result, err = h.service.Like(ctx, postID, principal.User.ID)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("点赞成功", result))
}

// Unlike 幂等取消点赞。
func (h *Post) Unlike(ctx context.Context, c *app.RequestContext) {
	postID, principal, err := postRequestIdentity(c)
	var result *service.PostLikeResult
	if err == nil {
		result, err = h.service.Unlike(ctx, postID, principal.User.ID)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("取消点赞成功", result))
}

// Favorite 幂等收藏。
func (h *Post) Favorite(ctx context.Context, c *app.RequestContext) {
	postID, principal, err := postRequestIdentity(c)
	var result *service.PostFavoriteResult
	if err == nil {
		result, err = h.service.Favorite(ctx, postID, principal.User.ID)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("收藏成功", result))
}

// Unfavorite 幂等取消收藏。
func (h *Post) Unfavorite(ctx context.Context, c *app.RequestContext) {
	postID, principal, err := postRequestIdentity(c)
	var result *service.PostFavoriteResult
	if err == nil {
		result, err = h.service.Unfavorite(ctx, postID, principal.User.ID)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("取消收藏成功", result))
}

func createInput(request createPostRequest) (service.CreatePostInput, error) {
	payload, err := postPayloadInput(request.postPayloadRequest)
	return service.CreatePostInput{PostPayload: payload, PostType: request.PostType, Status: request.Status}, err
}

func updateInput(request updatePostRequest) (service.UpdatePostInput, error) {
	payload, err := postPayloadInput(request.postPayloadRequest)
	return service.UpdatePostInput{
		PostPayload: payload, PostType: request.PostType, Status: request.Status, EditReason: request.EditReason,
	}, err
}

func postPayloadInput(request postPayloadRequest) (service.PostPayload, error) {
	price, err := optionalPrice(request.Price)
	if err != nil {
		return service.PostPayload{}, err
	}
	return service.PostPayload{
		Title: request.Title, Content: request.Content, Category: request.Category,
		ShareType: request.ShareType, CanteenCode: request.CanteenCode,
		CanteenWindowID: request.CanteenWindowID, Cuisine: request.Cuisine, Price: price,
		Flavors: request.Flavors, Tags: request.Tags, Images: request.Images,
		BudgetRange: request.BudgetRange, Preferences: request.Preferences,
	}, nil
}

func optionalPrice(raw json.RawMessage) (*money.Amount, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, apierr.InvalidField("price", apierr.FieldInvalidFormat, "price 必须是十进制字符串")
	}
	amount, err := money.Parse(value)
	if err != nil {
		return nil, apierr.InvalidField("price", apierr.FieldInvalidFormat, "price 格式不合法")
	}
	return &amount, nil
}

func listPostsInput(
	query postFiltersQuery,
	sortBy string,
	paginationQuery paginationQuery,
) (service.ListPostsInput, error) {
	params, err := paginationQuery.params()
	if err != nil {
		return service.ListPostsInput{}, err
	}
	minPrice, err := queryPrice(query.MinPrice, "min_price")
	if err != nil {
		return service.ListPostsInput{}, err
	}
	maxPrice, err := queryPrice(query.MaxPrice, "max_price")
	if err != nil {
		return service.ListPostsInput{}, err
	}
	return service.ListPostsInput{
		PostType: string(query.PostType), ShareType: string(query.ShareType),
		Category: string(query.Category), CanteenCode: query.CanteenCode,
		Cuisine: query.Cuisine, Flavors: query.Flavors,
		Tags: query.Tags, MinPrice: minPrice, MaxPrice: maxPrice,
		SortBy: sortBy, Pagination: params,
	}, nil
}

func feedListPostsInput(
	query postFiltersQuery,
	sortBy string,
	rawCursor string,
	paginationQuery paginationQuery,
) (service.ListPostsInput, error) {
	input, err := listPostsInput(query, sortBy, paginationQuery)
	if err != nil {
		return service.ListPostsInput{}, err
	}
	effectiveSort := sortBy
	if effectiveSort == "" {
		effectiveSort = "latest"
	}
	if effectiveSort == "latest" {
		if input.Pagination.Page != pagination.DefaultPage {
			return service.ListPostsInput{}, apierr.InvalidField(
				"page", apierr.FieldConflict, "latest 排序使用 cursor 分页",
			)
		}
		input.Cursor = pagination.CursorRequest{Token: rawCursor, Limit: input.Pagination.Limit}
		return input, nil
	}
	if rawCursor != "" {
		return service.ListPostsInput{}, apierr.InvalidField(
			"cursor", apierr.FieldConflict, "cursor 仅适用于 latest 排序",
		)
	}
	return input, nil
}

func queryPrice(raw, field string) (*money.Amount, error) {
	if raw == "" {
		return nil, nil
	}
	amount, err := money.Parse(raw)
	if err != nil {
		return nil, apierr.InvalidField(field, apierr.FieldInvalidFormat, "%s 格式不合法", field)
	}
	return &amount, nil
}

func splitQueryList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return strings.Split(raw, ",")
}

func postRequestIdentity(c *app.RequestContext) (uint64, *service.Principal, error) {
	postID, err := strconv.ParseUint(c.Param("post_id"), 10, 64)
	if err != nil || postID == 0 {
		return 0, nil, apierr.InvalidField(
			"post_id", apierr.FieldInvalidFormat, "post_id 必须是正整数",
		)
	}
	principal, err := httpx.CurrentPrincipal(c)
	return postID, principal, err
}

func updateMessage(status *string) string {
	if status == nil {
		return "更新成功"
	}
	switch model.PostStatus(*status) {
	case model.PostStatusDraft:
		return "草稿已保存"
	case model.PostStatusApproved:
		return "帖子已发布"
	default:
		return "更新成功"
	}
}
