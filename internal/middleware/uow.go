package middleware

import (
	"context"
	"errors"
	"log/slog"

	"github.com/cloudwego/hertz/pkg/app"
	"gorm.io/gorm"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/httpx"
	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
)

// UnitOfWork 给每个请求开一个事务，对应 Python 侧 get_db() 的 yield/commit/rollback。
//
// 默认提交条件很严格——只有**全部**满足才提交：
//   - 处理器没有上报错误
//   - HTTP 状态码 < 400
//
// 第二条是必要的：有些路径会直接写 4xx 响应而不走 Fail()，
// 只看错误标记会把它们当成功提交。
// 唯一例外是处理器在已成功写入安全状态后显式调用 CommitError，
// 例如验证码输错需要既返回 400 又保留 failed_attempts。
//
// panic 由外层 Recovery 兜住，但事务必须在这里先回滚——
// 所以这里也有一个 defer/recover，回滚之后把 panic 重新抛出去交给 Recovery。
func UnitOfWork(database *db.DB, log *slog.Logger) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		tx := database.WithContext(ctx).Begin()
		if tx.Error != nil {
			log.ErrorContext(ctx, "开启事务失败", slog.Any("err", tx.Error))
			httpx.Fail(ctx, c, tx.Error)
			return
		}

		committed := false
		defer func() {
			if r := recover(); r != nil {
				rollback(ctx, tx, log, "panic")
				panic(r) // 交给外层 Recovery 渲染 500
			}
			if !committed {
				rollback(ctx, tx, log, "未提交")
			}
		}()

		requestCtx, afterCommit := db.WithAfterCommitQueue(db.WithTx(ctx, tx))
		c.Next(requestCtx)

		if (httpx.HasError(c) || c.Response.StatusCode() >= 400) && !shouldCommitError(c) {
			rollback(ctx, tx, log, "请求失败")
			committed = true // 已处理，defer 不必再回滚
			return
		}

		if err := tx.Commit().Error; err != nil {
			log.ErrorContext(ctx, "事务提交失败", slog.Any("err", err))
			httpx.Fail(ctx, c, err)
			committed = true
			return
		}
		committed = true
		for _, recovered := range afterCommit.Run(context.WithoutCancel(ctx)) {
			log.ErrorContext(ctx, "事务提交后回调发生 panic", slog.Any("panic", recovered))
		}
	}
}

func shouldCommitError(c *app.RequestContext) bool {
	if !httpx.CommitErrorRequested(c) {
		return false
	}
	status := c.Response.StatusCode()
	if status >= 500 {
		return false
	}
	if reported, err := httpx.ReportedError(c); reported {
		if err == nil {
			return false
		}
		status = apierr.As(err).Status
	}
	return status >= 400 && status < 500
}

func rollback(ctx context.Context, tx *gorm.DB, log *slog.Logger, reason string) {
	if err := tx.Rollback().Error; err != nil && !errors.Is(err, gorm.ErrInvalidTransaction) {
		log.WarnContext(ctx, "事务回滚失败",
			slog.String("reason", reason), slog.Any("err", err))
	}
}
