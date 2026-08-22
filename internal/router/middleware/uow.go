package middleware

import (
	"context"
	"errors"
	"log/slog"

	"github.com/cloudwego/hertz/pkg/app"
	"gorm.io/gorm"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/infra/db"
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
			Fail(ctx, c, tx.Error)
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

		if (HasError(c) || c.Response.StatusCode() >= 400) && !shouldCommitError(c) {
			rollback(ctx, tx, log, "请求失败")
			committed = true // 已处理，defer 不必再回滚
			return
		}

		if err := tx.Commit().Error; err != nil {
			log.ErrorContext(ctx, "事务提交失败", slog.Any("err", err))
			Fail(ctx, c, err)
			committed = true
			return
		}
		committed = true
		for _, recovered := range afterCommit.Run(context.WithoutCancel(ctx)) {
			log.ErrorContext(ctx, "事务提交后回调发生 panic", slog.Any("panic", recovered))
		}
	}
}

const commitErrorCtxKey = "danshi.commit_error"

// CommitError 显式允许一次 4xx 业务错误响应提交当前事务。
//
// 只应用于“拒绝请求本身也必须留下安全状态”的场景，例如验证码输错后递增
// failed_attempts。普通 4xx 仍然回滚；调用方必须在全部必要写入成功后才能标记。
// 5xx 表示服务端未能可靠完成请求，无论是否误置本标记都必须回滚。
func CommitError(c *app.RequestContext) {
	c.Set(commitErrorCtxKey, true)
}

func shouldCommitError(c *app.RequestContext) bool {
	value, ok := c.Get(commitErrorCtxKey)
	commit, valid := value.(bool)
	if !ok || !valid || !commit {
		return false
	}
	status := c.Response.StatusCode()
	if status >= 500 {
		return false
	}
	if value, exists := c.Get(errCtxKey); exists && value != nil {
		err, isError := value.(error)
		if !isError || err == nil {
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
