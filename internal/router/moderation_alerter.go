package router

import (
	"context"
	"log/slog"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/Foodan-Dev/danshi-backend/internal/httpx"
	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

// moderationFailureAlerting 等原请求事务完成回滚后，为“本次失败”建立独立提交边界。
// 这样内部处理错误也能上报，但任何告警仍然只会在一个成功提交之后发送。
func moderationFailureAlerting(
	database *db.DB,
	alerter service.ModerationAlerter,
	log *slog.Logger,
) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		c.Next(ctx)
		alerts := httpx.ModerationFailureAlerts(c)
		if len(alerts) == 0 {
			return
		}
		if database == nil {
			logModerationFailureBoundaryError(ctx, log, nil)
			return
		}

		callbackCtx, queue := db.WithAfterCommitQueue(context.WithoutCancel(ctx))
		err := database.RunInTx(callbackCtx, func(txCtx context.Context) error {
			for _, alert := range alerts {
				alerter.Alert(txCtx, alert)
			}
			return nil
		})
		if err != nil {
			logModerationFailureBoundaryError(ctx, log, err)
			return
		}
		for _, recovered := range queue.Run(callbackCtx) {
			if log != nil {
				log.ErrorContext(ctx, "审核失败告警提交后回调发生 panic",
					slog.Any("panic", recovered))
			}
		}
	}
}

func logModerationFailureBoundaryError(ctx context.Context, log *slog.Logger, err error) {
	if log != nil {
		log.ErrorContext(ctx, "建立审核失败告警提交边界失败", slog.Any("err", err))
	}
}
