package middleware

import (
	"bytes"
	"context"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/Foodan-Dev/danshi-backend/internal/httpx"
)

// LimitInFlight 对精确匹配的方法和路径施加无等待的在途请求上限。
// 调用方必须把它挂在受保护资源的获取中间件之前。
func LimitInFlight(
	method string,
	path string,
	maxInFlight int,
	retryAfterSeconds int,
	busyError error,
	onReject ...func(context.Context),
) app.HandlerFunc {
	return LimitInFlightPaths(method, []string{path}, maxInFlight, retryAfterSeconds, busyError, onReject...)
}

// LimitInFlightPaths 对精确匹配的方法和多个路径共享一个无等待的在途请求上限。
// 调用方必须把它挂在受保护资源的获取中间件之前。
func LimitInFlightPaths(
	method string,
	paths []string,
	maxInFlight int,
	retryAfterSeconds int,
	busyError error,
	onReject ...func(context.Context),
) app.HandlerFunc {
	methodBytes := []byte(method)
	allowedPaths := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		allowedPaths[path] = struct{}{}
	}
	retryAfter := strconv.Itoa(retryAfterSeconds)
	permits := make(chan struct{}, maxInFlight)

	return func(ctx context.Context, c *app.RequestContext) {
		if !bytes.Equal(c.Method(), methodBytes) {
			c.Next(ctx)
			return
		}
		if _, ok := allowedPaths[string(c.Path())]; !ok {
			c.Next(ctx)
			return
		}

		select {
		case permits <- struct{}{}:
			defer func() { <-permits }()
			c.Next(ctx)
		default:
			for _, observe := range onReject {
				if observe != nil {
					observe(ctx)
				}
			}
			c.Header("Retry-After", retryAfter)
			httpx.Fail(ctx, c, busyError)
		}
	}
}
