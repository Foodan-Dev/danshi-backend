package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	dbinfra "github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/usernamepolicy"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestUsernameSpacesUpgradeFromOriginal21(t *testing.T) {
	database := testutil.OpenPostgres(t)
	ctx := context.Background()
	require.NoError(t, dbinfra.DownOne(ctx, database.SQL))
	// 还原旧版 21 的字符策略，验证升级不依赖重跑已应用的迁移 18。
	oldPattern := strings.ReplaceAll(usernamepolicy.SQLPattern(), `\0020`, "")
	_, err := database.SQL.ExecContext(ctx, `CREATE OR REPLACE FUNCTION danshi_valid_username_characters(value text)
 RETURNS boolean LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
 RETURN (value COLLATE "C") ~ U&'`+oldPattern+`';`)
	require.NoError(t, err)
	var allowed bool
	require.NoError(t, database.SQL.QueryRowContext(ctx, "SELECT danshi_valid_username_characters('Ut dolore')").Scan(&allowed))
	require.False(t, allowed)
	require.NoError(t, dbinfra.Up(ctx, database.SQL))
	require.NoError(t, database.SQL.QueryRowContext(ctx, "SELECT danshi_valid_username_characters('Ut dolore')").Scan(&allowed))
	require.True(t, allowed)
	_, err = database.SQL.ExecContext(ctx, "INSERT INTO users(email,password_hash,name) VALUES ('spaces-upgrade@fdueat.com','x','Ut dolore')")
	require.NoError(t, err)
	_, err = database.SQL.ExecContext(ctx, "INSERT INTO users(email,password_hash,name) VALUES ('spaces-double@fdueat.com','x','Ut  dolore')")
	require.Error(t, err)
	require.NoError(t, dbinfra.DownOne(ctx, database.SQL))
	require.NoError(t, dbinfra.Up(ctx, database.SQL))
	var name string
	require.NoError(t, database.SQL.QueryRowContext(ctx, "SELECT name FROM users WHERE email='spaces-upgrade@fdueat.com'").Scan(&name))
	require.Equal(t, "Ut dolore", name)
}
