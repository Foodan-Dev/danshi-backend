package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	hertzconfig "github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/common/ut"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/httpx"
)

func TestAccessLogRecordsSuccessfulRequest(t *testing.T) {
	var output bytes.Buffer
	h := newAccessLogTestEngine()
	h.Use(RequestID())
	h.Use(AccessLog(slog.New(slog.NewJSONHandler(&output, nil))))
	h.GET("/posts/:post_id", func(_ context.Context, c *app.RequestContext) {
		c.String(http.StatusOK, "ok")
	})

	response := ut.PerformRequest(
		h.Engine,
		http.MethodGet,
		"/posts/123",
		nil,
		ut.Header{Key: HeaderRequestID, Value: "request-123"},
	).Result()
	if response.StatusCode() != http.StatusOK {
		t.Fatalf("响应状态码 = %d，期望 %d", response.StatusCode(), http.StatusOK)
	}

	record := decodeAccessLog(t, output.Bytes())
	assertAccessLogField(t, record, "msg", "http 请求")
	assertAccessLogField(t, record, "method", http.MethodGet)
	assertAccessLogField(t, record, "path", "/posts/123")
	assertAccessLogField(t, record, "route", "/posts/:post_id")
	assertAccessLogField(t, record, "status", float64(http.StatusOK))
	assertAccessLogField(t, record, "request_id", "request-123")
	assertAccessLogField(t, record, "bytes", float64(len("ok")))
	if _, ok := record["duration_ms"].(float64); !ok {
		t.Fatalf("duration_ms 应为数字，实际 %#v", record["duration_ms"])
	}
	if _, ok := record["ip"].(string); !ok {
		t.Fatalf("ip 应为字符串，实际 %#v", record["ip"])
	}
}

func TestAccessLogSkipsConfiguredPath(t *testing.T) {
	var output bytes.Buffer
	h := newAccessLogTestEngine()
	h.Use(AccessLog(slog.New(slog.NewJSONHandler(&output, nil)), "/health"))
	h.GET("/health", func(_ context.Context, c *app.RequestContext) {
		c.String(http.StatusOK, "ok")
	})

	_ = ut.PerformRequest(h.Engine, http.MethodGet, "/health", nil)
	if output.Len() != 0 {
		t.Fatalf("跳过路径不应写访问日志，实际输出 %q", output.String())
	}
}

func TestAccessLogRecordsStatusRenderedByErrorHandler(t *testing.T) {
	var output bytes.Buffer
	h := newAccessLogTestEngine()
	h.Use(RequestID())
	h.Use(AccessLog(slog.New(slog.NewJSONHandler(&output, nil))))
	h.Use(ErrorHandler(slog.New(slog.NewTextHandler(io.Discard, nil))))
	h.GET("/forbidden", func(ctx context.Context, c *app.RequestContext) {
		httpx.Fail(ctx, c, apierr.Forbidden(apierr.BizNotOwner, "没有权限执行该操作"))
	})

	response := ut.PerformRequest(h.Engine, http.MethodGet, "/forbidden", nil).Result()
	if response.StatusCode() != http.StatusForbidden {
		t.Fatalf("响应状态码 = %d，期望 %d", response.StatusCode(), http.StatusForbidden)
	}

	record := decodeAccessLog(t, output.Bytes())
	assertAccessLogField(t, record, "status", float64(http.StatusForbidden))
}

// panic 的请求同样要出现在访问日志里。挂载顺序必须让 AccessLog 在 Recovery
// 外层，否则 panic 展开栈时会跳过写日志那行，崩掉的请求就静默消失了——
// 而这恰恰是最需要在日志里看到的一类请求。
func TestAccessLogRecordsPanicRecoveredAs500(t *testing.T) {
	var output bytes.Buffer
	h := newAccessLogTestEngine()
	h.Use(AccessLog(slog.New(slog.NewJSONHandler(&output, nil))))
	h.Use(Recovery(slog.New(slog.NewTextHandler(io.Discard, nil))))
	h.Use(RequestID())
	h.GET("/boom", func(context.Context, *app.RequestContext) {
		panic("测试用 panic")
	})

	response := ut.PerformRequest(h.Engine, http.MethodGet, "/boom", nil).Result()
	if response.StatusCode() != http.StatusInternalServerError {
		t.Fatalf("响应状态码 = %d，期望 %d",
			response.StatusCode(), http.StatusInternalServerError)
	}

	record := decodeAccessLog(t, output.Bytes())
	assertAccessLogField(t, record, "path", "/boom")
	assertAccessLogField(t, record, "status", float64(http.StatusInternalServerError))
	if id, _ := record["request_id"].(string); id == "" {
		t.Fatalf("panic 请求的访问日志缺少 request_id: %#v", record)
	}
}

func newAccessLogTestEngine() *server.Hertz {
	return server.New(hertzconfig.Option{F: func(*hertzconfig.Options) {}})
}

func decodeAccessLog(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(data), &record); err != nil {
		t.Fatalf("访问日志不是单条 JSON 记录: %q: %v", data, err)
	}
	return record
}

func assertAccessLogField(t *testing.T, record map[string]any, field string, want any) {
	t.Helper()
	if got := record[field]; got != want {
		t.Fatalf("访问日志字段 %s = %#v，期望 %#v", field, got, want)
	}
}
