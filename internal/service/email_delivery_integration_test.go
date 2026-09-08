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
		database.DB, sender,
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
	require.NotNil(t, stored.Code)
	require.Equal(t, "123456", *stored.Code, "outbox 保存明文供重试")

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
		database.DB, failureSender,
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

func TestVerificationEmailDeliveryWorkerCancelsStaleAndExpired(t *testing.T) {
	database := testutil.OpenPostgres(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	sender := testutil.NewMockEmailSender()
	worker := service.NewVerificationEmailDeliveryWorker(database.DB, sender,
		service.VerificationEmailDeliveryWorkerOptions{Now: func() time.Time { return now }})
	store := repository.VerificationEmailDeliveryRepository{}
	stale := seedDeliveryChallenge(t, database, "worker-stale", strings.Repeat("b", 64), now)
	expired := seedDeliveryChallenge(t, database, "worker-expired", strings.Repeat("c", 64), now.Add(-11*time.Minute))
	require.NoError(t, database.DB.RunInTx(context.Background(), func(ctx context.Context) error {
		if _, err := store.Enqueue(ctx, stale, stale.Email, stale.Purpose, strings.Repeat("a", 64), "123456", now); err != nil {
			return err
		}
		_, err := store.Enqueue(ctx, expired, expired.Email, expired.Purpose, expired.CodeDigest, "234567", now)
		return err
	}))
	result, err := worker.RunBatch(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, result.Canceled)
	var deliveries []model.VerificationEmailDelivery
	require.NoError(t, database.GORM.Find(&deliveries).Error)
	require.Len(t, deliveries, 2)
	for _, delivery := range deliveries {
		require.Nil(t, delivery.Code)
		require.Equal(t, model.VerificationEmailDeliveryCanceled, delivery.State)
	}
	require.Empty(t, sender.Deliveries(""))
}

func TestVerificationEmailDeliveryEnqueueRollsBackWithCallerTransaction(t *testing.T) {
	database := testutil.OpenPostgres(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	worker := service.NewVerificationEmailDeliveryWorker(
		database.DB, testutil.NewMockEmailSender(),
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

func TestVerificationEmailDeliveryClearsExpiredSentCode(t *testing.T) {
	database := testutil.OpenPostgres(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	sender := testutil.NewMockEmailSender()
	worker := service.NewVerificationEmailDeliveryWorker(database.DB, sender,
		service.VerificationEmailDeliveryWorkerOptions{Now: func() time.Time { return now }})
	challenge := seedDeliveryChallenge(t, database, "sent-expiration", strings.Repeat("a", 64), now)
	require.NoError(t, database.DB.RunInTx(context.Background(), func(ctx context.Context) error {
		_, err := worker.Enqueue(ctx, challenge, challenge.Email, challenge.Purpose,
			challenge.CodeDigest, "123456", now)
		return err
	}))
	result, err := worker.RunBatch(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Sent)
	now = now.Add(11 * time.Minute)
	result, err = worker.RunBatch(context.Background())
	require.NoError(t, err)
	require.Zero(t, result.Claimed)
	var delivery model.VerificationEmailDelivery
	require.NoError(t, database.GORM.Where("challenge_id = ?", challenge.ID).First(&delivery).Error)
	require.Equal(t, model.VerificationEmailDeliverySent, delivery.State)
	require.Nil(t, delivery.Code, "已发送任务同样清理过期验证码，保留投递结果")
	require.Len(t, sender.Deliveries(challenge.Email), 1)
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
		Email: challenge.Email, PasswordHash: "x", Username: strings.ReplaceAll(suffix, "-", "_"),
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
