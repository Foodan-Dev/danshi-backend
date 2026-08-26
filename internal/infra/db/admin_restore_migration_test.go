package db_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	dbinfra "github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestAdminRestoreReviewerMigrationRoundTripAndConstraints(t *testing.T) {
	database := testutil.OpenPostgres(t)
	ctx := context.Background()

	version, err := dbinfra.Version(ctx, database.SQL)
	require.NoError(t, err)
	for version > 6 {
		require.NoError(t, dbinfra.DownOne(ctx, database.SQL))
		version, err = dbinfra.Version(ctx, database.SQL)
		require.NoError(t, err)
	}
	require.EqualValues(t, 6, version)

	user := model.User{Email: "restore-migration@fdueat.com", PasswordHash: "x", Name: "迁移操作人"}
	require.NoError(t, database.GORM.Create(&user).Error)
	tag := model.Tag{Name: "迁移恢复", Moderation: model.ModerationStatusPending}
	require.NoError(t, database.GORM.Create(&tag).Error)
	// 存量恢复流水的操作人写在 raw_response 里，但 moderation_records 是
	// append-only（触发器禁止 UPDATE），回填就得关掉审计表的不可覆盖写保护。
	// 因此约束对 admin_restore 保持宽松，存量行的 reviewer_id 仍为空。
	legacy := model.ModerationRecord{
		TagID: &tag.ID, Scene: model.ModerationSceneText,
		Provider: "admin_restore", Verdict: model.ModerationVerdictPass,
		Labels: pq.StringArray{}, RawResponse: json.RawMessage(`{"action":"restore"}`),
	}
	require.NoError(t, database.GORM.Create(&legacy).Error,
		"v6 允许的无操作人恢复流水必须可作为存量数据迁移")

	require.NoError(t, dbinfra.Up(ctx, database.SQL))
	version, err = dbinfra.Version(ctx, database.SQL)
	require.NoError(t, err)
	require.Equal(t, dbinfra.ExpectedVersion, version)
	var migratedLegacy model.ModerationRecord
	require.NoError(t, database.GORM.First(&migratedLegacy, legacy.ID).Error)
	require.Nil(t, migratedLegacy.ReviewerID)
	require.Nil(t, migratedLegacy.ReviewedAt)

	version, err = dbinfra.Version(ctx, database.SQL)
	require.NoError(t, err)
	for version > 7 {
		require.NoError(t, dbinfra.DownOne(ctx, database.SQL))
		version, err = dbinfra.Version(ctx, database.SQL)
		require.NoError(t, err)
	}
	require.NoError(t, dbinfra.DownOne(ctx, database.SQL))
	require.NoError(t, dbinfra.Up(ctx, database.SQL), "迁移必须支持无损 down / up 重放")

	now := time.Now().UTC()
	auditable := model.ModerationRecord{
		TagID: &tag.ID, Scene: model.ModerationSceneText,
		Provider: "admin_restore", Verdict: model.ModerationVerdictPass,
		Labels: pq.StringArray{}, RawResponse: json.RawMessage(`{"action":"restore"}`),
		ReviewerID: &user.ID, ReviewedAt: &now,
	}
	require.NoError(t, database.GORM.Create(&auditable).Error)
	var byReviewer model.ModerationRecord
	require.NoError(t, database.GORM.Where("reviewer_id = ?", user.ID).First(&byReviewer).Error)
	require.Equal(t, auditable.ID, byReviewer.ID)

	machineWithReviewer := model.ModerationRecord{
		TagID: &tag.ID, Scene: model.ModerationSceneText,
		Provider: "machine_constraint_test", Verdict: model.ModerationVerdictPass,
		Labels: pq.StringArray{}, ReviewerID: &user.ID, ReviewedAt: &now,
	}
	err = database.GORM.Create(&machineWithReviewer).Error
	require.ErrorContains(t, err, "mr_manual_shape_check",
		"机器记录仍必须拒绝 reviewer_id 与 reviewed_at")

	version, err = dbinfra.Version(ctx, database.SQL)
	require.NoError(t, err)
	for version > 7 {
		require.NoError(t, dbinfra.DownOne(ctx, database.SQL))
		version, err = dbinfra.Version(ctx, database.SQL)
		require.NoError(t, err)
	}
	err = dbinfra.DownOne(ctx, database.SQL)
	require.ErrorContains(t, err,
		"cannot restore mr_manual_shape_check: non-manual moderation records carry reviewer metadata",
		"存在新形态流水时 down 必须显式失败，不能静默丢弃操作人")
	version, versionErr := dbinfra.Version(ctx, database.SQL)
	require.NoError(t, versionErr)
	require.EqualValues(t, 7, version)
	require.NoError(t, dbinfra.Up(ctx, database.SQL))
}
