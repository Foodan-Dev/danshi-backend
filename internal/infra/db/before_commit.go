package db

import (
	"context"
	"errors"
)

type beforeCommitCtxKey struct{}

// BeforeCommitQueue 延后执行同一事务中的收尾写入；任何失败仍回滚整个事务。
// 与事务句柄一样，队列由当前事务的单个执行流使用。
type BeforeCommitQueue struct {
	callbacks []func(context.Context) error
}

// WithBeforeCommitQueue 为事务创建提交前队列。
func WithBeforeCommitQueue(ctx context.Context) (context.Context, *BeforeCommitQueue) {
	queue := &BeforeCommitQueue{}
	return context.WithValue(ctx, beforeCommitCtxKey{}, queue), queue
}

// BeforeCommit 注册事务收尾操作，防止鉴权阶段的会话写入颠倒业务锁顺序。
func BeforeCommit(ctx context.Context, callback func(context.Context) error) error {
	queue, ok := ctx.Value(beforeCommitCtxKey{}).(*BeforeCommitQueue)
	if !ok || queue == nil || callback == nil {
		return errors.New("db: 提交前回调需要事务队列与非空操作")
	}
	queue.callbacks = append(queue.callbacks, callback)
	return nil
}

// Run 在业务成功后、提交前执行；失败立即停止，由调用方回滚。
func (q *BeforeCommitQueue) Run(ctx context.Context) error {
	callbacks := q.callbacks
	q.callbacks = nil
	for _, callback := range callbacks {
		if err := callback(ctx); err != nil {
			return err
		}
	}
	return nil
}
