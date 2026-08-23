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
	text := alertText(alert)
	payload := struct {
		MsgType string `json:"msg_type"`
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	}{MsgType: "text"}
	payload.Content.Text = text
	body, err := json.Marshal(payload)
	if err != nil {
		a.logFailure(ctx, alert, err)
		return
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url, bytes.NewReader(body))
	if err != nil {
		a.logFailure(ctx, alert, err)
		return
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(request)
	if err != nil {
		a.logFailure(ctx, alert, err)
		return
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			a.logFailure(ctx, alert, closeErr)
		}
	}()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		a.logFailure(ctx, alert, fmt.Errorf("飞书 webhook 返回 HTTP %d", response.StatusCode))
	}
}

func alertText(alert service.ModerationAlert) string {
	switch alert.EffectiveKind() {
	case service.ModerationAlertKindCallbackAuthFailures:
		return fmt.Sprintf(
			"旦食审核异常告警\n类型：%s\n供应商：%s\n窗口失败次数：%d\n窗口：%d 秒",
			alert.EffectiveKind(), alert.Provider, alert.Occurrences, alert.WindowSeconds,
		)
	case service.ModerationAlertKindCallbackPayloadInvalid,
		service.ModerationAlertKindCallbackTargetInvalid:
		return fmt.Sprintf(
			"旦食审核异常告警\n类型：%s\n对象：%s/%d\n供应商：%s\n错误码：%s",
			alert.EffectiveKind(), alert.Target, alert.TargetID, alert.Provider, alert.FailureCode,
		)
	case service.ModerationAlertKindCallbackProcessingFailed:
		return fmt.Sprintf(
			"旦食审核异常告警\n类型：%s\n对象：%s/%d\n供应商：%s\n错误码：%s\n错误 ID：%s",
			alert.EffectiveKind(), alert.Target, alert.TargetID, alert.Provider,
			alert.FailureCode, alert.ErrorID,
		)
	case service.ModerationAlertKindReviewBacklog:
		return fmt.Sprintf(
			"旦食审核异常告警\n类型：%s\n待复核队列条目：%d\n告警阈值：%d",
			alert.EffectiveKind(), alert.QueueDepth, alert.Threshold,
		)
	default:
		return fmt.Sprintf(
			"旦食内容审核告警\n对象：%s/%d\n供应商：%s\n结论：%s\n标签：%v",
			alert.Target, alert.TargetID, alert.Provider, alert.Verdict, alert.Labels,
		)
	}
}

func (a *FeishuWebhook) logFailure(ctx context.Context, alert service.ModerationAlert, err error) {
	if a.log != nil {
		a.log.ErrorContext(ctx, "飞书审核告警发送失败",
			slog.String("alert_kind", string(alert.EffectiveKind())),
			slog.String("target", string(alert.Target)),
			slog.Uint64("target_id", alert.TargetID),
			slog.Int64("queue_depth", alert.QueueDepth),
			slog.Any("err", err))
	}
}
