package obs

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestExternalCallSpanUsesActiveProviderAndSanitizesFailure(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	ctx, parent := provider.Tracer("test-parent").Start(context.Background(), "request")

	ctx, span := StartExternalCall(ctx, "test-instrumentation", "tencent_ci", "ReviewText")
	require.True(t, trace.SpanFromContext(ctx).SpanContext().IsValid())
	EndExternalCall(span, errors.New("secret provider response must not enter trace"))
	parent.End()

	spans := recorder.Ended()
	require.Len(t, spans, 2)
	child := spans[0]
	require.Equal(t, "EXTERNAL tencent_ci ReviewText", child.Name())
	require.Equal(t, trace.SpanKindClient, child.SpanKind())
	require.Equal(t, spans[1].SpanContext().SpanID(), child.Parent().SpanID())
	require.Equal(t, externalCallFailure, child.Status().Description)
	require.Empty(t, child.Events(), "供应商原始错误不得作为 exception event 进入 trace")
	require.Equal(t, map[string]string{
		"external.operation": "ReviewText",
		"external.system":    "tencent_ci",
	}, stringAttributes(child.Attributes()))
}

func TestExternalCallWithoutActiveTracingIsNoop(t *testing.T) {
	ctx, span := StartExternalCall(
		context.Background(), "test-instrumentation", "tencent_cos", "HeadObject",
	)
	require.False(t, trace.SpanFromContext(ctx).IsRecording())
	EndExternalCall(span, nil)
}

func stringAttributes(attributes []attribute.KeyValue) map[string]string {
	values := make(map[string]string, len(attributes))
	for _, item := range attributes {
		values[string(item.Key)] = item.Value.AsString()
	}
	return values
}
