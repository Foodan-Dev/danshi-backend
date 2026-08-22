package router

import (
	"github.com/cloudwego/hertz/pkg/route"

	"github.com/jingyijun/danshi_backend_go/internal/handler"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

func registerConfig(api *route.RouterGroup, _ Deps) {
	configHandler := handler.NewConfig(service.NewConfigService())
	api.GET("/config", configHandler.Get)
}
