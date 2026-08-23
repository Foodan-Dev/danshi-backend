package router

import (
	"github.com/cloudwego/hertz/pkg/route"

	"github.com/Foodan-Dev/danshi-backend/internal/handler"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

func registerConfig(api *route.RouterGroup, _ Deps) {
	configHandler := handler.NewConfig(service.NewConfigService())
	api.GET("/config", configHandler.Get)
}
