package handler

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/Foodan-Dev/danshi-backend/internal/httpx"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/envelope"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

// Search 处理 Search 域 HTTP 请求。
type Search struct {
	service *service.SearchService
}

// NewSearch 创建搜索 handler。
func NewSearch(searchService *service.SearchService) *Search { return &Search{service: searchService} }

// Posts 搜索已发布帖子。
func (h *Search) Posts(ctx context.Context, c *app.RequestContext) {
	query, err := bindQuery[searchPostsQuery](c)
	if err == nil {
		err = validateRequiredQuery(c, query.Search)
	}
	filters, filterErr := listPostsInput(query.Filters, query.SortBy, query.Pagination)
	if err == nil {
		err = filterErr
	}
	principal, principalErr := httpx.CurrentPrincipal(c)
	if err == nil {
		err = principalErr
	}
	var result *service.SearchPostList
	if err == nil {
		result, err = h.service.Posts(ctx, service.SearchPostsInput{
			Query: query.Search.Query, Filters: filters,
		}, principal.User.ID)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("请求成功", result))
}

// Users 按昵称搜索当前可用用户。
func (h *Search) Users(ctx context.Context, c *app.RequestContext) {
	query, err := bindQuery[searchUsersQuery](c)
	if err == nil {
		err = validateRequiredQuery(c, query.Search)
	}
	params, paramsErr := query.Pagination.params()
	if err == nil {
		err = paramsErr
	}
	principal, principalErr := httpx.CurrentPrincipal(c)
	if err == nil {
		err = principalErr
	}
	var result *service.SearchUserList
	if err == nil {
		result, err = h.service.Users(ctx, query.Search.Query, params, principal.User.ID)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("请求成功", result))
}
