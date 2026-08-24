package obs

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const externalCallFailure = "external call failed"

// StartExternalCall 从当前请求 span 的 provider 创建外部依赖 client span。
// system 与 operation 必须是代码中固定的低基数值，不能传 URL、对象键或用户输入。
func StartExternalCall(
	ctx context.Context,
	instrumentation string,
	system string,
	operation string,
) (context.Context, trace.Span) {
	parent := trace.SpanFromContext(ctx)
	if !parent.SpanContext().IsValid() {
		return ctx, parent
	}
	provider := parent.TracerProvider()
	return provider.Tracer(instrumentation).Start(
		ctx,
		"EXTERNAL "+system+" "+operation,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("external.system", system),
			attribute.String("external.operation", operation),
		),
	)
}

// EndExternalCall 只记录脱敏的失败状态，不把供应商错误正文写入 trace。
func EndExternalCall(span trace.Span, err error) {
	if err != nil {
		span.SetStatus(codes.Error, externalCallFailure)
	}
	span.End()
}
