package router

import (
	"context"

	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

type afterCommitModerationAlerter struct {
	next service.ModerationAlerter
}

func (a afterCommitModerationAlerter) Alert(ctx context.Context, alert service.ModerationAlert) {
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

var _ service.ModerationAlerter = afterCommitModerationAlerter{}
