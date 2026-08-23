package handler

import (
	"fmt"
	"reflect"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/Foodan-Dev/danshi-backend/internal/apicontract"
	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/pagination"
)

type paginationQuery struct {
	Page  string `query:"page" query_type:"integer" query_default:"1" query_min:"1"`
	Limit string `query:"limit" query_type:"integer" query_default:"20" query_min:"1" query_max:"100"`
}

func (query paginationQuery) params() (pagination.Params, error) {
	return pagination.Parse(query.Page, query.Limit)
}

type cursorPaginationQuery struct {
	Cursor string `query:"cursor"`
	Limit  string `query:"limit" query_type:"integer" query_default:"20" query_min:"1" query_max:"100"`
}

func (query cursorPaginationQuery) params() (pagination.CursorRequest, error) {
	return pagination.ParseCursorRequest(query.Cursor, query.Limit)
}

type replyPaginationQuery struct {
	Page  string `query:"page" query_type:"integer" query_default:"1" query_min:"1"`
	Limit string `query:"limit" query_type:"integer" query_default:"10" query_min:"1" query_max:"100"`
}

func (query replyPaginationQuery) params() (pagination.Params, error) {
	rawLimit := query.Limit
	if rawLimit == "" {
		rawLimit = "10"
	}
	return pagination.Parse(query.Page, rawLimit)
}

type postFiltersQuery struct {
	PostType    model.PostType     `query:"post_type"`
	ShareType   model.ShareType    `query:"share_type"`
	Category    model.PostCategory `query:"category"`
	CanteenCode string             `query:"canteen_code"`
	Cuisine     string             `query:"cuisine"`
	Flavors     []string           `query:"flavors" query_explode:"false"`
	Tags        []string           `query:"tags" query_explode:"false"`
	MinPrice    string             `query:"min_price" query_pattern:"^(?:0|[0-9]{1,8})(?:\\.[0-9]{1,2})?$"`
	MaxPrice    string             `query:"max_price" query_pattern:"^(?:0|[0-9]{1,8})(?:\\.[0-9]{1,2})?$"`
}

type listPostsQuery struct {
	Filters    postFiltersQuery
	SortBy     string `query:"sort_by" query_enum:"latest,hot,trending,price" query_default:"latest"`
	Cursor     string `query:"cursor"`
	Pagination paginationQuery
}

type requiredSearchQuery struct {
	Query string `query:"q,required"`
}

type searchPostsQuery struct {
	Search     requiredSearchQuery
	Filters    postFiltersQuery
	SortBy     string `query:"sort_by"`
	Pagination paginationQuery
}

type searchUsersQuery struct {
	Search     requiredSearchQuery
	Pagination paginationQuery
}

type userPostsQuery struct {
	Status     model.PostStatus `query:"status"`
	Pagination paginationQuery
}

type commentListQuery struct {
	SortBy     string `query:"sort_by" query_enum:"latest,hot" query_default:"latest"`
	Pagination paginationQuery
}

type notificationListQuery struct {
	IsRead     string                 `query:"is_read" query_type:"boolean"`
	Type       model.NotificationType `query:"type"`
	Pagination cursorPaginationQuery
}

type dictionaryPendingQuery struct {
	Kind       model.SuggestionKind `query:"kind"`
	Pagination paginationQuery
}

type adminPostsQuery struct {
	Status     model.PostStatus `query:"status"`
	PostType   model.PostType   `query:"post_type"`
	Pagination paginationQuery
}

type adminUsersQuery struct {
	Role       model.UserRole `query:"role" query_enum:"user,dict_reviewer,moderator,super_admin"`
	IsActive   string         `query:"is_active" query_type:"boolean"`
	Pagination paginationQuery
}

type adminCommentsQuery struct {
	PostID     string `query:"post_id" query_type:"integer" query_min:"1"`
	Pagination paginationQuery
}

type moderationCallbackQuery struct {
	Token string `query:"token,required"`
}

func bindQuery[T any](c *app.RequestContext) (T, error) {
	var target T
	fields, err := apicontract.QueryFields(target)
	if err != nil {
		return target, fmt.Errorf("解析 query 契约: %w", err)
	}
	value := reflect.ValueOf(&target).Elem()
	for _, field := range fields {
		raw := c.Query(field.Name)
		fieldValue := value.FieldByIndex(field.Index)
		switch {
		case fieldValue.Kind() == reflect.String:
			fieldValue.SetString(raw)
		case fieldValue.Kind() == reflect.Slice && fieldValue.Type().Elem().Kind() == reflect.String:
			items := splitQueryList(raw)
			if items != nil {
				fieldValue.Set(reflect.ValueOf(items).Convert(fieldValue.Type()))
			}
		default:
			return target, fmt.Errorf("query 参数 %s 使用了不支持的 Go 类型 %s", field.Name, field.Type)
		}
	}
	return target, nil
}

func validateRequiredQuery(c *app.RequestContext, query any) error {
	fields, err := apicontract.QueryFields(query)
	if err != nil {
		return fmt.Errorf("解析 query 契约: %w", err)
	}
	for _, field := range fields {
		if field.Required && !c.Request.URI().QueryArgs().Has(field.Name) {
			return apierr.InvalidField(
				field.Name, apierr.FieldRequired, "%s 不能为空", field.Name,
			)
		}
	}
	return nil
}
