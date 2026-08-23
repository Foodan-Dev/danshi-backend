// Package apicontract 保存 HTTP 契约测试与 OpenAPI 生成器共享的路由声明。
package apicontract

import (
	"fmt"
	"net/http"
)

// Route 描述一条必须同时存在于 Hertz 路由表和 OpenAPI 注册表中的路由。
type Route struct {
	Method         string
	Path           string
	BearerAuth     bool
	ExpectedStatus int
}

// TypeBinding 保存一条端点的 query、JSON 请求体与成功响应 data 的 Go 类型实例。
// nil Query 表示尚未声明 query 契约；无 query 的 GET 必须显式使用 NoQuery。
// nil Request 表示没有 JSON 请求体；nil Response 表示成功响应 data 固定为 null。
type TypeBinding struct {
	Query    any
	Request  any
	Response any
	// AdditionalErrorStatuses 只登记无法从鉴权、请求体、路径参数和管理端命名空间
	// 推导出的业务错误状态；生成器会拒绝重复登记规则已覆盖的状态。
	AdditionalErrorStatuses []int
}

// NoQuery 显式声明端点不读取任何 query 参数。
type NoQuery struct{}

// TypedRoute 把类型绑定关联到一条明确的 method + Hertz path。
type TypedRoute struct {
	Method string
	Path   string
	TypeBinding
}

// Key 返回用于路由表双向对账的稳定键。
func Key(method, path string) string { return method + " " + path }

// OperationKey 返回当前路由的稳定键。
func (r Route) OperationKey() string { return Key(r.Method, r.Path) }

// Routes 返回全部运行时与业务路由的显式契约声明。
func Routes() []Route {
	result := make([]Route, len(routes))
	copy(result, routes)
	return result
}

// ByKey 把路由声明索引为稳定键，并拒绝重复登记。
func ByKey(declarations []Route) (map[string]Route, error) {
	indexed := make(map[string]Route, len(declarations))
	for _, declaration := range declarations {
		key := declaration.OperationKey()
		if _, exists := indexed[key]; exists {
			return nil, fmt.Errorf("OpenAPI 路由重复登记: %s", key)
		}
		indexed[key] = declaration
	}
	return indexed, nil
}

// BindingsByKey 索引类型化注册表，并拒绝重复登记。
func BindingsByKey(bindings []TypedRoute) (map[string]TypeBinding, error) {
	indexed := make(map[string]TypeBinding, len(bindings))
	for _, binding := range bindings {
		key := Key(binding.Method, binding.Path)
		if _, exists := indexed[key]; exists {
			return nil, fmt.Errorf("OpenAPI 类型重复登记: %s", key)
		}
		indexed[key] = binding.TypeBinding
	}
	return indexed, nil
}

var routes = []Route{
	{Method: http.MethodGet, Path: "/health", ExpectedStatus: http.StatusOK},
	{Method: http.MethodGet, Path: "/ready", ExpectedStatus: http.StatusOK},
	{Method: http.MethodPost, Path: "/api/v2/auth/email-verification-codes", ExpectedStatus: http.StatusUnprocessableEntity},
	{Method: http.MethodPost, Path: "/api/v2/auth/register", ExpectedStatus: http.StatusUnprocessableEntity},
	{Method: http.MethodPost, Path: "/api/v2/auth/login", ExpectedStatus: http.StatusUnprocessableEntity},
	{Method: http.MethodPost, Path: "/api/v2/auth/refresh", ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/auth/me", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPost, Path: "/api/v2/auth/logout", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPost, Path: "/api/v2/auth/logout-all", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/auth/sessions", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodDelete, Path: "/api/v2/auth/sessions/:id", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/users/:user_id", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPut, Path: "/api/v2/users/:user_id", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodDelete, Path: "/api/v2/users/:user_id", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/users/:user_id/posts", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/users/:user_id/favorites", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPost, Path: "/api/v2/users/:user_id/follow", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodDelete, Path: "/api/v2/users/:user_id/follow", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/users/:user_id/following", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/users/:user_id/followers", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/posts", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/posts/:post_id", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPost, Path: "/api/v2/posts", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPut, Path: "/api/v2/posts/:post_id", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodDelete, Path: "/api/v2/posts/:post_id", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/posts/:post_id/history", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPost, Path: "/api/v2/posts/:post_id/like", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodDelete, Path: "/api/v2/posts/:post_id/like", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPost, Path: "/api/v2/posts/:post_id/favorite", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodDelete, Path: "/api/v2/posts/:post_id/favorite", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/posts/:post_id/comments", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPost, Path: "/api/v2/posts/:post_id/comments", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/comments/:comment_id/replies", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPut, Path: "/api/v2/comments/:comment_id", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/comments/:comment_id/history", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPost, Path: "/api/v2/comments/:comment_id/like", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodDelete, Path: "/api/v2/comments/:comment_id/like", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodDelete, Path: "/api/v2/comments/:comment_id", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/notifications", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/notifications/unread-count", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPut, Path: "/api/v2/notifications/read-all", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPut, Path: "/api/v2/notifications/:notification_id/read", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/search/posts", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/search/users", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPost, Path: "/api/v2/uploads/presign", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/uploads/:upload_id", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPost, Path: "/api/v2/uploads/:upload_id/complete", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/config", ExpectedStatus: http.StatusOK},
	{Method: http.MethodPost, Path: "/api/v2/dictionary-suggestions", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/dictionary-suggestions/mine", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/admin/dictionary-suggestions", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPost, Path: "/api/v2/admin/dictionary-suggestions/:suggestion_id/approve", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPost, Path: "/api/v2/admin/dictionary-suggestions/:suggestion_id/reject", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPost, Path: "/api/v2/admin/flavors", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPatch, Path: "/api/v2/admin/flavors/:flavor_id", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodDelete, Path: "/api/v2/admin/flavors/:flavor_id", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPost, Path: "/api/v2/admin/cuisines", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPatch, Path: "/api/v2/admin/cuisines/:cuisine_id", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodDelete, Path: "/api/v2/admin/cuisines/:cuisine_id", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPost, Path: "/api/v2/admin/canteens", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPatch, Path: "/api/v2/admin/canteens/:canteen_id", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodDelete, Path: "/api/v2/admin/canteens/:canteen_id", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPost, Path: "/api/v2/admin/canteens/:canteen_id/windows", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPatch, Path: "/api/v2/admin/canteen-windows/:window_id", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodDelete, Path: "/api/v2/admin/canteen-windows/:window_id", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPost, Path: "/api/v2/moderation/tencent-ci/callback", ExpectedStatus: http.StatusForbidden},
	{Method: http.MethodGet, Path: "/api/v2/admin/posts/pending", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPut, Path: "/api/v2/admin/posts/:post_id/review", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/admin/posts", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodDelete, Path: "/api/v2/admin/posts/:post_id", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPut, Path: "/api/v2/admin/posts/:post_id/restore", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/admin/images/:image_asset_id", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/admin/users", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/admin/users/:user_id", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/admin/users/:user_id/posts", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPut, Path: "/api/v2/admin/users/:user_id/status", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPut, Path: "/api/v2/admin/users/:user_id/role", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/admin/admins", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/admin/super-admins", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/admin/comments", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodDelete, Path: "/api/v2/admin/comments/:comment_id", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPut, Path: "/api/v2/admin/comments/:comment_id/restore", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodGet, Path: "/api/v2/admin/moderation-records/pending", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
	{Method: http.MethodPut, Path: "/api/v2/admin/moderation-records/:moderation_record_id/review", BearerAuth: true, ExpectedStatus: http.StatusUnauthorized},
}
