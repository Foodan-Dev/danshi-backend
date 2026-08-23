// Package obs 是可观测性：结构化日志、指标、链路追踪。
package obs

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel/trace"

	"github.com/jingyijun/danshi_backend_go/internal/config"
)

// NewLogger 建结构化日志器。
//
// 生产用 JSON（便于采集），开发用文本（便于人读）。
// 一律输出到 stdout——容器环境里日志归集是编排层的事，进程不该自己写文件。
func NewLogger(cfg config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(cfg.LogLevel)}

	var h slog.Handler
	if cfg.IsProd() {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	h = correlationHandler{Handler: h}
	return slog.New(h).With(
		slog.String("service", "danshi-server"),
		slog.String("profile", string(cfg.Profile)),
	)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// correlationHandler 从标准 context 自动补充 request_id、trace_id 与 span_id。
// 没有启用 tracing 时不会产生空字段，也不会创建 span。
type correlationHandler struct {
	slog.Handler
}

func (h correlationHandler) Handle(ctx context.Context, record slog.Record) error {
	if requestID := requestIDFromContext(ctx); requestID != "" {
		record.AddAttrs(slog.String("request_id", requestID))
	}
	spanContext := trace.SpanContextFromContext(ctx)
	if spanContext.IsValid() {
		record.AddAttrs(
			slog.String("trace_id", spanContext.TraceID().String()),
			slog.String("span_id", spanContext.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, record)
}

func (h correlationHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return correlationHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h correlationHandler) WithGroup(name string) slog.Handler {
	return correlationHandler{Handler: h.Handler.WithGroup(name)}
}
