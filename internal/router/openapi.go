package router

import (
	"net/http"

	"github.com/Foodan-Dev/danshi-backend/internal/apicontract"
	"github.com/Foodan-Dev/danshi-backend/internal/handler"
)

// OpenAPIBindings 合并 router 自有的探针类型与 handler 私有 DTO 注册表。
func OpenAPIBindings() []apicontract.TypedRoute {
	bindings := []apicontract.TypedRoute{
		{
			Method: http.MethodGet, Path: "/health",
			TypeBinding: apicontract.TypeBinding{
				Query: apicontract.NoQuery{}, Response: RuntimeStatus{},
			},
		},
		{
			Method: http.MethodGet, Path: "/ready",
			TypeBinding: apicontract.TypeBinding{
				Query: apicontract.NoQuery{}, Response: RuntimeStatus{},
			},
		},
	}
	return append(bindings, handler.OpenAPIBindings()...)
}
