package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestVerificationEmailDeliveryWorkerIsDurableAndRetries(t *testing.T) {
	database := testutil.OpenPostgres(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	sender := testutil.NewMockEmailSender()
	worker := service.NewVerificationEmailDeliveryWorker(
		database.DB, sender, "email-delivery-integration-secret",
		service.VerificationEmailDeliveryWorkerOptions{Now: func() time.Time { return now }},
	)

	challenge := seedDeliveryChallenge(t, database, "worker-success", strings.Repeat("a", 64), now)
	require.NoError(t, database.DB.RunInTx(context.Background(), func(ctx context.Context) error {
		_, err := worker.Enqueue(ctx, challenge, challenge.Email, challenge.Purpose,
			challenge.CodeDigest, "123456", now)
		return err
	}))
	var stored model.VerificationEmailDelivery
	require.NoError(t, database.GORM.Where("challenge_id = ?", challenge.ID).First(&stored).Error)
	require.Equal(t, model.VerificationEmailDeliveryPending, stored.State)
	require.NotEqual(t, []byte("123456"), stored.CodeCiphertext, "outbox 不得直接保存验证码明文")

	result, err := worker.RunBatch(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Claimed)
	require.Equal(t, 1, result.Sent)
	require.Equal(t, "123456", lastCode(t, sender, challenge.Email))
	require.NoError(t, database.GORM.First(&stored, stored.ID).Error)
	require.Equal(t, model.VerificationEmailDeliverySent, stored.State)

	failureSender := testutil.NewMockEmailSender()
	failureSender.SetDefault(testutil.EmailFailure(errors.New("provider private failure")))
	failureNow := now
	failureWorker := service.NewVerificationEmailDeliveryWorker(
		database.DB, failureSender, "email-delivery-integration-secret",
		service.VerificationEmailDeliveryWorkerOptions{
			Now: func() time.Time { return failureNow }, MaxAttempts: 2,
			RetryDelay: time.Second,
		},
	)
	failureChallenge := seedDeliveryChallenge(t, database, "worker-failure", strings.Repeat("c", 64), now)
	require.NoError(t, database.DB.RunInTx(context.Background(), func(ctx context.Context) error {
		_, err := failureWorker.Enqueue(ctx, failureChallenge, failureChallenge.Email,
			failureChallenge.Purpose, failureChallenge.CodeDigest, "234567", now)
		return err
	}))
	result, err = failureWorker.RunBatch(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Rescheduled)
	var failedDelivery model.VerificationEmailDelivery
	require.NoError(t, database.GORM.Where("challenge_id = ?", failureChallenge.ID).First(&failedDelivery).Error)
	require.Equal(t, int32(1), failedDelivery.Attempts)
	require.Equal(t, model.VerificationEmailDeliveryPending, failedDelivery.State)
	require.Equal(t, "provider_error", *failedDelivery.LastErrorCode)

	failureNow = now.Add(2 * time.Second)
	result, err = failureWorker.RunBatch(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.DeadLettered)
	require.NoError(t, database.GORM.First(&failedDelivery, failedDelivery.ID).Error)
	require.Equal(t, model.VerificationEmailDeliveryDeadLetter, failedDelivery.State)
	require.Equal(t, int32(2), failedDelivery.Attempts)
	require.Equal(t, "delivery_exhausted", *failedDelivery.LastErrorCode)
}

func TestVerificationEmailDeliveryWorkerCancelsStaleAndDecryptFailures(t *testing.T) {
	database := testutil.OpenPostgres(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	sender := testutil.NewMockEmailSender()
	worker := service.NewVerificationEmailDeliveryWorker(
		database.DB, sender, "email-delivery-integration-secret",
		service.VerificationEmailDeliveryWorkerOptions{Now: func() time.Time { return now }},
	)
	store := repository.VerificationEmailDeliveryRepository{}

	stale := seedDeliveryChallenge(t, database, "worker-stale", strings.Repeat("b", 64), now)
	bad := seedDeliveryChallenge(t, database, "worker-bad-cipher", strings.Repeat("d", 64), now)
	require.NoError(t, database.DB.RunInTx(context.Background(), func(ctx context.Context) error {
		if _, err := store.Enqueue(ctx, stale, stale.Email, stale.Purpose,
			strings.Repeat("a", 64), []byte("old-code"), now); err != nil {
			return err
		}
		_, err := store.Enqueue(ctx, bad, bad.Email, bad.Purpose,
			bad.CodeDigest, []byte("not-a-secretbox-payload"), now)
		return err
	}))

	result, err := worker.RunBatch(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, result.Claimed)
	require.Equal(t, 1, result.Canceled)
	require.Equal(t, 1, result.DeadLettered)
	var deliveries []model.VerificationEmailDelivery
	require.NoError(t, database.GORM.Where("challenge_id IN ?", []uint64{stale.ID, bad.ID}).
		Order("challenge_id").Find(&deliveries).Error)
	require.Len(t, deliveries, 2)
	require.Equal(t, model.VerificationEmailDeliveryCanceled, deliveries[0].State)
	require.Equal(t, "stale_challenge", *deliveries[0].LastErrorCode)
	require.Equal(t, model.VerificationEmailDeliveryDeadLetter, deliveries[1].State)
	require.Equal(t, "decrypt_failed", *deliveries[1].LastErrorCode)
	require.Empty(t, sender.Deliveries(""))
}

func TestVerificationEmailDeliveryEnqueueRollsBackWithCallerTransaction(t *testing.T) {
	database := testutil.OpenPostgres(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	worker := service.NewVerificationEmailDeliveryWorker(
		database.DB, testutil.NewMockEmailSender(), "email-delivery-integration-secret",
		service.VerificationEmailDeliveryWorkerOptions{Now: func() time.Time { return now }},
	)
	challenge := seedDeliveryChallenge(t, database, "worker-rollback", strings.Repeat("e", 64), now)
	err := database.DB.RunInTx(context.Background(), func(ctx context.Context) error {
		if _, enqueueErr := worker.Enqueue(ctx, challenge, challenge.Email, challenge.Purpose,
			challenge.CodeDigest, "345678", now); enqueueErr != nil {
			return enqueueErr
		}
		return errors.New("force caller rollback")
	})
	require.Error(t, err)
	var count int64
	require.NoError(t, database.GORM.Model(&model.VerificationEmailDelivery{}).
		Where("challenge_id = ?", challenge.ID).Count(&count).Error)
	require.Zero(t, count)
}

func seedDeliveryChallenge(
	t *testing.T,
	database *testutil.TestDatabase,
	suffix string,
	digest string,
	now time.Time,
) *model.EmailVerificationCode {
	t.Helper()
	challenge := &model.EmailVerificationCode{
		Email: suffix + "@fdueat.com", Purpose: model.VerificationPurposePasswordReset,
		CodeDigest: digest, ExpiresAt: now.Add(10 * time.Minute),
		SendWindowStartedAt: now,
	}
	require.NoError(t, database.GORM.Create(&model.User{
		Email: challenge.Email, PasswordHash: "x", Name: suffix,
	}).Error)
	require.NoError(t, database.GORM.Create(challenge).Error)
	return challenge
}

func lastCode(t *testing.T, sender *testutil.MockEmailSender, email string) string {
	t.Helper()
	code, ok := sender.LastCode(email)
	require.True(t, ok)
	return code
}
