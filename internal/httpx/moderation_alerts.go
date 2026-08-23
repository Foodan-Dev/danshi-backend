package httpx

import (
	"github.com/cloudwego/hertz/pkg/app"

	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

const moderationFailureAlertsCtxKey = "danshi.moderation_failure_alerts"

// AddModerationFailureAlert 暂存一条描述本次失败本身的审核告警。
// 外层中间件会等原请求事务完成回滚，再建立独立提交边界发送。
func AddModerationFailureAlert(c *app.RequestContext, alert service.ModerationAlert) {
	if c == nil {
		return
	}
	alerts := ModerationFailureAlerts(c)
	alerts = append(alerts, cloneModerationAlert(alert))
	c.Set(moderationFailureAlertsCtxKey, alerts)
}

// ModerationFailureAlerts 返回当前请求暂存的失败告警副本。
func ModerationFailureAlerts(c *app.RequestContext) []service.ModerationAlert {
	if c == nil {
		return nil
	}
	raw, ok := c.Get(moderationFailureAlertsCtxKey)
	if !ok {
		return nil
	}
	alerts, ok := raw.([]service.ModerationAlert)
	if !ok {
		return nil
	}
	cloned := make([]service.ModerationAlert, len(alerts))
	for index := range alerts {
		cloned[index] = cloneModerationAlert(alerts[index])
	}
	return cloned
}

func cloneModerationAlert(alert service.ModerationAlert) service.ModerationAlert {
	alert.Labels = append([]string{}, alert.Labels...)
	if alert.Field != nil {
		field := *alert.Field
		alert.Field = &field
	}
	if alert.ProviderJobID != nil {
		providerJobID := *alert.ProviderJobID
		alert.ProviderJobID = &providerJobID
	}
	return alert
}
