package middleware

import (
	"context"
	"log/slog"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/httpx"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/envelope"
)

// ErrorHandler 把处理器上报的错误渲染成统一响应体。
// 挂在 Recovery 之内、UoW 之外——这样 UoW 能看到 abort 状态并据此回滚。
func ErrorHandler(log *slog.Logger) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		c.Next(ctx)

		reported, err := httpx.ReportedError(c)
		if !reported || err == nil {
			return
		}
		e := apierr.As(err)

		// 5xx 才记 error 级别；4xx 是客户端问题，记 info 即可，否则日志会被刷爆
		attrs := []any{
			slog.Int("status", e.Status),
			slog.String("error_code", string(e.Code)),
			slog.String("path", string(c.Path())),
			slog.String("method", string(c.Method())),
		}
		// error_id 是用户报障时能提供的唯一线索，必须同时出现在日志和响应体里。
		if e.ErrorID != "" {
			attrs = append(attrs, slog.String("error_id", e.ErrorID))
		}
		if e.Cause() != nil {
			attrs = append(attrs, slog.Any("cause", e.Cause()))
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
