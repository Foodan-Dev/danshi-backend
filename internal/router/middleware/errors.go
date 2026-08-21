package middleware

import (
	"context"
	"log/slog"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/envelope"
)

// Fail 是处理器上报错误的唯一入口。
// 处理器不自己写响应体——统一由 ErrorHandler 渲染，保证错误契约只有一处实现。
func Fail(_ context.Context, c *app.RequestContext, err error) {
	c.Set(errCtxKey, err)
	c.Abort()
}

const errCtxKey = "danshi.error"

// ErrorHandler 把处理器上报的错误渲染成统一响应体。
// 挂在 Recovery 之内、UoW 之外——这样 UoW 能看到 abort 状态并据此回滚。
func ErrorHandler(log *slog.Logger) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		c.Next(ctx)

		raw, ok := c.Get(errCtxKey)
		if !ok || raw == nil {
			return
		}
		err, _ := raw.(error)
		if err == nil {
			return
		}
		e := apierr.As(err)

		// 5xx 才记 error 级别；4xx 是客户端问题，记 info 即可，否则日志会被刷爆
		attrs := []any{
			slog.Int("status", e.Status),
			slog.String("path", string(c.Path())),
			slog.String("method", string(c.Method())),
		}
		if e.Status >= 500 {
			log.ErrorContext(ctx, e.Error(), attrs...)
		} else {
			log.InfoContext(ctx, e.Message, attrs...)
		}

		status, body := envelope.FromError(e)
		c.JSON(status, body)
	}
}

// HasError 供 UoW 判断本次请求是否应回滚。
func HasError(c *app.RequestContext) bool {
	raw, ok := c.Get(errCtxKey)
	return ok && raw != nil
}
