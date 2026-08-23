package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

func TestFeishuWebhookAlertDoesNotLeakUserContent(t *testing.T) {
	const userOriginal = "PRIVATE-USER-CONTENT-7f3c9a"

	alertType := reflect.TypeOf(service.ModerationAlert{})
	if _, exists := alertType.FieldByName("Text"); exists {
		t.Fatal("ModerationAlert 不得承载被审核的用户原文")
	}

	var postedBody []byte
	webhookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var err error
		postedBody, err = io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("读取 webhook 请求体失败: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer webhookServer.Close()

	// Field 与 ProviderJobID 也按不可信输入处理，用原文哨兵证明适配器只允许固定摘要字段出站。
	field := model.ModerationField(userOriginal)
	providerJobID := "job-" + userOriginal
	NewFeishuWebhook(webhookServer.URL, webhookServer.Client(), nil).Alert(context.Background(), service.ModerationAlert{
		Target:        service.ModerationTargetPost,
		TargetID:      42,
		Field:         &field,
		Provider:      model.ModerationProviderTencentCI,
		ProviderJobID: &providerJobID,
		Verdict:       model.ModerationVerdictBlock,
		Labels:        []string{"abuse", "spam"},
	})

	if strings.Contains(string(postedBody), userOriginal) {
		t.Fatalf("webhook 请求体泄露了用户原文: %s", postedBody)
	}

	var payload map[string]any
	if err := json.Unmarshal(postedBody, &payload); err != nil {
		t.Fatalf("webhook 请求体不是合法 JSON: %v", err)
	}
	if len(payload) != 2 {
		t.Fatalf("webhook 顶层只能包含 msg_type 与 content，实际为: %v", payload)
	}
	content, ok := payload["content"].(map[string]any)
	if !ok || len(content) != 1 {
		t.Fatalf("webhook content 只能包含 text，实际为: %v", payload["content"])
	}
	const expectedText = "旦食内容审核告警\n对象：post/42\n供应商：tencent_ci\n结论：block\n标签：[abuse spam]"
	if content["text"] != expectedText {
		t.Fatalf("webhook 文案必须严格限于审核摘要，实际为: %v", content["text"])
	}
}

func TestFeishuWebhookAlertPayloadShape(t *testing.T) {
	var postedMethod, contentType string
	var postedBody []byte
	webhookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		postedMethod = request.Method
		contentType = request.Header.Get("Content-Type")
		var err error
		postedBody, err = io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("读取 webhook 请求体失败: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer webhookServer.Close()

	NewFeishuWebhook(webhookServer.URL, webhookServer.Client(), nil).Alert(context.Background(), service.ModerationAlert{
		Target:   service.ModerationTargetComment,
		TargetID: 7,
		Provider: model.ModerationProviderTencentCI,
		Verdict:  model.ModerationVerdictReview,
		Labels:   []string{"suspected"},
	})

	if postedMethod != http.MethodPost {
		t.Fatalf("webhook 方法应为 POST，实际为 %q", postedMethod)
	}
	if contentType != "application/json" {
		t.Fatalf("webhook Content-Type 应为 application/json，实际为 %q", contentType)
	}
	var payload struct {
		MsgType string `json:"msg_type"`
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(postedBody, &payload); err != nil {
		t.Fatalf("webhook 请求体不是合法 JSON: %v", err)
	}
	if payload.MsgType != "text" {
		t.Fatalf("msg_type 应为 text，实际为 %q", payload.MsgType)
	}
	if payload.Content.Text == "" {
		t.Fatal("content.text 不得为空")
	}
}

func TestFeishuWebhookAlertHTTPFailuresAreFailOpen(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			webhookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte("webhook failure"))
			}))
			defer webhookServer.Close()

			logger, logs := bufferedLogger()
			alerter := NewFeishuWebhook(webhookServer.URL, webhookServer.Client(), logger)
			callAlertWithoutPanic(context.Background(), t, alerter)
			if logs.Len() == 0 {
				t.Fatalf("HTTP %d 失败应被记录", status)
			}
		})
	}
}

func TestFeishuWebhookAlertNetworkFailureIsFailOpen(t *testing.T) {
	webhookServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := webhookServer.URL
	client := webhookServer.Client()
	webhookServer.Close()

	logger, logs := bufferedLogger()
	callAlertWithoutPanic(context.Background(), t, NewFeishuWebhook(url, client, logger))
	if logs.Len() == 0 {
		t.Fatal("网络不可达应被记录")
	}
}

func TestFeishuWebhookAlertInvalidURLIsFailOpen(t *testing.T) {
	logger, logs := bufferedLogger()
	callAlertWithoutPanic(context.Background(), t, NewFeishuWebhook("://invalid webhook URL", nil, logger))
	if logs.Len() == 0 {
		t.Fatal("非法 URL 应被记录")
	}
}

func TestFeishuWebhookAlertCanceledContextIsFailOpen(t *testing.T) {
	var handlerCalled atomic.Bool
	webhookServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		handlerCalled.Store(true)
	}))
	defer webhookServer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	logger, logs := bufferedLogger()
	callAlertWithoutPanic(ctx, t, NewFeishuWebhook(webhookServer.URL, webhookServer.Client(), logger))
	if logs.Len() == 0 {
		t.Fatal("context 已取消应被记录")
	}
	if handlerCalled.Load() {
		t.Fatal("context 已取消时不应抵达 webhook handler")
	}
}

func TestFeishuWebhookAlertNilLoggerIsFailOpen(t *testing.T) {
	callAlertWithoutPanic(context.Background(), t, NewFeishuWebhook("://invalid webhook URL", nil, nil))
}

func TestNewFeishuWebhookClientTimeout(t *testing.T) {
	alerter := NewFeishuWebhook("http://example.invalid", nil, nil)
	if alerter.client == nil {
		t.Fatal("nil client 应创建默认 HTTP 客户端")
	}
	if alerter.client.Timeout != webhookTimeout {
		t.Fatalf("默认 HTTP 客户端超时应使用 webhookTimeout，实际为 %s", alerter.client.Timeout)
	}
	if alerter.client.Timeout != 5*time.Second {
		t.Fatalf("默认 HTTP 客户端超时应为 5 秒，实际为 %s", alerter.client.Timeout)
	}

	requestStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	var startedOnce, releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseHandler) })
	}
	webhookServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		startedOnce.Do(func() { close(requestStarted) })
		<-releaseHandler
	}))
	defer webhookServer.Close()
	defer release()

	const shortTimeout = 40 * time.Millisecond
	logger, logs := bufferedLogger()
	shortClient := &http.Client{Timeout: shortTimeout}
	startedAt := time.Now()
	callAlertWithoutPanic(context.Background(), t, NewFeishuWebhook(webhookServer.URL, shortClient, logger))
	elapsed := time.Since(startedAt)
	release()

	select {
	case <-requestStarted:
	default:
		t.Fatal("超时测试请求没有抵达故意不响应的 server")
	}
	if elapsed < shortTimeout {
		t.Fatalf("请求在客户端超时生效前就返回了，耗时 %s", elapsed)
	}
	if elapsed >= time.Second {
		t.Fatalf("自定义客户端超时未及时终止请求，耗时 %s", elapsed)
	}
	if logs.Len() == 0 {
		t.Fatal("客户端超时应被记录")
	}
}

func TestFeishuWebhookAlertDrainsAndClosesResponseBody(t *testing.T) {
	body := &trackingResponseBody{Reader: strings.NewReader("response body that must be drained")}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       body,
			Header:     make(http.Header),
			Request:    request,
		}, nil
	})}

	NewFeishuWebhook("http://webhook.test", client, nil).Alert(context.Background(), service.ModerationAlert{})
	if body.Len() != 0 {
		t.Fatalf("响应体没有被读干净，剩余 %d 字节", body.Len())
	}
	if !body.closed {
		t.Fatal("响应体没有被关闭")
	}
}

func bufferedLogger() (*slog.Logger, *bytes.Buffer) {
	var logs bytes.Buffer
	return slog.New(slog.NewTextHandler(&logs, nil)), &logs
}

func callAlertWithoutPanic(ctx context.Context, t *testing.T, alerter *FeishuWebhook) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("告警失败不得 panic: %v", recovered)
		}
	}()
	alerter.Alert(ctx, service.ModerationAlert{})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type trackingResponseBody struct {
	*strings.Reader
	closed bool
}

func (body *trackingResponseBody) Close() error {
	body.closed = true
	return nil
}
