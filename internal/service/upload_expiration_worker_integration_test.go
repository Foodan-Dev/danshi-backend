package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestPendingUploadExpirationWorkerRetiresExpiredOrphanAndPreservesNewerUploadAgainstPostgres18(
	t *testing.T,
) {
	database := testutil.OpenPostgres(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	user := model.User{
		Email: "pending-expiration@fdueat.com", PasswordHash: "x", Name: "过期回收测试",
	}
	require.NoError(t, database.GORM.Create(&user).Error)
	createPending := func(objectKey string, createdAt time.Time) model.ImageAsset {
		size := int64(128)
		asset := model.ImageAsset{
			UploaderID: &user.ID, Purpose: model.ImagePurposePost, ObjectKey: objectKey,
			PublicURL: "https://img.example.test/" + objectKey, ContentType: "image/jpeg",
			Size: &size, Status: model.ImageStatusPending,
			Moderation: model.ModerationStatusPending, CreatedAt: createdAt, UpdatedAt: createdAt,
		}
		require.NoError(t, database.GORM.Create(&asset).Error)
		return asset
	}
	expired := createPending("pending-expiration/expired.jpg", now.Add(-24*time.Hour-time.Second))
	newer := createPending("pending-expiration/newer.jpg", now.Add(-24*time.Hour+time.Second))
	storage := testutil.NewMockImageStorage()
	uploads := service.NewUploadService(storage, nil, nil, 10*1024*1024, 10*time.Minute, time.Hour)
	worker := service.NewPendingUploadExpirationWorker(
		database.DB, uploads, service.PendingUploadExpirationWorkerOptions{
			Retention: 24 * time.Hour,
			BatchSize: 10,
			Now:       func() time.Time { return now },
		},
	)

	result, err := worker.RunBatch(context.Background())
	require.NoError(t, err)
	require.Equal(t, now.Add(-24*time.Hour), result.Before)
	require.Equal(t, 1, result.Selected)
	require.Equal(t, 1, result.Retired)
	require.Empty(t, result.Failures)
	require.Equal(t, []string{expired.ObjectKey}, storage.DeleteCalls())
	require.NoError(t, database.GORM.First(&expired, expired.ID).Error)
	require.Equal(t, model.ImageStatusRetired, expired.Status)
	require.NoError(t, database.GORM.First(&newer, newer.ID).Error)
	require.Equal(t, model.ImageStatusPending, newer.Status)
}
