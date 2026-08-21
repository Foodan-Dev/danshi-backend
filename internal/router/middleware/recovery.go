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
// 必须挂在**最外层**：它要能兜住后面所有中间件（包括 UoW 的提交）里的 panic。
func Recovery(log *slog.Logger) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		defer func() {
			if r := recover(); r != nil {
				log.ErrorContext(ctx, "请求处理 panic",
					slog.Any("panic", r),
					slog.String("path", string(c.Path())),
					slog.String("stack", string(debug.Stack())),
				)
				// panic 的细节只进日志。响应体固定文案，不泄露内部结构。
				status, body := envelope.FromError(apierr.Internal(fmt.Errorf("panic: %v", r)))
				c.AbortWithStatusJSON(status, body)
			}
		}()
		c.Next(ctx)
	}
}
