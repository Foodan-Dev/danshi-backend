package router_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/envelope"
	"github.com/jingyijun/danshi_backend_go/internal/router/middleware"
)

// 用 ut 直接驱动路由，不占端口、不连数据库。
// 这里验证的是**错误契约**：404 / 405 / panic / 业务错误都必须走同一个 envelope，
// 前端才不会遇到「有的错误是 JSON、有的是纯文本」。
func newTestEngine(t *testing.T) *server.Hertz {
	t.Helper()
	h := server.New(
		server.WithHandleMethodNotAllowed(true),
		config.Option{F: func(_ *config.Options) {}},
	)
	return h
}

func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("响应不是 JSON: %s", body)
	}
	return m
}

func TestErrorHandlerRendersEnvelope(t *testing.T) {
	h := newTestEngine(t)
	h.Use(middleware.ErrorHandler(discardLogger()))
	h.GET("/boom", func(ctx context.Context, c *app.RequestContext) {
		middleware.Fail(ctx, c, apierr.Forbidden(apierr.BizNotOwner, "没有权限执行该操作"))
	})

	w := ut.PerformRequest(h.Engine, http.MethodGet, "/boom", nil)
	resp := w.Result()
	if resp.StatusCode() != consts.StatusForbidden {
		t.Fatalf("期望 403，实际 %d", resp.StatusCode())
	}
	m := decode(t, resp.Body())
	if m["code"] != float64(403) || m["message"] != "没有权限执行该操作" {
		t.Fatalf("错误体不符: %v", m)
	}
	if m["data"] != nil {
		t.Fatalf("非 422 的 data 必须是 null，实际 %v", m["data"])
	}
}

func TestValidationErrorCarriesFields(t *testing.T) {
	h := newTestEngine(t)
	h.Use(middleware.ErrorHandler(discardLogger()))
	h.GET("/v", func(ctx context.Context, c *app.RequestContext) {
		middleware.Fail(ctx, c, apierr.InvalidField("page", apierr.FieldOutOfRange, "page 不能小于 1"))
	})

	resp := ut.PerformRequest(h.Engine, http.MethodGet, "/v", nil).Result()
	if resp.StatusCode() != 422 {
		t.Fatalf("期望 422，实际 %d", resp.StatusCode())
	}
	m := decode(t, resp.Body())
	data, ok := m["data"].(map[string]any)
	if !ok {
		t.Fatalf("422 必须带 data.errors，实际 %v", m["data"])
	}
	errs, ok := data["errors"].([]any)
	if !ok || len(errs) != 1 {
		t.Fatalf("errors 形态不符: %v", data)
	}
	first := errs[0].(map[string]any)
	if first["field"] != "page" || first["code"] != "out_of_range" {
		t.Fatalf("字段错误不符: %v", first)
	}
	// 顶层 message 取第一条字段错误的文案
	if m["message"] != "page 不能小于 1" {
		t.Fatalf("顶层 message 应取第一条字段错误，实际 %v", m["message"])
	}
}

func TestRecoveryConvertsPanicTo500(t *testing.T) {
	h := newTestEngine(t)
	h.Use(middleware.Recovery(discardLogger()))
	h.GET("/panic", func(_ context.Context, _ *app.RequestContext) {
		panic("boom")
	})

	resp := ut.PerformRequest(h.Engine, http.MethodGet, "/panic", nil).Result()
	if resp.StatusCode() != 500 {
		t.Fatalf("期望 500，实际 %d", resp.StatusCode())
	}
	m := decode(t, resp.Body())
	// panic 细节只进日志，响应体固定文案
	if m["message"] != "服务器内部错误" {
		t.Fatalf("500 文案不应泄露内部信息: %v", m["message"])
	}
	if body := string(resp.Body()); contains(body, "boom") {
		t.Fatal("panic 内容泄露到了响应体")
	}
}

func TestEnvelopeOK(t *testing.T) {
	e := envelope.OK("", map[string]int{"n": 1})
	if e.Code != 200 || e.Message != "success" {
		t.Fatalf("空 message 应回落为 success: %+v", e)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}()
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
