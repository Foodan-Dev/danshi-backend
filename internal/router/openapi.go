package router

import (
	"net/http"

	"github.com/jingyijun/danshi_backend_go/internal/apicontract"
	"github.com/jingyijun/danshi_backend_go/internal/handler"
)

// OpenAPIBindings 合并 router 自有的探针类型与 handler 私有 DTO 注册表。
func OpenAPIBindings() []apicontract.TypedRoute {
	bindings := []apicontract.TypedRoute{
		{
			Method: http.MethodGet, Path: "/health",
			TypeBinding: apicontract.TypeBinding{Response: RuntimeStatus{}},
		},
		{
			Method: http.MethodGet, Path: "/ready",
			TypeBinding: apicontract.TypeBinding{Response: RuntimeStatus{}},
		},
	}
	return append(bindings, handler.OpenAPIBindings()...)
}
