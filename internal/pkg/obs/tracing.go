package obs

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	serviceName         = "danshi-server"
	instrumentationName = "github.com/jingyijun/danshi_backend_go/internal/pkg/obs"
	requestIDHeader     = "X-Request-Id"
)

type spanExporterFactory func(context.Context, string) (sdktrace.SpanExporter, error)

// Tracing 是独立于 Prometheus 指标的 OTLP trace 管线。provider 为 nil 表示完全禁用。
type Tracing struct {
	provider   *sdktrace.TracerProvider
	propagator propagation.TextMapPropagator
}

// NewTracing 仅在 endpoint 非空时创建 OTLP exporter 与后台 batch processor。
// 空字符串会在任何 exporter、连接或 goroutine 创建之前直接返回禁用状态。
func NewTracing(ctx context.Context, endpoint string) (*Tracing, error) {
	return newTracing(ctx, endpoint, newOTLPExporter)
}

func newTracing(ctx context.Context, endpoint string, factory spanExporterFactory) (*Tracing, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return &Tracing{}, nil
	}

	exporter, err := factory(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("初始化 OTLP trace exporter 失败: %w", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
		)),
	)
	return &Tracing{
		provider: provider,
		propagator: propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		),
	}, nil
}

// Enabled 报告 trace exporter、provider 与 HTTP/DB instrumentation 是否应启用。
func (t *Tracing) Enabled() bool {
	return t != nil && t.provider != nil
}

// TracerProvider 返回给 HTTP 中间件和 GORM 插件共享的 provider；禁用时返回 nil。
func (t *Tracing) TracerProvider() trace.TracerProvider {
	if !t.Enabled() {
		return nil
	}
	return t.provider
}

// Shutdown 刷新剩余 span 并关闭 exporter。禁用状态下是无副作用操作。
func (t *Tracing) Shutdown(ctx context.Context) error {
	if !t.Enabled() {
		return nil
	}
	return t.provider.Shutdown(ctx)
}

// Middleware 从 W3C traceparent 提取父上下文，并以路由模板命名 server span。
// 实际 URL path 不写入 span 名或 http.route，避免资源 ID 形成高基数名称。
func (t *Tracing) Middleware() app.HandlerFunc {
	if !t.Enabled() {
		return func(ctx context.Context, c *app.RequestContext) {
			c.Next(ctx)
		}
	}

	tracer := t.provider.Tracer(instrumentationName)
	return func(ctx context.Context, c *app.RequestContext) {
		route := routeTemplate(c)
		method := boundedMethod(string(c.Method()))
		ctx = t.propagator.Extract(ctx, requestHeaderCarrier{header: &c.Request.Header})
		ctx, span := tracer.Start(
			ctx,
			spanName(method, route),
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				semconv.HTTPRoute(route),
				semconv.HTTPRequestMethodKey.String(method),
			),
		)
		defer span.End()

		c.Next(ctx)

		status := c.Response.StatusCode()
		span.SetAttributes(
			semconv.HTTPResponseStatusCode(status),
			attribute.Int("http.response.body.size", len(c.Response.Body())),
		)
		if requestID := string(c.Response.Header.Peek(requestIDHeader)); requestID != "" {
			span.SetAttributes(attribute.String("request.id", requestID))
		}
		if status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(status))
		}
	}
}

func newOTLPExporter(ctx context.Context, endpoint string) (sdktrace.SpanExporter, error) {
	options, err := otlpHTTPOptions(endpoint)
	if err != nil {
		return nil, err
	}
	return otlptracehttp.New(ctx, options...)
}

func otlpHTTPOptions(endpoint string) ([]otlptracehttp.Option, error) {
	if strings.Contains(endpoint, "://") {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Host == "" {
			return nil, fmt.Errorf("OTLP_ENDPOINT 不是有效 URL: %q", endpoint)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return nil, fmt.Errorf("OTLP_ENDPOINT 仅支持 http 或 https scheme: %q", parsed.Scheme)
		}
		if parsed.User != nil {
			return nil, fmt.Errorf("OTLP_ENDPOINT 不允许包含用户信息")
		}
		return []otlptracehttp.Option{otlptracehttp.WithEndpointURL(endpoint)}, nil
	}

	if _, _, err := net.SplitHostPort(endpoint); err != nil {
		return nil, fmt.Errorf("OTLP_ENDPOINT 应为 host:port 或 http(s) URL: %w", err)
	}
	return []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure(),
	}, nil
}

func spanName(method, route string) string {
	return method + " " + route
}

type requestHeaderCarrier struct {
	header *protocol.RequestHeader
}

func (c requestHeaderCarrier) Get(key string) string {
	return string(c.header.Peek(key))
}

func (c requestHeaderCarrier) Set(key, value string) {
	c.header.Set(key, value)
}

func (c requestHeaderCarrier) Keys() []string {
	keys := make([]string, 0, c.header.Len())
	c.header.VisitAll(func(key, _ []byte) {
		keys = append(keys, string(key))
	})
	return keys
}
