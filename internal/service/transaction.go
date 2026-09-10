package service

import "context"

// TxRunner 为后台任务的领取和结果写回提供独立短事务。
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(context.Context) error) error
}
