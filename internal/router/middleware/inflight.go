package middleware

import (
	"bytes"
	"context"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"
)

// LimitInFlight 对精确匹配的方法和路径施加无等待的在途请求上限。
// 调用方必须把它挂在受保护资源的获取中间件之前。
func LimitInFlight(
	method string,
	path string,
	maxInFlight int,
	retryAfterSeconds int,
	busyError error,
) app.HandlerFunc {
	methodBytes := []byte(method)
	pathBytes := []byte(path)
	retryAfter := strconv.Itoa(retryAfterSeconds)
	permits := make(chan struct{}, maxInFlight)

	return func(ctx context.Context, c *app.RequestContext) {
		if !bytes.Equal(c.Method(), methodBytes) || !bytes.Equal(c.Path(), pathBytes) {
			c.Next(ctx)
			return
		}

		select {
		case permits <- struct{}{}:
			defer func() { <-permits }()
			c.Next(ctx)
		default:
			c.Header("Retry-After", retryAfter)
			Fail(ctx, c, busyError)
		}
	}
}
