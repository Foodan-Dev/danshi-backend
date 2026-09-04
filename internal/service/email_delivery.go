package service

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"time"

	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/secretbox"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

const (
	defaultVerificationEmailDeliveryBatchSize = 4
	defaultVerificationEmailDeliveryLease     = time.Minute
	defaultVerificationEmailDeliveryTimeout   = 10 * time.Second
	defaultVerificationEmailDeliveryAttempts  = 8
	defaultVerificationEmailDeliveryRetry     = 30 * time.Second
)

// VerificationEmailDeliveryQueue 是密码重置写路径依赖的 durable outbox 端口。
type VerificationEmailDeliveryQueue interface {
	Enqueue(
		context.Context,
		*model.EmailVerificationCode,
		string,
		model.VerificationPurpose,
		string,
		string,
		time.Time,
	) (uint64, error)
	Kick(context.Context)
}

// VerificationEmailDeliveryWorkerOptions 固定 outbox worker 的批次、租约和重试边界。
type VerificationEmailDeliveryWorkerOptions struct {
	BatchSize       int
	LeaseDuration   time.Duration
	DeliveryTimeout time.Duration
	MaxAttempts     int
	RetryDelay      time.Duration
	Now             func() time.Time
	Log             *slog.Logger
}

// VerificationEmailDeliveryWorkerResult 只包含低基数的运维计数。
type VerificationEmailDeliveryWorkerResult struct {
	Claimed      int
	Sent         int
	Canceled     int
	Rescheduled  int
	DeadLettered int
}

// VerificationEmailDeliveryWorker 负责验证码邮件的提交后投递和失败重试。
type VerificationEmailDeliveryWorker struct {
	tx      ImageAccessTxRunner
	store   repository.VerificationEmailDeliveryRepository
	sender  VerificationEmailSender
	box     *secretbox.Box
	initErr error
	opts    VerificationEmailDeliveryWorkerOptions
}

// NewVerificationEmailDeliveryWorker 创建验证码邮件 outbox worker。
func NewVerificationEmailDeliveryWorker(
	tx ImageAccessTxRunner,
	sender VerificationEmailSender,
	secret string,
	opts VerificationEmailDeliveryWorkerOptions,
) *VerificationEmailDeliveryWorker {
	if opts.BatchSize <= 0 || opts.BatchSize > 100 {
		opts.BatchSize = defaultVerificationEmailDeliveryBatchSize
	}
	if opts.LeaseDuration <= 0 {
		opts.LeaseDuration = defaultVerificationEmailDeliveryLease
	}
	if opts.DeliveryTimeout <= 0 {
		opts.DeliveryTimeout = defaultVerificationEmailDeliveryTimeout
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = defaultVerificationEmailDeliveryAttempts
	}
	if opts.RetryDelay <= 0 {
		opts.RetryDelay = defaultVerificationEmailDeliveryRetry
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	box, err := secretbox.New(secret)
	return &VerificationEmailDeliveryWorker{
		tx: tx, sender: sender, box: box, initErr: err, opts: opts,
	}
}

// Enqueue 加密验证码并把投递任务追加到当前事务。
func (w *VerificationEmailDeliveryWorker) Enqueue(
	ctx context.Context,
	challenge *model.EmailVerificationCode,
	email string,
	purpose model.VerificationPurpose,
	codeDigest string,
	code string,
	now time.Time,
) (uint64, error) {
	if w == nil {
		return 0, errors.New("verification email delivery worker is nil")
	}
	if challenge == nil {
		return 0, errors.New("verification email challenge is nil")
	}
	if w.initErr != nil {
		return 0, w.initErr
	}
	ciphertext, err := w.box.Seal([]byte(code))
	if err != nil {
		return 0, err
	}
	return w.store.Enqueue(ctx, challenge, email, purpose, codeDigest, ciphertext, now)
}

// Kick 在事务成功提交后执行一批投递；失败任务保留在 outbox 等待下一次扫描。
func (w *VerificationEmailDeliveryWorker) Kick(ctx context.Context) {
	result, err := w.RunBatch(ctx)
	if err != nil {
		if w != nil && w.opts.Log != nil && ctx.Err() == nil {
			w.opts.Log.WarnContext(ctx, "验证码邮件 outbox 批次失败", slog.Any("err", err))
		}
		return
	}
	if w != nil && w.opts.Log != nil && result.Claimed > 0 {
		w.opts.Log.InfoContext(ctx, "验证码邮件 outbox 批次完成",
			slog.Int("claimed", result.Claimed), slog.Int("sent", result.Sent),
			slog.Int("canceled", result.Canceled), slog.Int("rescheduled", result.Rescheduled),
			slog.Int("dead_lettered", result.DeadLettered),
		)
	}
}

// RunBatch 领取并投递一批到期任务；供应商调用发生在领取事务提交之后。
func (w *VerificationEmailDeliveryWorker) RunBatch(
	ctx context.Context,
) (VerificationEmailDeliveryWorkerResult, error) {
	if w == nil || w.tx == nil || w.sender == nil || w.opts.Now == nil {
		return VerificationEmailDeliveryWorkerResult{}, errors.New(
			"verification email delivery worker dependencies are incomplete",
		)
	}
	if w.initErr != nil {
		return VerificationEmailDeliveryWorkerResult{}, w.initErr
	}
	now := w.opts.Now().UTC()
	token, err := leaseToken()
	if err != nil {
		return VerificationEmailDeliveryWorkerResult{}, err
	}
	var claims []repository.VerificationEmailDeliveryClaim
	err = w.tx.RunInTx(ctx, func(txCtx context.Context) error {
		claims, err = w.store.ClaimDue(
			txCtx, now, now.Add(w.opts.LeaseDuration), token, w.opts.BatchSize,
		)
		return err
	})
	if err != nil {
		return VerificationEmailDeliveryWorkerResult{}, err
	}
	result := VerificationEmailDeliveryWorkerResult{Claimed: len(claims)}
	for _, claim := range claims {
		if claim.CurrentCodeDigest == "" || claim.CurrentCodeDigest != claim.CodeDigest {
			updated, err := w.updateClaim(ctx, claim, map[string]any{
				"state": model.VerificationEmailDeliveryCanceled, "lease_token": nil, "lease_until": nil,
				"next_attempt_at": nil, "last_error_code": "stale_challenge",
				"canceled_at": now, "updated_at": now,
			})
			if err != nil {
				return result, err
			}
			if !updated {
				continue
			}
			result.Canceled++
			continue
		}
		code, err := w.box.Open(claim.CodeCiphertext)
		if err != nil {
			updated, updateErr := w.deadLetter(ctx, claim, now, "decrypt_failed")
			if updateErr != nil {
				return result, updateErr
			}
			if !updated {
				continue
			}
			result.DeadLettered++
			continue
		}
		deliveryCtx, cancel := context.WithTimeout(ctx, w.opts.DeliveryTimeout)
		deliveryErr := sendVerificationEmail(deliveryCtx, w.sender, claim.Purpose, claim.Email, string(code))
		cancel()
		if deliveryErr == nil {
			updated, err := w.updateClaim(ctx, claim, map[string]any{
				"state": model.VerificationEmailDeliverySent, "lease_token": nil, "lease_until": nil,
				"next_attempt_at": nil, "last_error_code": nil, "sent_at": now,
				"updated_at": now,
			})
			if err != nil {
				return result, err
			}
			if !updated {
				continue
			}
			result.Sent++
			continue
		}
		attempts := claim.Attempts + 1
		if attempts >= int32(w.opts.MaxAttempts) {
			updated, err := w.deadLetter(ctx, claim, now, "delivery_exhausted")
			if err != nil {
				return result, err
			}
			if !updated {
				continue
			}
			result.DeadLettered++
			continue
		}
		next := now.Add(retryDelay(w.opts.RetryDelay, attempts))
		updated, err := w.updateClaim(ctx, claim, map[string]any{
			"state": model.VerificationEmailDeliveryPending, "attempts": attempts,
			"next_attempt_at": next, "lease_token": nil, "lease_until": nil,
			"last_error_code": "provider_error",
			"updated_at":      now,
		})
		if err != nil {
			return result, err
		}
		if !updated {
			continue
		}
		result.Rescheduled++
	}
	return result, nil
}

func (w *VerificationEmailDeliveryWorker) updateClaim(
	ctx context.Context,
	claim repository.VerificationEmailDeliveryClaim,
	updates map[string]any,
) (bool, error) {
	var updated bool
	err := w.tx.RunInTx(ctx, func(txCtx context.Context) error {
		var err error
		updated, err = w.store.UpdateClaim(txCtx, claim, updates)
		return err
	})
	return updated, err
}

func (w *VerificationEmailDeliveryWorker) deadLetter(
	ctx context.Context,
	claim repository.VerificationEmailDeliveryClaim,
	now time.Time,
	errorCode string,
) (bool, error) {
	return w.updateClaim(ctx, claim, map[string]any{
		"state": model.VerificationEmailDeliveryDeadLetter, "attempts": claim.Attempts + 1,
		"next_attempt_at": nil, "lease_token": nil, "lease_until": nil,
		"last_error_code": errorCode, "dead_lettered_at": now, "updated_at": now,
	})
}

func sendVerificationEmail(
	ctx context.Context,
	sender VerificationEmailSender,
	purpose model.VerificationPurpose,
	email string,
	code string,
) error {
	switch purpose {
	case model.VerificationPurposeRegistration:
		return sender.SendRegistrationCode(ctx, email, code)
	case model.VerificationPurposePasswordReset:
		return sender.SendPasswordResetCode(ctx, email, code)
	default:
		return errors.New("unknown verification email purpose")
	}
}

func leaseToken() (string, error) {
	var raw [24]byte
	if _, err := cryptorand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func retryDelay(base time.Duration, attempts int32) time.Duration {
	delay := base
	for index := int32(1); index < attempts && delay < 24*time.Hour; index++ {
		delay *= 2
	}
	if delay > 24*time.Hour {
		return 24 * time.Hour
	}
	return delay
}

var _ VerificationEmailDeliveryQueue = (*VerificationEmailDeliveryWorker)(nil)
