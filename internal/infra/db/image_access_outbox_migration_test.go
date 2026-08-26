package db_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	dbinfra "github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestImageAccessOutboxMigrationGrandfathersTerminalAssets(t *testing.T) {
	database := testutil.OpenPostgres(t)
	ctx := context.Background()

	version, err := dbinfra.Version(ctx, database.SQL)
	require.NoError(t, err)
	for version > 11 {
		require.NoError(t, dbinfra.DownOne(ctx, database.SQL))
		version, err = dbinfra.Version(ctx, database.SQL)
		require.NoError(t, err)
	}
	require.EqualValues(t, 11, version)

	require.NoError(t, database.GORM.Exec(`
INSERT INTO users (id, email, password_hash, name)
VALUES (9101, 'outbox-migration@fdueat.com', 'x', 'outbox migration');

INSERT INTO image_assets
    (id, uploader_id, purpose, object_key, public_url, content_type, status, moderation)
VALUES
    (9201, 9101, 'post', 'migration/pass.jpg',
     'https://img.example.test/migration/pass.jpg', 'image/jpeg', 'ready', 'pass'),
    (9202, 9101, 'post', 'migration/block.jpg',
     'https://img.example.test/migration/block.jpg', 'image/jpeg', 'ready', 'block'),
    (9203, 9101, 'post', 'migration/pending.jpg',
     '', 'image/jpeg', 'pending', 'pending'),
    (9204, 9101, 'post', 'migration/retired.jpg',
     'urn:danshi:image-asset:9204:retired', 'image/jpeg', 'retired', 'block'),
    (9205, 9101, 'post', 'migration/mismatch.jpg',
     'https://img.example.test/migration/mismatch.jpg', 'image/jpeg', 'ready', 'review');

INSERT INTO moderation_records
    (id, image_asset_id, scene, provider, provider_job_id, verdict, labels, created_at)
VALUES
    (9301, 9201, 'image', 'tencent_ci', 'migration-review-pass', 'review', '{}', now() - interval '2 minutes'),
    (9302, 9202, 'image', 'tencent_ci', 'migration-block', 'block', '{}', now()),
    (9303, 9204, 'image', 'tencent_ci', 'migration-retired', 'block', '{}', now()),
    (9304, 9205, 'image', 'tencent_ci', 'migration-mismatch', 'pass', '{}', now());

INSERT INTO moderation_records
    (id, image_asset_id, scene, provider, verdict, labels,
     reviewer_id, reviewed_at, supersedes_id, created_at)
VALUES
    (9305, 9201, 'image', 'manual', 'pass', '{}',
     9101, now(), 9301, now() - interval '1 minute');
`).Error)

	err = dbinfra.Up(ctx, database.SQL)
	require.ErrorContains(t, err,
		"cannot grandfather image access outbox: terminal asset lacks matching moderation fact")
	version, err = dbinfra.Version(ctx, database.SQL)
	require.NoError(t, err)
	require.EqualValues(t, 11, version, "失败的 grandfather 必须整体回滚")

	require.NoError(t, database.GORM.Exec(`
INSERT INTO moderation_records
    (id, image_asset_id, scene, provider, provider_job_id, verdict, labels)
VALUES (9306, 9205, 'image', 'tencent_ci', 'migration-review', 'review', '{}')
`).Error)
	require.NoError(t, dbinfra.Up(ctx, database.SQL))

	var intents []model.ImageAccessIntent
	require.NoError(t, database.GORM.Order("source_moderation_record_id").Find(&intents).Error)
	require.Len(t, intents, 6, "所有历史图片审核事实都必须 grandfather，保证旧回调幂等")
	intentBySource := make(map[uint64]model.ImageAccessIntent, len(intents))
	intentByID := make(map[uint64]model.ImageAccessIntent, len(intents))
	for _, intent := range intents {
		intentBySource[intent.SourceModerationRecordID] = intent
		intentByID[intent.ID] = intent
	}
	require.False(t, intentBySource[9301].DesiredPublic, "旧 machine review 也必须保留幂等 intent")
	require.True(t, intentBySource[9305].DesiredPublic)
	require.Equal(t, uint64(9201), intentBySource[9305].ImageAssetID)
	require.False(t, intentBySource[9302].DesiredPublic)
	require.False(t, intentBySource[9306].DesiredPublic)

	var deliveries []model.ImageAccessDelivery
	require.NoError(t, database.GORM.Order("image_asset_id").Find(&deliveries).Error)
	require.Len(t, deliveries, 3, "pending 与已经物理清理的墓碑资产不得进入交付队列")
	for _, delivery := range deliveries {
		intent := intentByID[delivery.DesiredIntentID]
		require.Equal(t, intent.ImageAssetID, delivery.ImageAssetID)
		require.Equal(t, intent.DesiredPublic, delivery.DesiredPublic)
		require.True(t, delivery.PurgeRequired,
			"历史状态转换不可重建时必须保守刷新，避免漏掉已缓存的公开响应或 403")
		require.Equal(t, model.ImageAccessPendingACL, delivery.State)
	}
	require.Equal(t, intentBySource[9305].ID, deliveries[0].DesiredIntentID,
		"pass delivery 应锚定与当前状态一致的最新人工事实")
	require.Equal(t, intentBySource[9302].ID, deliveries[1].DesiredIntentID)
	require.Equal(t, intentBySource[9306].ID, deliveries[2].DesiredIntentID)

	err = database.GORM.Exec(`
UPDATE image_access_deliveries
SET desired_public = NOT desired_public
WHERE image_asset_id = 9201
`).Error
	require.ErrorContains(t, err, "fk_image_access_delivery_intent")
	err = database.GORM.Exec(`
UPDATE image_access_deliveries
SET image_asset_id = 9203
WHERE image_asset_id = 9201
`).Error
	require.ErrorContains(t, err, "fk_image_access_delivery_intent")

	version, err = dbinfra.Version(ctx, database.SQL)
	require.NoError(t, err)
	for version > 11 {
		require.NoError(t, dbinfra.DownOne(ctx, database.SQL))
		version, err = dbinfra.Version(ctx, database.SQL)
		require.NoError(t, err)
	}
	require.NoError(t, dbinfra.Up(ctx, database.SQL), "grandfather 迁移必须可 down/up 重放")
	var replayed int64
	require.NoError(t, database.GORM.Model(&model.ImageAccessDelivery{}).Count(&replayed).Error)
	require.EqualValues(t, 3, replayed)
	var replayedPurgeRequired int64
	require.NoError(t, database.GORM.Model(&model.ImageAccessDelivery{}).
		Where("purge_required").Count(&replayedPurgeRequired).Error)
	require.EqualValues(t, 3, replayedPurgeRequired)
}
