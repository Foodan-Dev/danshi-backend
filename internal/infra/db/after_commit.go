package db

import (
	"context"
	"sync"
)

type afterCommitCtxKey struct{}

// AfterCommitQueue 收集只能在当前事务成功提交后执行的副作用。
type AfterCommitQueue struct {
	mu        sync.Mutex
	callbacks []func(context.Context)
}

// WithAfterCommitQueue 为一次事务创建提交后回调队列。
func WithAfterCommitQueue(ctx context.Context) (context.Context, *AfterCommitQueue) {
	queue := &AfterCommitQueue{}
	return context.WithValue(ctx, afterCommitCtxKey{}, queue), queue
}

// AfterCommit 把 callback 注册到当前事务；没有事务队列时返回 false。
func AfterCommit(ctx context.Context, callback func(context.Context)) bool {
	if callback == nil {
		return false
	}
	queue, ok := ctx.Value(afterCommitCtxKey{}).(*AfterCommitQueue)
	if !ok || queue == nil {
		return false
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	queue.callbacks = append(queue.callbacks, callback)
	return true
}

// Run 按注册顺序执行全部回调，并隔离单个回调的 panic。
func (q *AfterCommitQueue) Run(ctx context.Context) []any {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	callbacks := append([]func(context.Context){}, q.callbacks...)
	q.callbacks = nil
	q.mu.Unlock()

	panics := make([]any, 0)
	for _, callback := range callbacks {
		if recovered, panicked := runAfterCommit(ctx, callback); panicked {
			panics = append(panics, recovered)
		}
	}
	return panics
}

func runAfterCommit(ctx context.Context, callback func(context.Context)) (recovered any, panicked bool) {
	defer func() {
		if value := recover(); value != nil {
			recovered = value
			panicked = true
		}
	}()
	callback(ctx)
	return nil, false
}
