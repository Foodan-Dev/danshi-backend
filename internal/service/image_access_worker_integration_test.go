package service_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestImageAccessWorkerReachesEdgeOneTerminalSuccessAgainstPostgres18(t *testing.T) {
	database := testutil.OpenPostgres(t)
	storage := testutil.NewMockImageStorage()
	provider := &imageAccessProviderFake{
		submission: service.ImageCachePurgeSubmission{JobID: "edgeone-success"},
		describe:   service.ImageCachePurgeSuccess,
	}
	now := time.Now().UTC()
	asset := seedImageAccessDelivery(t, database, storage, "worker-success", false, true, now)
	worker := service.NewImageAccessWorker(database.DB, storage, provider, service.ImageAccessWorkerOptions{
		BatchSize: 1, Now: func() time.Time { return now },
	})

	result, err := worker.RunBatch(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Claimed)
	require.Equal(t, 1, result.Rescheduled)
	var delivery model.ImageAccessDelivery
	require.NoError(t, database.GORM.First(&delivery, asset.ID).Error)
	require.Equal(t, model.ImageAccessSubmitted, delivery.State)
	require.Equal(t, "edgeone-success", *delivery.ProviderJobID)
	require.Len(t, storage.AccessCalls(), 1)
	require.False(t, storage.AccessCalls()[0].Public)
	_, readErr := storage.ReadPublicURL(asset.PublicURL)
	require.ErrorIs(t, readErr, testutil.ErrMockPublicAccessDenied)

	now = now.Add(3 * time.Second)
	result, err = worker.RunBatch(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Succeeded)
	require.NoError(t, database.GORM.First(&delivery, asset.ID).Error)
	require.Equal(t, model.ImageAccessSucceeded, delivery.State)
	require.NotNil(t, delivery.CompletedAt)
	require.Equal(t, 1, provider.submitCalls())
	require.Equal(t, 1, provider.describeCalls())
}

func TestImageAccessWorkerDoesNotReplayAmbiguousCreateAgainstPostgres18(t *testing.T) {
	database := testutil.OpenPostgres(t)
	storage := testutil.NewMockImageStorage()
	provider := &imageAccessProviderFake{
		submitErr: service.NewImageCachePurgeError(service.ImageCachePurgeErrorUnknown),
		recovery:  service.ImageCachePurgeRecovery{Ambiguous: true},
	}
	now := time.Now().UTC()
	asset := seedImageAccessDelivery(t, database, storage, "worker-unknown", false, true, now)
	worker := service.NewImageAccessWorker(database.DB, storage, provider, service.ImageAccessWorkerOptions{
		BatchSize: 1, UnknownGrace: 5 * time.Second, Now: func() time.Time { return now },
	})

	result, err := worker.RunBatch(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Rescheduled)
	var delivery model.ImageAccessDelivery
	require.NoError(t, database.GORM.First(&delivery, asset.ID).Error)
	require.Equal(t, model.ImageAccessSubmitting, delivery.State)
	require.Equal(t, "submit_unknown", *delivery.LastErrorCode)
	require.Equal(t, 1, provider.submitCalls())

	now = now.Add(10 * time.Second)
	result, err = worker.RunBatch(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.DeadLettered)
	require.NoError(t, database.GORM.First(&delivery, asset.ID).Error)
	require.Equal(t, model.ImageAccessDeadLetter, delivery.State)
	require.Equal(t, "discover_exhausted", *delivery.LastErrorCode)
	require.Equal(t, 1, provider.submitCalls(), "response unknown 后只能 Describe 对账，禁止重放 Create")
	require.Equal(t, 1, provider.recoverCalls())
}

func TestImageAccessWorkerRecoversAcceptedTaskAfterCrashAgainstPostgres18(t *testing.T) {
	database := testutil.OpenPostgres(t)
	storage := testutil.NewMockImageStorage()
	provider := &imageAccessProviderFake{
		recovery: service.ImageCachePurgeRecovery{Found: true, EffectSucceeded: true},
	}
	now := time.Now().UTC()
	asset := seedImageAccessDelivery(t, database, storage, "worker-crash", false, true, now)
	startedAt := now.Add(-20 * time.Second)
	require.NoError(t, database.GORM.Model(&model.ImageAccessDelivery{}).
		Where("image_asset_id = ?", asset.ID).Updates(map[string]any{
		"state": model.ImageAccessSubmitting, "submit_attempts": 1,
		"submission_started_at": startedAt, "next_attempt_at": now,
	}).Error)
	worker := service.NewImageAccessWorker(database.DB, storage, provider, service.ImageAccessWorkerOptions{
		BatchSize: 1, Now: func() time.Time { return now },
	})

	result, err := worker.RunBatch(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Succeeded)
	var delivery model.ImageAccessDelivery
	require.NoError(t, database.GORM.First(&delivery, asset.ID).Error)
	require.Equal(t, model.ImageAccessSucceeded, delivery.State)
	require.Equal(t, 0, provider.submitCalls(), "submitting 租约恢复必须先对账")
	require.Equal(t, 1, provider.recoverCalls())
}

func TestImageAccessWorkerSkipsPurgeAfterPublishingNeverExposedAssetAgainstPostgres18(t *testing.T) {
	database := testutil.OpenPostgres(t)
	storage := testutil.NewMockImageStorage()
	provider := &imageAccessProviderFake{}
	now := time.Now().UTC()
	asset := seedImageAccessDelivery(t, database, storage, "worker-no-purge", true, false, now)
	worker := service.NewImageAccessWorker(database.DB, storage, provider, service.ImageAccessWorkerOptions{
		BatchSize: 1, Now: func() time.Time { return now },
	})

	result, err := worker.RunBatch(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Claimed)
	require.Equal(t, 1, result.Succeeded)
	var delivery model.ImageAccessDelivery
	require.NoError(t, database.GORM.First(&delivery, asset.ID).Error)
	require.Equal(t, model.ImageAccessSucceeded, delivery.State)
	require.False(t, delivery.PurgeRequired)
	require.NotNil(t, delivery.CompletedAt)
	require.Equal(t, []testutil.StorageAccessCall{{
		ObjectKey: asset.ObjectKey, Public: true,
	}}, storage.AccessCalls(), "即使不刷新 CDN，也必须先把对象 ACL 设为公开")
	require.Zero(t, provider.submitCalls())
	require.Zero(t, provider.describeCalls())
	require.Zero(t, provider.recoverCalls())
}

func seedImageAccessDelivery(
	t *testing.T,
	database *testutil.TestDatabase,
	storage *testutil.MockImageStorage,
	suffix string,
	desiredPublic bool,
	purgeRequired bool,
	now time.Time,
) model.ImageAsset {
	t.Helper()
	user := model.User{
		Email: suffix + "@fdueat.com", PasswordHash: "x", Name: strings.ReplaceAll(suffix, "-", "_"),
	}
	require.NoError(t, database.GORM.Create(&user).Error)
	size := int64(128)
	moderation := model.ModerationStatusBlock
	if desiredPublic {
		moderation = model.ModerationStatusPass
	}
	asset := model.ImageAsset{
		UploaderID: &user.ID, Purpose: model.ImagePurposePost,
		ObjectKey:   suffix + "/image.jpg",
		PublicURL:   "https://img.example.test/" + suffix + "/image.jpg",
		ContentType: "image/jpeg", Size: &size, Status: model.ImageStatusPending,
		Moderation: moderation,
	}
	require.NoError(t, database.GORM.Create(&asset).Error)
	storage.PutObject(asset.ObjectKey, testutil.StoredObject{ContentLength: size})
	storage.SetPublicURL(asset.ObjectKey, asset.PublicURL)
	verdict := model.ModerationVerdictBlock
	if desiredPublic {
		verdict = model.ModerationVerdictPass
	}
	jobID := suffix + "-moderation"
	record := model.ModerationRecord{
		ImageAssetID: &asset.ID, Scene: model.ModerationSceneImage,
		Provider: model.ModerationProviderTencentCI, ProviderJobID: &jobID,
		Verdict: verdict, Labels: []string{}, CreatedAt: now,
	}
	require.NoError(t, database.GORM.Create(&record).Error)
	outbox := repository.ImageAccessOutboxRepository{}
	require.NoError(t, database.DB.RunInTx(context.Background(), func(txCtx context.Context) error {
		_, err := outbox.Enqueue(
			txCtx, asset.ID, record.ID, desiredPublic, purgeRequired, now,
		)
		return err
	}))
	return asset
}

type imageAccessProviderFake struct {
	mu sync.Mutex

	submission  service.ImageCachePurgeSubmission
	submitErr   error
	describe    service.ImageCachePurgeTaskState
	describeErr error
	recovery    service.ImageCachePurgeRecovery
	recoverErr  error

	submits   int
	describes int
	recovers  int
}

func (f *imageAccessProviderFake) Submit(
	context.Context,
	string,
) (service.ImageCachePurgeSubmission, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submits++
	return f.submission, f.submitErr
}

func (f *imageAccessProviderFake) Describe(
	context.Context,
	string,
	string,
) (service.ImageCachePurgeTaskState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.describes++
	return f.describe, f.describeErr
}

func (f *imageAccessProviderFake) Recover(
	context.Context,
	string,
	time.Time,
	time.Time,
) (service.ImageCachePurgeRecovery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recovers++
	return f.recovery, f.recoverErr
}

func (f *imageAccessProviderFake) submitCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.submits
}

func (f *imageAccessProviderFake) describeCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.describes
}

func (f *imageAccessProviderFake) recoverCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.recovers
}
