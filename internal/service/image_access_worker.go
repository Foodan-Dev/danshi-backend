package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

const (
	defaultImageAccessLease            = 60 * time.Second
	defaultImageAccessACLTimeout       = 15 * time.Second
	defaultImageAccessUnknownGrace     = 90 * time.Second
	defaultImageAccessPendingTimeout   = 10 * time.Minute
	defaultImageAccessMaxACLAttempts   = 8
	defaultImageAccessMaxSubmissions   = 3
	defaultImageAccessMaxPollFailures  = 8
	defaultImageAccessMaxUnknownChecks = 12
)

// ImageAccessTxRunner 让 worker 的 claim/finalize 各自使用短事务。
type ImageAccessTxRunner interface {
	RunInTx(ctx context.Context, fn func(context.Context) error) error
}

type imageAccessOutboxStore interface {
	ClaimDue(context.Context, time.Time, time.Time, string, int) ([]repository.ImageAccessDeliveryClaim, error)
	UpdateClaim(context.Context, repository.ImageAccessDeliveryClaim, map[string]any) (bool, error)
	ReleaseSuperseded(context.Context, uint64, string, time.Time) error
}

// ImageAccessWorkerOptions 固定 worker 的有界批次、lease 与重试预算。
type ImageAccessWorkerOptions struct {
	BatchSize        int
	LeaseDuration    time.Duration
	ACLTimeout       time.Duration
	UnknownGrace     time.Duration
	PendingTimeout   time.Duration
	MaxACLAttempts   int
	MaxSubmissions   int
	MaxPollFailures  int
	MaxUnknownChecks int
	Now              func() time.Time
}

// ImageAccessWorkerResult 只包含低基数计数，不携带 URL、对象键、JobId 或错误正文。
type ImageAccessWorkerResult struct {
	Claimed      int
	Succeeded    int
	Rescheduled  int
	DeadLettered int
	Superseded   int
}

// ImageAccessWorker 最终收敛 COS ACL 与 EdgeOne 精确刷新任务。
type ImageAccessWorker struct {
	tx       ImageAccessTxRunner
	store    imageAccessOutboxStore
	storage  ImageStorage
	provider ImageCachePurgeTaskProvider
	options  ImageAccessWorkerOptions
}

// NewImageAccessWorker 创建一次性批处理 worker；调用方负责周期调度。
func NewImageAccessWorker(
	tx ImageAccessTxRunner,
	storage ImageStorage,
	provider ImageCachePurgeTaskProvider,
	options ImageAccessWorkerOptions,
) *ImageAccessWorker {
	options = withImageAccessWorkerDefaults(options)
	return &ImageAccessWorker{
		tx: tx, store: repository.ImageAccessOutboxRepository{}, storage: storage,
		provider: provider, options: options,
	}
}

func withImageAccessWorkerDefaults(options ImageAccessWorkerOptions) ImageAccessWorkerOptions {
	if options.BatchSize <= 0 || options.BatchSize > 4 {
		options.BatchSize = 4
	}
	if options.LeaseDuration <= 0 {
		options.LeaseDuration = defaultImageAccessLease
	}
	if options.ACLTimeout <= 0 {
		options.ACLTimeout = defaultImageAccessACLTimeout
	}
	if options.UnknownGrace <= 0 {
		options.UnknownGrace = defaultImageAccessUnknownGrace
	}
	if options.PendingTimeout <= 0 {
		options.PendingTimeout = defaultImageAccessPendingTimeout
	}
	if options.MaxACLAttempts <= 0 {
		options.MaxACLAttempts = defaultImageAccessMaxACLAttempts
	}
	if options.MaxSubmissions <= 0 {
		options.MaxSubmissions = defaultImageAccessMaxSubmissions
	}
	if options.MaxPollFailures <= 0 {
		options.MaxPollFailures = defaultImageAccessMaxPollFailures
	}
	if options.MaxUnknownChecks <= 0 {
		options.MaxUnknownChecks = defaultImageAccessMaxUnknownChecks
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return options
}

// RunBatch 用一条 SKIP LOCKED 查询领取有限批次，然后在事务外顺序执行外部调用。
func (w *ImageAccessWorker) RunBatch(ctx context.Context) (ImageAccessWorkerResult, error) {
	if w == nil || w.tx == nil || w.store == nil || w.storage == nil || w.provider == nil {
		return ImageAccessWorkerResult{}, errors.New("image access worker dependencies are incomplete")
	}
	now := w.now()
	token, err := newImageAccessLeaseToken()
	if err != nil {
		return ImageAccessWorkerResult{}, err
	}
	var claims []repository.ImageAccessDeliveryClaim
	err = w.tx.RunInTx(ctx, func(txCtx context.Context) error {
		var claimErr error
		claims, claimErr = w.store.ClaimDue(
			txCtx, now, now.Add(w.options.LeaseDuration), token, w.options.BatchSize,
		)
		return claimErr
	})
	if err != nil {
		return ImageAccessWorkerResult{}, err
	}
	result := ImageAccessWorkerResult{Claimed: len(claims)}
	type processedClaim struct {
		outcome imageAccessOutcome
		err     error
	}
	processed := make(chan processedClaim, len(claims))
	for index := range claims {
		go func(claim repository.ImageAccessDeliveryClaim) {
			outcome, processErr := w.processClaim(ctx, &claim)
			processed <- processedClaim{outcome: outcome, err: processErr}
		}(claims[index])
	}
	var processErr error
	for range claims {
		item := <-processed
		processErr = errors.Join(processErr, item.err)
		switch item.outcome {
		case imageAccessSucceeded:
			result.Succeeded++
		case imageAccessDead:
			result.DeadLettered++
		case imageAccessSuperseded:
			result.Superseded++
		default:
			result.Rescheduled++
		}
	}
	return result, processErr
}

type imageAccessOutcome int

const (
	imageAccessRescheduled imageAccessOutcome = iota
	imageAccessSucceeded
	imageAccessDead
	imageAccessSuperseded
)

func (w *ImageAccessWorker) processClaim(
	ctx context.Context,
	claim *repository.ImageAccessDeliveryClaim,
) (imageAccessOutcome, error) {
	switch claim.State {
	case model.ImageAccessPendingACL:
		return w.applyACL(ctx, claim)
	case model.ImageAccessPendingSubmit:
		return w.submit(ctx, claim)
	case model.ImageAccessSubmitting:
		return w.recoverSubmission(ctx, claim)
	case model.ImageAccessSubmitted:
		return w.poll(ctx, claim)
	default:
		return w.dead(ctx, *claim, "poll_permanent")
	}
}

func (w *ImageAccessWorker) applyACL(
	ctx context.Context,
	claim *repository.ImageAccessDeliveryClaim,
) (imageAccessOutcome, error) {
	callCtx, cancel := context.WithTimeout(ctx, w.options.ACLTimeout)
	err := w.storage.SetObjectPublicAccess(callCtx, claim.ObjectKey, claim.DesiredPublic)
	cancel()
	if err != nil {
		attempts := claim.ACLAttempts + 1
		if attempts >= w.options.MaxACLAttempts {
			return w.dead(ctx, *claim, "acl_exhausted")
		}
		return w.schedule(ctx, *claim, model.ImageAccessPendingACL,
			w.now().Add(retryBackoff(attempts, time.Second, 5*time.Minute)),
			"acl_retryable", map[string]any{"acl_attempts": attempts})
	}
	if !claim.PurgeRequired {
		return w.succeed(ctx, *claim)
	}
	updated, err := w.update(ctx, *claim, map[string]any{
		"state": model.ImageAccessPendingSubmit, "next_attempt_at": w.now(),
		"last_error_code": nil, "updated_at": w.now(),
	})
	if err != nil || !updated {
		return w.updateOutcome(ctx, *claim, updated, err)
	}
	claim.State = model.ImageAccessPendingSubmit
	return w.submit(ctx, claim)
}

func (w *ImageAccessWorker) submit(
	ctx context.Context,
	claim *repository.ImageAccessDeliveryClaim,
) (imageAccessOutcome, error) {
	if claim.SubmitAttempts >= w.options.MaxSubmissions {
		return w.dead(ctx, *claim, "submit_exhausted")
	}
	startedAt := w.now()
	updated, err := w.update(ctx, *claim, map[string]any{
		"state": model.ImageAccessSubmitting, "submit_attempts": claim.SubmitAttempts + 1,
		"submission_started_at": startedAt, "next_attempt_at": startedAt.Add(5 * time.Second),
		"last_error_code": nil, "updated_at": startedAt,
	})
	if err != nil || !updated {
		return w.updateOutcome(ctx, *claim, updated, err)
	}
	claim.State = model.ImageAccessSubmitting
	claim.SubmitAttempts++
	claim.SubmissionStartedAt = &startedAt
	submission, submitErr := w.provider.Submit(ctx, claim.PublicURL)
	if submitErr != nil {
		kind := ClassifyImageCachePurgeError(submitErr)
		if kind == ImageCachePurgeErrorPermanent {
			return w.dead(ctx, *claim, "submit_permanent")
		}
		code := "submit_unknown"
		if kind == ImageCachePurgeErrorRetryable {
			code = "submit_retryable"
		}
		return w.schedule(ctx, *claim, model.ImageAccessSubmitting,
			w.now().Add(5*time.Second), code, nil)
	}
	if submission.JobID == "" {
		return w.schedule(ctx, *claim, model.ImageAccessSubmitting,
			w.now().Add(5*time.Second), "submit_unknown", nil)
	}
	code := any(nil)
	if submission.Partial {
		code = "provider_failed"
	}
	return w.schedule(ctx, *claim, model.ImageAccessSubmitted,
		w.now().Add(2*time.Second), code, map[string]any{
			"provider_job_id": submission.JobID,
		})
}

func (w *ImageAccessWorker) recoverSubmission(
	ctx context.Context,
	claim *repository.ImageAccessDeliveryClaim,
) (imageAccessOutcome, error) {
	if claim.SubmissionStartedAt == nil {
		return w.dead(ctx, *claim, "discover_permanent")
	}
	recovery, err := w.provider.Recover(
		ctx, claim.PublicURL, *claim.SubmissionStartedAt, w.now(),
	)
	if err != nil {
		if ClassifyImageCachePurgeError(err) == ImageCachePurgeErrorPermanent {
			return w.dead(ctx, *claim, "discover_permanent")
		}
		return w.scheduleUnknown(ctx, *claim, "discover_retryable")
	}
	if recovery.EffectSucceeded {
		return w.succeed(ctx, *claim)
	}
	if recovery.Found && recovery.JobID != "" {
		claim.ProviderJobID = &recovery.JobID
		switch recovery.State {
		case ImageCachePurgeSuccess:
			return w.succeed(ctx, *claim)
		case ImageCachePurgeFailed, ImageCachePurgeTimeout, ImageCachePurgeCanceled:
			return w.retrySubmission(ctx, *claim, providerStateCode(recovery.State))
		default:
			return w.schedule(ctx, *claim, model.ImageAccessSubmitted,
				w.now().Add(2*time.Second), nil,
				map[string]any{"provider_job_id": recovery.JobID})
		}
	}
	age := w.now().Sub(*claim.SubmissionStartedAt)
	if recovery.Found && terminalPurgeState(recovery.State) {
		return w.retrySubmission(ctx, *claim, providerStateCode(recovery.State))
	}
	if recovery.Ambiguous && age < w.options.UnknownGrace {
		return w.scheduleUnknown(ctx, *claim, "discover_retryable")
	}
	if age >= w.options.UnknownGrace {
		if imageAccessString(claim.LastErrorCode) == "submit_retryable" &&
			claim.SubmitAttempts < w.options.MaxSubmissions && !recovery.Ambiguous {
			return w.retrySubmission(ctx, *claim, "submit_retryable")
		}
		return w.dead(ctx, *claim, "discover_exhausted")
	}
	return w.scheduleUnknown(ctx, *claim, "submit_unknown")
}

func (w *ImageAccessWorker) scheduleUnknown(
	ctx context.Context,
	claim repository.ImageAccessDeliveryClaim,
	code string,
) (imageAccessOutcome, error) {
	checks := claim.UnknownChecks + 1
	if checks >= w.options.MaxUnknownChecks {
		return w.dead(ctx, claim, "discover_exhausted")
	}
	return w.schedule(ctx, claim, model.ImageAccessSubmitting,
		w.now().Add(retryBackoff(checks, 5*time.Second, 15*time.Second)), code,
		map[string]any{"unknown_checks": checks})
}

func (w *ImageAccessWorker) poll(
	ctx context.Context,
	claim *repository.ImageAccessDeliveryClaim,
) (imageAccessOutcome, error) {
	if claim.ProviderJobID == nil || claim.SubmissionStartedAt == nil {
		return w.dead(ctx, *claim, "poll_permanent")
	}
	state, err := w.provider.Describe(ctx, claim.PublicURL, *claim.ProviderJobID)
	if err != nil {
		if ClassifyImageCachePurgeError(err) == ImageCachePurgeErrorPermanent {
			return w.dead(ctx, *claim, "poll_permanent")
		}
		attempts := claim.PollAttempts + 1
		if attempts >= w.options.MaxPollFailures {
			return w.dead(ctx, *claim, "poll_exhausted")
		}
		return w.schedule(ctx, *claim, model.ImageAccessSubmitted,
			w.now().Add(retryBackoff(attempts, 2*time.Second, 30*time.Second)),
			"poll_retryable", map[string]any{"poll_attempts": attempts})
	}
	switch state {
	case ImageCachePurgeSuccess:
		return w.succeed(ctx, *claim)
	case ImageCachePurgeFailed, ImageCachePurgeTimeout, ImageCachePurgeCanceled:
		return w.retrySubmission(ctx, *claim, providerStateCode(state))
	case ImageCachePurgeProcessing, ImageCachePurgeProtocolUnknown:
		if w.now().Sub(*claim.SubmissionStartedAt) >= w.options.PendingTimeout {
			return w.retrySubmission(ctx, *claim, "provider_pending_timeout")
		}
		polls := claim.PollAttempts + 1
		return w.schedule(ctx, *claim, model.ImageAccessSubmitted,
			w.now().Add(retryBackoff(polls, 2*time.Second, 30*time.Second)), nil,
			map[string]any{"poll_attempts": polls})
	default:
		return w.dead(ctx, *claim, "poll_permanent")
	}
}

func (w *ImageAccessWorker) retrySubmission(
	ctx context.Context,
	claim repository.ImageAccessDeliveryClaim,
	code string,
) (imageAccessOutcome, error) {
	if claim.SubmitAttempts >= w.options.MaxSubmissions {
		return w.dead(ctx, claim, "submit_exhausted")
	}
	return w.schedule(ctx, claim, model.ImageAccessPendingSubmit,
		w.now().Add(retryBackoff(claim.SubmitAttempts, 10*time.Second, 5*time.Minute)), code,
		map[string]any{
			"provider_job_id": nil, "submission_started_at": nil,
			"poll_attempts": 0, "unknown_checks": 0,
		})
}

func (w *ImageAccessWorker) schedule(
	ctx context.Context,
	claim repository.ImageAccessDeliveryClaim,
	state model.ImageAccessOutboxState,
	next time.Time,
	errorCode any,
	extra map[string]any,
) (imageAccessOutcome, error) {
	updates := map[string]any{
		"state": state, "next_attempt_at": next.UTC(), "lease_token": nil,
		"lease_until": nil, "last_error_code": errorCode, "updated_at": w.now(),
	}
	for key, value := range extra {
		updates[key] = value
	}
	updated, err := w.update(ctx, claim, updates)
	if err != nil || !updated {
		return w.updateOutcome(ctx, claim, updated, err)
	}
	return imageAccessRescheduled, nil
}

func (w *ImageAccessWorker) succeed(
	ctx context.Context,
	claim repository.ImageAccessDeliveryClaim,
) (imageAccessOutcome, error) {
	now := w.now()
	updated, err := w.update(ctx, claim, map[string]any{
		"state": model.ImageAccessSucceeded, "next_attempt_at": nil,
		"lease_token": nil, "lease_until": nil, "last_error_code": nil,
		"completed_at": now, "dead_lettered_at": nil, "updated_at": now,
	})
	if err != nil || !updated {
		return w.updateOutcome(ctx, claim, updated, err)
	}
	return imageAccessSucceeded, nil
}

func (w *ImageAccessWorker) dead(
	ctx context.Context,
	claim repository.ImageAccessDeliveryClaim,
	code string,
) (imageAccessOutcome, error) {
	now := w.now()
	updated, err := w.update(ctx, claim, map[string]any{
		"state": model.ImageAccessDeadLetter, "next_attempt_at": nil,
		"lease_token": nil, "lease_until": nil, "last_error_code": code,
		"completed_at": nil, "dead_lettered_at": now, "updated_at": now,
	})
	if err != nil || !updated {
		return w.updateOutcome(ctx, claim, updated, err)
	}
	return imageAccessDead, nil
}

func (w *ImageAccessWorker) update(
	ctx context.Context,
	claim repository.ImageAccessDeliveryClaim,
	updates map[string]any,
) (updated bool, err error) {
	err = w.tx.RunInTx(ctx, func(txCtx context.Context) error {
		var updateErr error
		updated, updateErr = w.store.UpdateClaim(txCtx, claim, updates)
		return updateErr
	})
	return updated, err
}

func (w *ImageAccessWorker) updateOutcome(
	ctx context.Context,
	claim repository.ImageAccessDeliveryClaim,
	updated bool,
	err error,
) (imageAccessOutcome, error) {
	if err != nil {
		return imageAccessRescheduled, err
	}
	if updated {
		return imageAccessRescheduled, nil
	}
	token := imageAccessString(claim.LeaseToken)
	releaseErr := w.tx.RunInTx(ctx, func(txCtx context.Context) error {
		return w.store.ReleaseSuperseded(txCtx, claim.ImageAssetID, token, w.now())
	})
	return imageAccessSuperseded, releaseErr
}

func (w *ImageAccessWorker) now() time.Time { return w.options.Now().UTC() }

func retryBackoff(attempt int, base time.Duration, maximum time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := base
	for index := 1; index < attempt && delay < maximum; index++ {
		delay *= 2
		if delay >= maximum {
			return maximum
		}
	}
	return min(delay, maximum)
}

func providerStateCode(state ImageCachePurgeTaskState) string {
	switch state {
	case ImageCachePurgeTimeout:
		return "provider_timeout"
	case ImageCachePurgeCanceled:
		return "provider_canceled"
	default:
		return "provider_failed"
	}
}

func terminalPurgeState(state ImageCachePurgeTaskState) bool {
	return state == ImageCachePurgeFailed || state == ImageCachePurgeTimeout ||
		state == ImageCachePurgeCanceled
}

func newImageAccessLeaseToken() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func imageAccessString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
