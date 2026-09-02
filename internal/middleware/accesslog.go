package middleware

import (
	"context"
	"log/slog"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
)

// AccessLog 为每次请求记录一条结构化访问日志。
// 探针和指标端点由平台高频访问，跳过它们可以避免淹没业务请求日志。
func AccessLog(log *slog.Logger, skipPaths ...string) app.HandlerFunc {
	skipped := make(map[string]struct{}, len(skipPaths))
	for _, path := range skipPaths {
		skipped[path] = struct{}{}
	}

	return func(ctx context.Context, c *app.RequestContext) {
		if _, ok := skipped[string(c.Request.Path())]; ok {
			c.Next(ctx)
			return
		}

		started := time.Now()
		c.Next(ctx)

		durationMS := float64(time.Since(started).Round(time.Microsecond)) / float64(time.Millisecond)
		attrs := []any{
			slog.String("method", string(c.Request.Method())),
			slog.String("path", string(c.Request.Path())),
		}
		if route := c.FullPath(); route != "" {
			attrs = append(attrs, slog.String("route", route))
		}
		attrs = append(attrs,
			slog.Int("status", c.Response.StatusCode()),
			slog.Float64("duration_ms", durationMS),
			slog.Int("bytes", len(c.Response.Body())),
			slog.String("ip", c.ClientIP()),
			slog.String("request_id", RequestIDFrom(c)),
		)
		log.InfoContext(ctx, "http 请求", attrs...)
	}
}
