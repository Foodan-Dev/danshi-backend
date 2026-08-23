// Package middleware 是请求链路上的横切关注点。
//
// 挂载顺序很重要，见 router/register.go 的说明。
package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/envelope"
)

// Recovery 兜住 panic 并转成 500。
// 必须挂在所有会改变请求行为的中间件之外：它要能兜住后续中间件
// （包括 UoW 的提交）里的 panic。只读的 metrics/tracing wrapper 可以在它外层观测最终 500。
func Recovery(log *slog.Logger) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		defer func() {
			if r := recover(); r != nil {
				// 先建错误再打日志：error_id 要同时出现在日志和响应体里，
				// 用户报这个 id 就能定位到下面这条 stack。
				e := apierr.Internal(fmt.Errorf("panic: %v", r))
				attrs := []any{
					slog.Any("panic", r),
					slog.String("error_id", e.ErrorID),
					slog.String("path", string(c.Path())),
					slog.String("stack", string(debug.Stack())),
				}
				if requestID := RequestIDFrom(c); requestID != "" {
					attrs = append(attrs, slog.String("request_id", requestID))
				}
				log.ErrorContext(ctx, "请求处理 panic", attrs...)
				// panic 的细节只进日志。响应体固定文案，不泄露内部结构。
				status, body := envelope.FromError(e)
				c.AbortWithStatusJSON(status, body)
			}
		}()
		c.Next(ctx)
	}
}
