package db_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	dbinfra "github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestImageLifecycleMigrationRepairsOnlyUnreferencedReadyAssets(t *testing.T) {
	database := testutil.OpenPostgres(t)
	ctx := context.Background()
	version, err := dbinfra.Version(ctx, database.SQL)
	require.NoError(t, err)
	for version > 5 {
		require.NoError(t, dbinfra.DownOne(ctx, database.SQL))
		version, err = dbinfra.Version(ctx, database.SQL)
		require.NoError(t, err)
	}
	require.EqualValues(t, 5, version,
		"图片生命周期修复必须从 00006 之前的 schema 装载测试数据")

	user := model.User{Email: "image-migration@fdueat.com", PasswordHash: "x", Name: "migration"}
	require.NoError(t, database.GORM.Create(&user).Error)
	size := int64(1024)
	orphan := model.ImageAsset{
		UploaderID: &user.ID, Purpose: model.ImagePurposePost,
		ObjectKey: "migration/orphan.jpg", PublicURL: "https://img.example.test/migration/orphan.jpg",
		ContentType: "image/jpeg", Size: &size, Status: model.ImageStatusReady,
		Moderation: model.ModerationStatusPending,
	}
	referenced := model.ImageAsset{
		UploaderID: &user.ID, Purpose: model.ImagePurposeAvatar,
		ObjectKey: "migration/avatar.jpg", PublicURL: "https://img.example.test/migration/avatar.jpg",
		ContentType: "image/jpeg", Size: &size, Status: model.ImageStatusReady,
		Moderation: model.ModerationStatusPending,
	}
	require.NoError(t, database.GORM.Create(&orphan).Error)
	require.NoError(t, database.GORM.Create(&referenced).Error)
	require.NoError(t, database.GORM.Model(&user).
		Update("avatar_image_asset_id", referenced.ID).Error)

	require.NoError(t, dbinfra.Up(ctx, database.SQL))
	require.NoError(t, database.GORM.First(&orphan, orphan.ID).Error)
	require.NoError(t, database.GORM.First(&referenced, referenced.ID).Error)
	require.Equal(t, model.ImageStatusPending, orphan.Status)
	require.Equal(t, model.ImageStatusReady, referenced.Status)
}
