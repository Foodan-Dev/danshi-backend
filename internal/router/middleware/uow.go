package middleware

import (
	"context"
	"errors"
	"log/slog"

	"github.com/cloudwego/hertz/pkg/app"
	"gorm.io/gorm"

	"github.com/jingyijun/danshi_backend_go/internal/infra/db"
)

// UnitOfWork 给每个请求开一个事务，对应 Python 侧 get_db() 的 yield/commit/rollback。
//
// 提交条件很严格——只有**全部**满足才提交：
//   - 处理器没有上报错误
//   - HTTP 状态码 < 400
//
// 第二条是必要的：有些路径会直接写 4xx 响应而不走 Fail()，
// 只看错误标记会把它们当成功提交。
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

		c.Next(db.WithTx(ctx, tx))

		if HasError(c) || c.Response.StatusCode() >= 400 {
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
	}
}

func rollback(ctx context.Context, tx *gorm.DB, log *slog.Logger, reason string) {
	if err := tx.Rollback().Error; err != nil && !errors.Is(err, gorm.ErrInvalidTransaction) {
		log.WarnContext(ctx, "事务回滚失败",
			slog.String("reason", reason), slog.Any("err", err))
	}
}
