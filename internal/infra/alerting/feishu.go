// Package alerting 提供管理员审核告警适配器。
package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

const webhookTimeout = 5 * time.Second

// FeishuWebhook 把非 pass 结论发送到飞书自定义机器人。
type FeishuWebhook struct {
	url    string
	client *http.Client
	log    *slog.Logger
}

// NewFeishuWebhook 创建飞书告警器。
func NewFeishuWebhook(url string, client *http.Client, log *slog.Logger) *FeishuWebhook {
	if client == nil {
		client = &http.Client{Timeout: webhookTimeout}
	}
	return &FeishuWebhook{url: url, client: client, log: log}
}

// Alert 发送不含用户原文的审核摘要；失败只记录日志，不覆盖审核事务。
func (a *FeishuWebhook) Alert(ctx context.Context, alert service.ModerationAlert) {
	text := fmt.Sprintf(
		"旦食内容审核告警\n对象：%s/%d\n供应商：%s\n结论：%s\n标签：%v",
		alert.Target, alert.TargetID, alert.Provider, alert.Verdict, alert.Labels,
	)
	payload := struct {
		MsgType string `json:"msg_type"`
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}{MsgType: "text"}
	payload.Content.Text = text
	body, err := json.Marshal(payload)
	if err != nil {
		a.logFailure(ctx, err)
		return
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url, bytes.NewReader(body))
	if err != nil {
		a.logFailure(ctx, err)
		return
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(request)
	if err != nil {
		a.logFailure(ctx, err)
		return
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			a.logFailure(ctx, closeErr)
		}
	}()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		a.logFailure(ctx, fmt.Errorf("飞书 webhook 返回 HTTP %d", response.StatusCode))
	}
}

func (a *FeishuWebhook) logFailure(ctx context.Context, err error) {
	if a.log != nil {
		a.log.ErrorContext(ctx, "飞书审核告警发送失败", slog.Any("err", err))
	}
}
