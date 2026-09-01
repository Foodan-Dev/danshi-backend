package service

import (
	"context"
	"errors"
	"time"
)

const defaultPendingUploadExpirationBatchSize = 100

// PendingUploadExpirationWorkerOptions 固定待回收上传 worker 的保留期、批次与时钟。
type PendingUploadExpirationWorkerOptions struct {
	Retention time.Duration
	BatchSize int
	Now       func() time.Time
}

// PendingUploadExpirationWorkerResult 附带本批的创建时间截止点，便于结构化观测。
type PendingUploadExpirationWorkerResult struct {
	UploadExpirationResult
	Before time.Time
}

// PendingUploadExpirationWorker 在事务内复用 UploadService.ExpirePending 执行一个有界批次。
type PendingUploadExpirationWorker struct {
	tx      ImageAccessTxRunner
	uploads *UploadService
	options PendingUploadExpirationWorkerOptions
}

// NewPendingUploadExpirationWorker 创建一次性批处理 worker；调用方负责周期调度。
func NewPendingUploadExpirationWorker(
	tx ImageAccessTxRunner,
	uploads *UploadService,
	options PendingUploadExpirationWorkerOptions,
) *PendingUploadExpirationWorker {
	if options.BatchSize <= 0 {
		options.BatchSize = defaultPendingUploadExpirationBatchSize
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &PendingUploadExpirationWorker{tx: tx, uploads: uploads, options: options}
}

// RunBatch 按保留期计算截止点，再调用与 danshi-jobs 相同的回收方法。
func (w *PendingUploadExpirationWorker) RunBatch(
	ctx context.Context,
) (PendingUploadExpirationWorkerResult, error) {
	if w == nil || w.tx == nil || w.uploads == nil || w.options.Now == nil {
		return PendingUploadExpirationWorkerResult{}, errors.New(
			"pending upload expiration worker dependencies are incomplete",
		)
	}
	if w.options.Retention <= 0 {
		return PendingUploadExpirationWorkerResult{}, errors.New(
			"pending upload expiration retention must be positive",
		)
	}
	before := w.options.Now().UTC().Add(-w.options.Retention)
	result := UploadExpirationResult{}
	err := w.tx.RunInTx(ctx, func(txCtx context.Context) error {
		var expireErr error
		result, expireErr = w.uploads.ExpirePending(txCtx, ExpirePendingOptions{
			Before: before,
			Limit:  w.options.BatchSize,
		})
		return expireErr
	})
	return PendingUploadExpirationWorkerResult{
		UploadExpirationResult: result,
		Before:                 before,
	}, err
}
