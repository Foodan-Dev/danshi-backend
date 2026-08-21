package router

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"

	"github.com/jingyijun/danshi_backend_go/internal/pkg/envelope"
)

// RuntimeStatus 与 Python 侧 /health /ready 的响应形态保持一致。
type RuntimeStatus struct {
	Status string `json:"status"`
}

// registerProbes 挂载探针。
//
// 刻意**不挂在 /api/v2 下**，也不经过 UoW：
// 探针要在数据库不可用时仍能回答「进程还活着」，开事务就自相矛盾了。
func registerProbes(h *server.Hertz, d Deps) {
	// 存活探针：进程在跑就返回 200，不碰任何外部依赖
	h.GET("/health", func(_ context.Context, c *app.RequestContext) {
		c.JSON(200, envelope.OK("ok", RuntimeStatus{Status: "healthy"}))
	})

	// 就绪探针：数据库不通就返回 503，让编排层把流量摘走
	h.GET("/ready", func(ctx context.Context, c *app.RequestContext) {
		sqlDB, err := d.DB.DB.DB()
		if err == nil {
			err = sqlDB.PingContext(ctx)
		}
		if err != nil {
			d.Log.WarnContext(ctx, "就绪探针失败：数据库不可达")
			c.JSON(503, envelope.Envelope[RuntimeStatus]{
				Code: 503, Message: "依赖不可用", Data: RuntimeStatus{Status: "unready"},
			})
			return
		}
		c.JSON(200, envelope.OK("ok", RuntimeStatus{Status: "ready"}))
	})
}
