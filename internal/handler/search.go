package handler

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/envelope"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/pagination"
	"github.com/jingyijun/danshi_backend_go/internal/router/middleware"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

// Search 处理 Search 域 HTTP 请求。
type Search struct {
	service *service.SearchService
}

// NewSearch 创建搜索 handler。
func NewSearch(searchService *service.SearchService) *Search { return &Search{service: searchService} }

// Posts 搜索已发布帖子。
func (h *Search) Posts(ctx context.Context, c *app.RequestContext) {
	query, err := requiredSearchQuery(c)
	filters, filterErr := listPostsInput(c)
	if err == nil {
		err = filterErr
	}
	principal, principalErr := middleware.CurrentPrincipal(c)
	if err == nil {
		err = principalErr
	}
	var result *service.SearchPostList
	if err == nil {
		result, err = h.service.Posts(ctx, service.SearchPostsInput{
			Query: query, Filters: filters,
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
	query, err := requiredSearchQuery(c)
	params, paramsErr := pagination.Parse(c.Query("page"), c.Query("limit"))
	if err == nil {
		err = paramsErr
	}
	principal, principalErr := middleware.CurrentPrincipal(c)
	if err == nil {
		err = principalErr
	}
	var result *service.SearchUserList
	if err == nil {
		result, err = h.service.Users(ctx, query, params, principal.User.ID)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("请求成功", result))
}

func requiredSearchQuery(c *app.RequestContext) (string, error) {
	if !c.Request.URI().QueryArgs().Has("q") {
		return "", apierr.InvalidField("q", apierr.FieldRequired, "q 不能为空")
	}
	return c.Query("q"), nil
}
