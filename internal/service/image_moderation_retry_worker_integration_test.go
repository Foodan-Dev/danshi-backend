package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestImageModerationRetryWorkerSubmitsAndCallbackConcludesImageAgainstPostgres18(
	t *testing.T,
) {
	database := testutil.OpenPostgres(t)
	now := time.Now().UTC()
	asset := seedImageModerationRetry(t, database, now)
	moderator := testutil.NewMockModeration()
	moderator.SetDefaultImage(testutil.ImagePending("retry-job-accepted"))
	moderation := service.NewModerationService(
		service.DiscardModerationAlerter{}, service.NewDurableImageAccessController(),
	)
	worker := service.NewImageModerationRetryWorker(
		database.DB, moderator, moderation, service.ImageModerationRetryWorkerOptions{
			BatchSize: 1, Now: func() time.Time { return now },
		},
	)

	result, err := worker.RunBatch(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Claimed)
	require.Equal(t, 1, result.Submitted)
	require.NoError(t, database.GORM.First(&asset, asset.ID).Error)
	require.Equal(t, model.ModerationStatusPending, asset.Moderation,
		"异步受理后仍必须等待真实回调结论")
	var retry model.ImageModerationRetry
	require.ErrorIs(t, database.GORM.First(&retry, asset.ID).Error, gorm.ErrRecordNotFound)

	err = database.DB.RunInTx(context.Background(), func(ctx context.Context) error {
		_, callbackErr := moderator.TriggerImageCallback(
			ctx, "retry-job-accepted", model.ModerationVerdictPass,
			moderation.ApplyImageCallback,
		)
		return callbackErr
	})
	require.NoError(t, err)
	require.NoError(t, database.GORM.First(&asset, asset.ID).Error)
	require.Equal(t, model.ModerationStatusPass, asset.Moderation)
	var records int64
	require.NoError(t, database.GORM.Model(&model.ModerationRecord{}).
		Where("image_asset_id = ? AND provider_job_id = ?", asset.ID, "retry-job-accepted").
		Count(&records).Error)
	require.EqualValues(t, 1, records)
}

func TestImageModerationRetryWorkerDeadLettersAtAttemptLimitAgainstPostgres18(t *testing.T) {
	database := testutil.OpenPostgres(t)
	now := time.Now().UTC()
	asset := seedImageModerationRetry(t, database, now)
	moderator := testutil.NewMockModeration()
	moderator.SetDefaultImage(testutil.ImageFailure(errors.New("provider unavailable")))
	moderation := service.NewModerationService(
		service.DiscardModerationAlerter{}, service.NewDurableImageAccessController(),
	)
	worker := service.NewImageModerationRetryWorker(
		database.DB, moderator, moderation, service.ImageModerationRetryWorkerOptions{
			BatchSize: 1, MaxAttempts: 2, Now: func() time.Time { return now },
		},
	)

	result, err := worker.RunBatch(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.DeadLettered)
	var retry model.ImageModerationRetry
	require.NoError(t, database.GORM.First(&retry, asset.ID).Error)
	require.Equal(t, model.ImageModerationRetryDeadLetter, retry.State)
	require.Equal(t, 2, retry.Attempts)
	require.Equal(t, "submit_exhausted", retry.LastErrorCode)
	require.Nil(t, retry.NextAttemptAt)
	require.NotNil(t, retry.DeadLetteredAt)
}

func seedImageModerationRetry(
	t *testing.T,
	database *testutil.TestDatabase,
	now time.Time,
) model.ImageAsset {
	t.Helper()
	user := model.User{
		Email:        "retry-" + now.Format("150405.000000") + "@fdueat.com",
		PasswordHash: "x", Name: "补审测试",
	}
	require.NoError(t, database.GORM.Create(&user).Error)
	size := int64(128)
	asset := model.ImageAsset{
		UploaderID: &user.ID, Purpose: model.ImagePurposePost,
		ObjectKey: "posts/retry/image.jpg", PublicURL: "https://img.example.test/posts/retry/image.jpg",
		ContentType: "image/jpeg", Size: &size, Status: model.ImageStatusPending,
		Moderation: model.ModerationStatusPending, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, database.GORM.Create(&asset).Error)
	err := database.DB.RunInTx(context.Background(), func(ctx context.Context) error {
		return (repository.ImageModerationRetryRepository{}).Enqueue(
			ctx, asset.ID, now, "submit_failed", now,
		)
	})
	require.NoError(t, err)
	return asset
}
