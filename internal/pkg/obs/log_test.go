package obs

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/jingyijun/danshi_backend_go/internal/config"
)

func TestNewLoggerUsesProfileFormatAndAttributes(t *testing.T) {
	t.Run("prod JSON", func(t *testing.T) {
		output := captureLoggerOutput(t, config.Config{Profile: config.ProfileProd, LogLevel: "info"}, func(log *slog.Logger) {
			log.Info("prod-message")
		})

		var entry map[string]any
		if err := json.Unmarshal([]byte(output), &entry); err != nil {
			t.Fatalf("prod 日志应为 JSON，实际输出 %q: %v", output, err)
		}
		if entry["service"] != "danshi-server" {
			t.Fatalf("prod 日志缺少 service 属性: %v", entry)
		}
		if entry["profile"] != string(config.ProfileProd) {
			t.Fatalf("prod 日志缺少当前 profile 属性: %v", entry)
		}
	})

	t.Run("dev text", func(t *testing.T) {
		output := captureLoggerOutput(t, config.Config{Profile: config.ProfileDev, LogLevel: "info"}, func(log *slog.Logger) {
			log.Info("dev-message")
		})

		if json.Valid([]byte(output)) {
			t.Fatalf("dev 日志应为文本而不是 JSON，实际输出 %q", output)
		}
		if !strings.Contains(output, "service=danshi-server") {
			t.Fatalf("dev 日志缺少 service 属性: %q", output)
		}
		if !strings.Contains(output, "profile=dev") {
			t.Fatalf("dev 日志缺少当前 profile 属性: %q", output)
		}
	})
}

func TestNewLoggerFiltersConfiguredLevel(t *testing.T) {
	output := captureLoggerOutput(t, config.Config{Profile: config.ProfileProd, LogLevel: "warn"}, func(log *slog.Logger) {
		log.Info("must-not-be-emitted")
		log.Warn("must-be-emitted")
	})

	if strings.Contains(output, "must-not-be-emitted") {
		t.Fatalf("LOG_LEVEL=warn 时不应输出 info 日志: %q", output)
	}
	if !strings.Contains(output, "must-be-emitted") {
		t.Fatalf("LOG_LEVEL=warn 时应输出 warn 日志: %q", output)
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 1 {
		t.Fatalf("级别过滤后应只剩一条日志，实际为 %d 条: %q", len(lines), output)
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("warn 日志不是合法 JSON: %v", err)
	}
	if entry["level"] != "WARN" {
		t.Fatalf("输出日志级别应为 WARN，实际为 %v", entry["level"])
	}
}

func TestLoggerAddsRequestAndTraceCorrelation(t *testing.T) {
	traceID, err := trace.TraceIDFromHex("00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("构造 trace ID: %v", err)
	}
	spanID, err := trace.SpanIDFromHex("0011223344556677")
	if err != nil {
		t.Fatalf("构造 span ID: %v", err)
	}
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID})
	ctx := trace.ContextWithSpanContext(
		ContextWithRequestID(context.Background(), "request-42"),
		spanContext,
	)

	output := captureLoggerOutput(t, config.Config{Profile: config.ProfileProd, LogLevel: "info"}, func(log *slog.Logger) {
		log.InfoContext(ctx, "correlated-message")
	})
	var entry map[string]any
	if err := json.Unmarshal([]byte(output), &entry); err != nil {
		t.Fatalf("关联日志不是合法 JSON: %v", err)
	}
	if entry["request_id"] != "request-42" {
		t.Errorf("request_id = %v", entry["request_id"])
	}
	if entry["trace_id"] != traceID.String() {
		t.Errorf("trace_id = %v", entry["trace_id"])
	}
	if entry["span_id"] != spanID.String() {
		t.Errorf("span_id = %v", entry["span_id"])
	}
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  slog.Level
	}{
		{name: "debug", input: "debug", want: slog.LevelDebug},
		{name: "info", input: "info", want: slog.LevelInfo},
		{name: "warn", input: "warn", want: slog.LevelWarn},
		{name: "warning", input: "warning", want: slog.LevelWarn},
		{name: "error", input: "error", want: slog.LevelError},
		{name: "mixed case", input: "WaRnInG", want: slog.LevelWarn},
		{name: "surrounding whitespace", input: "  error\t", want: slog.LevelError},
		{name: "empty", input: "", want: slog.LevelInfo},
		{name: "unknown", input: "trace", want: slog.LevelInfo},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parseLevel(test.input); got != test.want {
				t.Fatalf("parseLevel(%q) = %s，期望 %s", test.input, got, test.want)
			}
		})
	}
}

func captureLoggerOutput(t *testing.T, cfg config.Config, emit func(*slog.Logger)) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("创建 stdout 管道失败: %v", err)
	}
	originalStdout := os.Stdout
	os.Stdout = writer
	defer func() {
		os.Stdout = originalStdout
	}()

	emit(NewLogger(cfg))
	closeWriterErr := writer.Close()
	os.Stdout = originalStdout
	output, readErr := io.ReadAll(reader)
	closeReaderErr := reader.Close()
	if closeWriterErr != nil {
		t.Fatalf("关闭 stdout 写端失败: %v", closeWriterErr)
	}
	if readErr != nil {
		t.Fatalf("读取日志输出失败: %v", readErr)
	}
	if closeReaderErr != nil {
		t.Fatalf("关闭 stdout 读端失败: %v", closeReaderErr)
	}
	return string(output)
}
