package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

const (
	defaultImageModerationRetryBatchSize      = 4
	defaultImageModerationRetryLease          = 45 * time.Second
	defaultImageModerationRetrySubmitTimeout  = 15 * time.Second
	defaultImageModerationRetryMaxAttempts    = 8
	defaultImageModerationRetryBaseBackoff    = 30 * time.Second
	defaultImageModerationRetryMaximumBackoff = 30 * time.Minute
)

type imageModerationRetryStore interface {
	ClaimDue(context.Context, time.Time, time.Time, string, int) ([]repository.ImageModerationRetryClaim, error)
	RescheduleClaim(context.Context, repository.ImageModerationRetryClaim, int, time.Time, string, time.Time) (bool, error)
	DeadLetterClaim(context.Context, repository.ImageModerationRetryClaim, int, time.Time) (bool, error)
	DeleteClaim(context.Context, repository.ImageModerationRetryClaim) (bool, error)
}

// ImageModerationResultApplier 持久化同步供应商直接返回的审核结论。
type ImageModerationResultApplier interface {
	ApplyImageResult(context.Context, ImageModerationCallback) (*ImageModerationApplyResult, error)
}

// ImageModerationRetryWorkerOptions 固定补审 worker 的批次、租约、超时与预算。
type ImageModerationRetryWorkerOptions struct {
	BatchSize     int
	LeaseDuration time.Duration
	SubmitTimeout time.Duration
	MaxAttempts   int
	BaseBackoff   time.Duration
	MaxBackoff    time.Duration
	Now           func() time.Time
}

// ImageModerationRetryWorkerResult 只暴露低基数批次计数。
type ImageModerationRetryWorkerResult struct {
	Claimed      int
	Submitted    int
	Concluded    int
	Rescheduled  int
	DeadLettered int
	Superseded   int
}

// ImageModerationRetryWorker 对首次送审失败的图片执行有界补审。
type ImageModerationRetryWorker struct {
	tx        ImageAccessTxRunner
	store     imageModerationRetryStore
	moderator ImageModerator
	applier   ImageModerationResultApplier
	options   ImageModerationRetryWorkerOptions
}

// NewImageModerationRetryWorker 创建一次性批处理 worker；调用方负责周期调度。
func NewImageModerationRetryWorker(
	tx ImageAccessTxRunner,
	moderator ImageModerator,
	applier ImageModerationResultApplier,
	options ImageModerationRetryWorkerOptions,
) *ImageModerationRetryWorker {
	return &ImageModerationRetryWorker{
		tx: tx, store: repository.ImageModerationRetryRepository{}, moderator: moderator,
		applier: applier, options: withImageModerationRetryDefaults(options),
	}
}

func withImageModerationRetryDefaults(
	options ImageModerationRetryWorkerOptions,
) ImageModerationRetryWorkerOptions {
	if options.BatchSize <= 0 || options.BatchSize > 100 {
		options.BatchSize = defaultImageModerationRetryBatchSize
	}
	if options.LeaseDuration <= 0 {
		options.LeaseDuration = defaultImageModerationRetryLease
	}
	if options.SubmitTimeout <= 0 {
		options.SubmitTimeout = defaultImageModerationRetrySubmitTimeout
	}
	if options.MaxAttempts <= 1 {
		options.MaxAttempts = defaultImageModerationRetryMaxAttempts
	}
	if options.BaseBackoff <= 0 {
		options.BaseBackoff = defaultImageModerationRetryBaseBackoff
	}
	if options.MaxBackoff < options.BaseBackoff {
		options.MaxBackoff = defaultImageModerationRetryMaximumBackoff
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return options
}

// RunBatch 用 SKIP LOCKED 领取有限批次，并在领取事务提交后并行调用供应商。
func (w *ImageModerationRetryWorker) RunBatch(
	ctx context.Context,
) (ImageModerationRetryWorkerResult, error) {
	if w == nil || w.tx == nil || w.store == nil || w.moderator == nil || w.applier == nil {
		return ImageModerationRetryWorkerResult{}, errors.New(
			"image moderation retry worker dependencies are incomplete",
		)
	}
	now := w.now()
	token, err := newImageModerationRetryLeaseToken()
	if err != nil {
		return ImageModerationRetryWorkerResult{}, err
	}
	var claims []repository.ImageModerationRetryClaim
	err = w.tx.RunInTx(ctx, func(txCtx context.Context) error {
		var claimErr error
		claims, claimErr = w.store.ClaimDue(
			txCtx, now, now.Add(w.options.LeaseDuration), token, w.options.BatchSize,
		)
		return claimErr
	})
	if err != nil {
		return ImageModerationRetryWorkerResult{}, err
	}
	result := ImageModerationRetryWorkerResult{Claimed: len(claims)}
	type processedClaim struct {
		outcome imageModerationRetryOutcome
		err     error
	}
	processed := make(chan processedClaim, len(claims))
	for index := range claims {
		go func(claim repository.ImageModerationRetryClaim) {
			outcome, processErr := w.processClaim(ctx, claim)
			processed <- processedClaim{outcome: outcome, err: processErr}
		}(claims[index])
	}
	var processErr error
	for range claims {
		item := <-processed
		processErr = errors.Join(processErr, item.err)
		switch item.outcome {
		case imageModerationRetrySubmitted:
			result.Submitted++
		case imageModerationRetryConcluded:
			result.Concluded++
		case imageModerationRetryDead:
			result.DeadLettered++
		case imageModerationRetrySuperseded:
			result.Superseded++
		default:
			result.Rescheduled++
		}
	}
	return result, processErr
}

type imageModerationRetryOutcome int

const (
	imageModerationRetryRescheduled imageModerationRetryOutcome = iota
	imageModerationRetrySubmitted
	imageModerationRetryConcluded
	imageModerationRetryDead
	imageModerationRetrySuperseded
)

func (w *ImageModerationRetryWorker) processClaim(
	ctx context.Context,
	claim repository.ImageModerationRetryClaim,
) (imageModerationRetryOutcome, error) {
	if claim.Moderation != model.ModerationStatusPending {
		return w.deleteClaim(ctx, claim, imageModerationRetrySuperseded)
	}
	callCtx, cancel := context.WithTimeout(ctx, w.options.SubmitTimeout)
	submission, err := w.moderator.SubmitImage(callCtx, ImageModerationRequest{
		ImageAssetID: claim.ImageAssetID, ObjectKey: claim.ObjectKey,
	})
	cancel()
	if err != nil {
		return w.failed(ctx, claim, "submit_failed")
	}
	if submission.Immediate != nil {
		return w.applyImmediate(ctx, claim, *submission.Immediate)
	}
	if strings.TrimSpace(string(submission.Provider)) == "" ||
		submission.ProviderJobID == nil || strings.TrimSpace(*submission.ProviderJobID) == "" {
		return w.failed(ctx, claim, "invalid_submission")
	}
	return w.deleteClaim(ctx, claim, imageModerationRetrySubmitted)
}

func (w *ImageModerationRetryWorker) applyImmediate(
	ctx context.Context,
	claim repository.ImageModerationRetryClaim,
	callback ImageModerationCallback,
) (imageModerationRetryOutcome, error) {
	callback.ImageAssetID = claim.ImageAssetID
	callback.ObjectKey = claim.ObjectKey
	err := w.tx.RunInTx(ctx, func(txCtx context.Context) error {
		_, applyErr := w.applier.ApplyImageResult(txCtx, callback)
		return applyErr
	})
	if err != nil {
		return imageModerationRetryRescheduled, err
	}
	return imageModerationRetryConcluded, nil
}

func (w *ImageModerationRetryWorker) failed(
	ctx context.Context,
	claim repository.ImageModerationRetryClaim,
	errorCode string,
) (imageModerationRetryOutcome, error) {
	attempts := claim.Attempts + 1
	if attempts >= w.options.MaxAttempts {
		var updated bool
		err := w.tx.RunInTx(ctx, func(txCtx context.Context) error {
			var updateErr error
			updated, updateErr = w.store.DeadLetterClaim(txCtx, claim, attempts, w.now())
			return updateErr
		})
		if err != nil {
			return imageModerationRetryRescheduled, err
		}
		if !updated {
			return imageModerationRetrySuperseded, nil
		}
		return imageModerationRetryDead, nil
	}
	now := w.now()
	next := now.Add(imageModerationRetryBackoff(
		attempts, w.options.BaseBackoff, w.options.MaxBackoff,
	))
	var updated bool
	err := w.tx.RunInTx(ctx, func(txCtx context.Context) error {
		var updateErr error
		updated, updateErr = w.store.RescheduleClaim(
			txCtx, claim, attempts, next, errorCode, now,
		)
		return updateErr
	})
	if err != nil {
		return imageModerationRetryRescheduled, err
	}
	if !updated {
		return imageModerationRetrySuperseded, nil
	}
	return imageModerationRetryRescheduled, nil
}

func (w *ImageModerationRetryWorker) deleteClaim(
	ctx context.Context,
	claim repository.ImageModerationRetryClaim,
	outcome imageModerationRetryOutcome,
) (imageModerationRetryOutcome, error) {
	var deleted bool
	err := w.tx.RunInTx(ctx, func(txCtx context.Context) error {
		var deleteErr error
		deleted, deleteErr = w.store.DeleteClaim(txCtx, claim)
		return deleteErr
	})
	if err != nil {
		return imageModerationRetryRescheduled, err
	}
	if !deleted {
		return imageModerationRetrySuperseded, nil
	}
	return outcome, nil
}

func (w *ImageModerationRetryWorker) now() time.Time { return w.options.Now().UTC() }

func imageModerationRetryBackoff(
	attempt int,
	base time.Duration,
	maximum time.Duration,
) time.Duration {
	return retryBackoff(attempt, base, maximum)
}

func initialImageModerationRetryAt(now time.Time) time.Time {
	return now.Add(defaultImageModerationRetryBaseBackoff)
}

func newImageModerationRetryLeaseToken() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
