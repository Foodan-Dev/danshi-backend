package obs

import "context"

type requestIDContextKey struct{}

// ContextWithRequestID 把请求 ID 放进标准 context，供所有下游结构化日志自动关联。
func ContextWithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDContextKey{}, requestID)
}

func requestIDFromContext(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}
