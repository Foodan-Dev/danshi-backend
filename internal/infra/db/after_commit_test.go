package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAfterCommitQueueRunsAllCallbacksInOrder(t *testing.T) {
	ctx, queue := WithAfterCommitQueue(context.Background())
	order := make([]int, 0, 2)
	require.True(t, AfterCommit(ctx, func(context.Context) {
		order = append(order, 1)
		panic("isolated")
	}))
	require.True(t, AfterCommit(ctx, func(context.Context) {
		order = append(order, 2)
	}))

	panics := queue.Run(ctx)
	require.Equal(t, []int{1, 2}, order)
	require.Equal(t, []any{"isolated"}, panics)
	require.Empty(t, queue.Run(ctx), "提交后回调只能执行一次")
	require.False(t, AfterCommit(context.Background(), func(context.Context) {}))
}
