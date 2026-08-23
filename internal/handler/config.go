package handler

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/Foodan-Dev/danshi-backend/internal/pkg/envelope"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

// Config 处理公共配置请求。
type Config struct {
	service *service.ConfigService
}

// NewConfig 创建配置 handler。
func NewConfig(configService *service.ConfigService) *Config { return &Config{service: configService} }

// Get 返回当前启用的后端词表真源。
func (h *Config) Get(ctx context.Context, c *app.RequestContext) {
	result, err := h.service.Get(ctx)
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("请求成功", result))
}
