package db

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// ctxKey 是私有类型，避免与其它包的 context key 冲突。
type ctxKey struct{}

// WithTx 把事务句柄放进 context。只有 UoW 中间件该调它。
func WithTx(ctx context.Context, tx *gorm.DB) context.Context {
	return context.WithValue(ctx, ctxKey{}, tx)
}

// FromContext 取出当前请求的事务句柄。
//
// repository 层一律通过它拿 DB，**不接受直接注入 *gorm.DB**——
// 那样会绕开请求级事务，出现「一半提交一半没提交」的写入。
// 拿不到说明调用方不在请求链路里，属于编程错误，直接 panic 比静默降级安全。
func FromContext(ctx context.Context) *gorm.DB {
	tx, ok := ctx.Value(ctxKey{}).(*gorm.DB)
	if !ok || tx == nil {
		panic("db: 当前 context 没有事务句柄，repository 必须在 UoW 中间件覆盖的请求内调用")
	}
	return tx.WithContext(ctx)
}

// ErrRollback 由业务代码返回，表示「主动回滚但不算错误」。
var ErrRollback = errors.New("db: 主动回滚")

// RunInTx 在事务里执行 fn。这是给后台任务、CLI 等**非 HTTP 路径**用的；
// HTTP 请求由 UoW 中间件统一开事务，处理器里不要再调它。
func (d *DB) RunInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return d.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := fn(WithTx(ctx, tx))
		if errors.Is(err, ErrRollback) {
			return err
		}
		return err
	})
}
