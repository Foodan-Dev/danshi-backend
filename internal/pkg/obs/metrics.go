package obs

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
)

// RegisterMetrics 挂载 /metrics。
//
// 按 D8：应用**直出** Prometheus 指标（拉模型），trace 走 OTLP 推到 Collector。
// 两者分开是有意的——指标要能在 Collector 挂掉时仍然可查。
//
// TODO(P1.7)：接入 prometheus client + otel meter provider。
// 当前先占住路由，让编排层的抓取配置可以提前就位而不至于 404。
func RegisterMetrics(h *server.Hertz) {
	h.GET("/metrics", func(_ context.Context, c *app.RequestContext) {
		c.String(200, "# danshi metrics endpoint\n# TODO(P1.7): prometheus exporter\n")
	})
}
