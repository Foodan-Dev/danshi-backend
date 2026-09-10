package service

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"time"

	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

const (
	defaultVerificationEmailDeliveryBatchSize = 4
	defaultVerificationEmailDeliveryLease     = time.Minute
	defaultVerificationEmailDeliveryTimeout   = 10 * time.Second
	defaultVerificationEmailDeliveryAttempts  = 8
	defaultVerificationEmailDeliveryRetry     = 30 * time.Second
)

// VerificationEmailDeliveryQueue 是注册和密码重置写路径依赖的 durable outbox 端口。
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
	tx     TxRunner
	store  repository.VerificationEmailDeliveryRepository
	sender VerificationEmailSender
	opts   VerificationEmailDeliveryWorkerOptions
	wake   chan struct{}
}

// NewVerificationEmailDeliveryWorker 创建验证码邮件 outbox worker。
func NewVerificationEmailDeliveryWorker(
	tx TxRunner,
	sender VerificationEmailSender,
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
	return &VerificationEmailDeliveryWorker{
		tx: tx, sender: sender, opts: opts, wake: make(chan struct{}, 1),
	}
}

// Enqueue 将验证码明文和投递任务追加到当前事务。
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
	return w.store.Enqueue(ctx, challenge, email, purpose, codeDigest, code, now)
}

// Kick 只合并后台唤醒信号，不在 HTTP 请求中等待供应商。
func (w *VerificationEmailDeliveryWorker) Kick(context.Context) {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run 由进程生命周期管理；启动扫描、定期补偿并在取消时退出。
func (w *VerificationEmailDeliveryWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	w.Kick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-ticker.C:
		}
		if ctx.Err() != nil {
			return
		}
		result, err := w.RunBatch(ctx)
		if err != nil && ctx.Err() == nil && w.opts.Log != nil {
			w.opts.Log.WarnContext(ctx, "验证码邮件 outbox 批次失败")
		}
		if w.opts.Log != nil && result.DeadLettered > 0 {
			w.opts.Log.ErrorContext(ctx, "验证码邮件投递耗尽重试预算", slog.Int("dead_lettered", result.DeadLettered))
		}
		if err == nil && result.Claimed == w.opts.BatchSize {
			w.Kick(ctx)
		}
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
	now := w.opts.Now().UTC()
	token, err := leaseToken()
	if err != nil {
		return VerificationEmailDeliveryWorkerResult{}, err
	}
	var claims []repository.VerificationEmailDeliveryClaim
	err = w.tx.RunInTx(ctx, func(txCtx context.Context) error {
		if err := w.store.ClearInvalidCodes(txCtx, now, w.opts.BatchSize*10); err != nil {
			return err
		}
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
		now = w.opts.Now().UTC()
		var owned bool
		err = w.tx.RunInTx(ctx, func(txCtx context.Context) error {
			claim, owned, err = w.store.RefreshClaim(txCtx, claim, now)
			return err
		})
		if err != nil {
			return result, err
		}
		if !owned {
			continue
		}
		if claim.Code == nil || claim.CurrentCodeDigest == "" || claim.CurrentCodeDigest != claim.CodeDigest {
			updated, err := w.updateClaim(ctx, claim, map[string]any{
				"state": model.VerificationEmailDeliveryCanceled, "lease_token": nil, "lease_until": nil,
				"next_attempt_at": nil, "last_error_code": "stale_challenge",
				"canceled_at": now, "updated_at": now, "code": nil,
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
		deliveryCtx, cancel := context.WithTimeout(ctx, w.opts.DeliveryTimeout)
		deliveryErr := sendVerificationEmail(deliveryCtx, w.sender, claim.Purpose, claim.Email, *claim.Code)
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
		"last_error_code": errorCode, "dead_lettered_at": now, "updated_at": now, "code": nil,
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
