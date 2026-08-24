package repository_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestImageAccessOutboxIdempotencyGenerationAndFencingAgainstPostgres18(t *testing.T) {
	database := testutil.OpenPostgres(t)
	ctx := context.Background()
	outbox := repository.ImageAccessOutboxRepository{}
	user := model.User{
		Email: "image-outbox@fdueat.com", PasswordHash: "x", Name: "outbox",
	}
	require.NoError(t, database.GORM.Create(&user).Error)
	size := int64(128)
	asset := model.ImageAsset{
		UploaderID: &user.ID, Purpose: model.ImagePurposePost,
		ObjectKey: "outbox/image.jpg", PublicURL: "https://img.example.test/outbox/image.jpg",
		ContentType: "image/jpeg", Size: &size, Status: model.ImageStatusPending,
		Moderation: model.ModerationStatusBlock,
	}
	require.NoError(t, database.GORM.Create(&asset).Error)
	block := imageModerationRecord(asset.ID, "outbox-block", model.ModerationVerdictBlock)
	require.NoError(t, database.GORM.Create(&block).Error)
	now := time.Now().UTC()
	require.NoError(t, database.DB.RunInTx(ctx, func(txCtx context.Context) error {
		inserted, err := outbox.Enqueue(txCtx, asset.ID, block.ID, false, true, now)
		require.True(t, inserted)
		return err
	}))

	var initial model.ImageAccessDelivery
	require.NoError(t, database.GORM.First(&initial, asset.ID).Error)
	require.EqualValues(t, 1, initial.Generation)
	require.True(t, initial.PurgeRequired)
	require.NoError(t, database.DB.RunInTx(ctx, func(txCtx context.Context) error {
		inserted, err := outbox.Enqueue(txCtx, asset.ID, block.ID, false, false, now.Add(time.Second))
		require.False(t, inserted)
		return err
	}))
	var duplicate model.ImageAccessDelivery
	require.NoError(t, database.GORM.First(&duplicate, asset.ID).Error)
	require.Equal(t, initial.Generation, duplicate.Generation)
	require.True(t, duplicate.PurgeRequired, "重复 source 不得改写既有交付策略")

	var oldClaim repository.ImageAccessDeliveryClaim
	require.NoError(t, database.DB.RunInTx(ctx, func(txCtx context.Context) error {
		claims, err := outbox.ClaimDue(txCtx, now.Add(time.Second), now.Add(time.Minute),
			"0123456789abcdef0123456789abcdef", 1)
		require.Len(t, claims, 1)
		oldClaim = claims[0]
		return err
	}))
	pass := imageModerationRecord(asset.ID, "outbox-pass", model.ModerationVerdictPass)
	require.NoError(t, database.GORM.Create(&pass).Error)
	require.NoError(t, database.DB.RunInTx(ctx, func(txCtx context.Context) error {
		inserted, err := outbox.Enqueue(txCtx, asset.ID, pass.ID, true, true, now.Add(2*time.Second))
		require.True(t, inserted)
		return err
	}))
	var latest model.ImageAccessDelivery
	require.NoError(t, database.GORM.First(&latest, asset.ID).Error)
	require.EqualValues(t, 2, latest.Generation)
	require.True(t, latest.DesiredPublic)
	require.True(t, latest.PurgeRequired)
	require.Equal(t, oldClaim.LeaseToken, latest.LeaseToken, "新代际必须保留旧 lease 防止并发外调")

	require.NoError(t, database.DB.RunInTx(ctx, func(txCtx context.Context) error {
		updated, err := outbox.UpdateClaim(txCtx, oldClaim, map[string]any{
			"state": model.ImageAccessSucceeded, "completed_at": now,
		})
		require.False(t, updated, "旧 generation 不得 finalize 新意图")
		return err
	}))
	require.NoError(t, database.DB.RunInTx(ctx, func(txCtx context.Context) error {
		return outbox.ReleaseSuperseded(
			txCtx, asset.ID, "0123456789abcdef0123456789abcdef", now.Add(3*time.Second),
		)
	}))
	require.NoError(t, database.GORM.First(&latest, asset.ID).Error)
	require.Nil(t, latest.LeaseToken)
	require.Equal(t, model.ImageAccessPendingACL, latest.State)
}

func TestImageAccessOutboxConcurrentClaimUsesSkipLockedAgainstPostgres18(t *testing.T) {
	database := testutil.OpenPostgres(t)
	ctx := context.Background()
	outbox := repository.ImageAccessOutboxRepository{}
	user := model.User{Email: "image-claim@fdueat.com", PasswordHash: "x", Name: "claim"}
	require.NoError(t, database.GORM.Create(&user).Error)
	size := int64(64)
	now := time.Now().UTC()
	for index := 0; index < 4; index++ {
		asset := model.ImageAsset{
			UploaderID: &user.ID, Purpose: model.ImagePurposePost,
			ObjectKey:   fmt.Sprintf("claim/%d.jpg", index),
			PublicURL:   fmt.Sprintf("https://img.example.test/claim/%d.jpg", index),
			ContentType: "image/jpeg", Size: &size, Status: model.ImageStatusPending,
			Moderation: model.ModerationStatusBlock,
		}
		require.NoError(t, database.GORM.Create(&asset).Error)
		record := imageModerationRecord(asset.ID, fmt.Sprintf("claim-%d", index), model.ModerationVerdictBlock)
		require.NoError(t, database.GORM.Create(&record).Error)
		require.NoError(t, database.DB.RunInTx(ctx, func(txCtx context.Context) error {
			_, err := outbox.Enqueue(txCtx, asset.ID, record.ID, false, true, now)
			return err
		}))
	}

	start := make(chan struct{})
	claimed := make(chan []repository.ImageAccessDeliveryClaim, 2)
	errors := make(chan error, 2)
	var workers sync.WaitGroup
	for index := 0; index < 2; index++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			<-start
			var rows []repository.ImageAccessDeliveryClaim
			err := database.DB.RunInTx(ctx, func(txCtx context.Context) error {
				var claimErr error
				rows, claimErr = outbox.ClaimDue(txCtx, now.Add(time.Second), now.Add(time.Minute),
					fmt.Sprintf("%032d", worker+1), 2)
				return claimErr
			})
			claimed <- rows
			errors <- err
		}(index)
	}
	close(start)
	workers.Wait()
	close(claimed)
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	seen := make(map[uint64]struct{}, 4)
	for rows := range claimed {
		require.Len(t, rows, 2)
		for _, row := range rows {
			_, duplicate := seen[row.ImageAssetID]
			require.False(t, duplicate, "SKIP LOCKED 不得重复领取同一图片")
			seen[row.ImageAssetID] = struct{}{}
		}
	}
	require.Len(t, seen, 4)
}

func imageModerationRecord(
	assetID uint64,
	jobID string,
	verdict model.ModerationVerdict,
) model.ModerationRecord {
	return model.ModerationRecord{
		ImageAssetID: &assetID, Scene: model.ModerationSceneImage,
		Provider: model.ModerationProviderTencentCI, ProviderJobID: &jobID,
		Verdict: verdict, Labels: []string{}, CreatedAt: time.Now().UTC(),
	}
}
