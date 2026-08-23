package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/jingyijun/danshi_backend_go/internal/pkg/obs"
)

// 请求 ID 的 HTTP 头名与内部上下文键。
const (
	HeaderRequestID = "X-Request-Id"
	ctxRequestID    = "danshi.request_id"
)

// RequestID 给每个请求分配一个 id，透传到日志、trace 与响应头。
// 用户报障时报这个 id，就能直接定位到那一次请求的全部日志。
func RequestID() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		id := string(c.GetHeader(HeaderRequestID))
		if id == "" {
			id = newID()
		}
		c.Set(ctxRequestID, id)
		c.Header(HeaderRequestID, id)
		c.Next(obs.ContextWithRequestID(ctx, id))
	}
}

// RequestIDFrom 返回当前请求的请求 ID。
func RequestIDFrom(c *app.RequestContext) string {
	if v, ok := c.Get(ctxRequestID); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func newID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b)
}
