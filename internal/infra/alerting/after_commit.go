package alerting

import (
	"context"

	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

type afterCommitAlerter struct {
	next service.ModerationAlerter
}

// AfterCommit 装饰告警通道：事务内调用只在提交成功后发送；非事务调用保持显式直发。
func AfterCommit(next service.ModerationAlerter) service.ModerationAlerter {
	if next == nil {
		next = service.DiscardModerationAlerter{}
	}
	return afterCommitAlerter{next: next}
}

func (a afterCommitAlerter) Alert(ctx context.Context, alert service.ModerationAlert) {
	alert = cloneModerationAlert(alert)
	callback := func(callbackCtx context.Context) {
		a.next.Alert(callbackCtx, alert)
	}
	if db.AfterCommit(ctx, callback) {
		return
	}
	callback(ctx)
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

var _ service.ModerationAlerter = afterCommitAlerter{}
