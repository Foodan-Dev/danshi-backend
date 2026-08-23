package db_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	dbinfra "github.com/jingyijun/danshi_backend_go/internal/infra/db"
	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/testutil"
)

func TestRBACMigrationMapsLegacyRolesAndDropsSourceColumn(t *testing.T) {
	database := testutil.OpenPostgres(t)
	ctx := context.Background()
	// 回到 v2 才能装载仍含 users.role 的旧 schema 数据：v4 是搜索索引，v3 才是 RBAC 迁移。
	require.NoError(t, dbinfra.DownOne(ctx, database.SQL))
	require.NoError(t, dbinfra.DownOne(ctx, database.SQL))

	_, err := database.SQL.ExecContext(ctx, `
		INSERT INTO users (id, email, password_hash, name, role) VALUES
			(10001, 'migration-user@fdueat.com', 'x', 'user', 'user'),
			(10002, 'migration-admin@fdueat.com', 'x', 'admin', 'admin'),
			(10003, 'migration-super@fdueat.com', 'x', 'super', 'super_admin')
	`)
	require.NoError(t, err)
	require.NoError(t, dbinfra.Up(ctx, database.SQL))

	version, err := dbinfra.Version(ctx, database.SQL)
	require.NoError(t, err)
	require.Equal(t, dbinfra.ExpectedVersion, version)

	var roleColumnCount int
	require.NoError(t, database.SQL.QueryRowContext(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'users' AND column_name = 'role'
	`).Scan(&roleColumnCount))
	require.Zero(t, roleColumnCount, "users.role 必须被迁走，不能与 user_roles 并存")

	type binding struct {
		UserID uint64
		Role   model.UserRole
	}
	var bindings []binding
	require.NoError(t, database.GORM.Table("user_roles").Where("user_id >= ?", 10001).
		Order("user_id, role").Find(&bindings).Error)
	require.Equal(t, []binding{
		{UserID: 10002, Role: model.UserRoleModerator},
		{UserID: 10003, Role: model.UserRoleSuperAdmin},
	}, bindings)

	var records []model.UserRoleRecord
	require.NoError(t, database.GORM.Where("user_id >= ?", 10001).Order("user_id").Find(&records).Error)
	require.Len(t, records, 2)
	require.Nil(t, records[0].ActorID)
	require.Nil(t, records[1].ActorID)
}
