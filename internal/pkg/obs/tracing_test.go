package obs

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	hertzconfig "github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestEmptyOTLPEndpointCompletelyDisablesTracing(t *testing.T) {
	factoryCalls := 0
	tracing, err := newTracing(context.Background(), " \t\n", func(context.Context, string) (sdktrace.SpanExporter, error) {
		factoryCalls++
		return nil, nil
	})
	if err != nil {
		t.Fatalf("空 endpoint 不应报错: %v", err)
	}
	if tracing.Enabled() {
		t.Fatal("空 OTLP_ENDPOINT 必须完全禁用 tracing")
	}
	if tracing.TracerProvider() != nil {
		t.Fatal("禁用 tracing 时不应返回 provider")
	}
	if factoryCalls != 0 {
		t.Fatalf("空 OTLP_ENDPOINT 调用了 exporter factory %d 次，可能产生连接尝试", factoryCalls)
	}
	if err := tracing.Shutdown(context.Background()); err != nil {
		t.Fatalf("关闭禁用的 tracing: %v", err)
	}
}

func TestTracingSpanUsesRouteTemplate(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("关闭测试 trace provider: %v", err)
		}
	})
	tracing := &Tracing{
		provider: provider,
		propagator: propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		),
	}

	h := server.New(hertzconfig.Option{F: func(*hertzconfig.Options) {}})
	h.Use(tracing.Middleware())
	h.GET("/api/v2/posts/:post_id", func(_ context.Context, c *app.RequestContext) {
		c.Header(requestIDHeader, "request-123")
		c.String(http.StatusOK, "ok")
	})
	response := ut.PerformRequest(h.Engine, http.MethodGet, "/api/v2/posts/123", nil).Result()
	if response.StatusCode() != http.StatusOK {
		t.Fatalf("请求状态码 = %d", response.StatusCode())
	}

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("结束的 server span 数 = %d，期望 1", len(spans))
	}
	span := spans[0]
	if span.Name() != "GET /api/v2/posts/:post_id" {
		t.Fatalf("span 名 = %q，必须使用路由模板", span.Name())
	}
	if strings.Contains(span.Name(), "123") {
		t.Fatalf("span 名包含实际资源 ID: %q", span.Name())
	}
	if span.SpanKind() != trace.SpanKindServer {
		t.Fatalf("span kind = %s，期望 server", span.SpanKind())
	}
	attrs := spanAttributes(span.Attributes())
	if attrs["http.route"] != "/api/v2/posts/:post_id" {
		t.Fatalf("http.route = %v，必须使用路由模板", attrs["http.route"])
	}
	if attrs["request.id"] != "request-123" {
		t.Fatalf("request.id = %v，期望 request-123", attrs["request.id"])
	}
}

func TestOTLPEndpointValidationPreventsDefaultFallback(t *testing.T) {
	for _, endpoint := range []string{
		"collector:4317",
		"http://collector:4318/v1/traces",
		"https://collector.example.com:4318/v1/traces",
	} {
		if _, err := otlpHTTPOptions(endpoint); err != nil {
			t.Errorf("合法 endpoint %q 被拒绝: %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{
		"collector",
		"ftp://collector:4317",
		"http://user:password@collector:4317",
	} {
		if _, err := otlpHTTPOptions(endpoint); err == nil {
			t.Errorf("非法 endpoint %q 应被拒绝，不能静默回落到默认地址", endpoint)
		}
	}
}

func spanAttributes(attrs []attribute.KeyValue) map[string]any {
	result := make(map[string]any, len(attrs))
	for _, attr := range attrs {
		result[string(attr.Key)] = attr.Value.AsInterface()
	}
	return result
}
