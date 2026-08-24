package db_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbinfra "github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestRunInReadOnlyTxEnforcesModeAndStatementTimeout(t *testing.T) {
	database := testutil.OpenPostgres(t)
	ctx := context.Background()

	type settings struct {
		ReadOnly         string
		StatementTimeout string
	}
	observed := settings{}
	err := database.DB.RunInReadOnlyTx(ctx, 250*time.Millisecond, func(txCtx context.Context) error {
		return dbinfra.FromContext(txCtx).Raw(`
			SELECT
				current_setting('transaction_read_only') AS read_only,
				current_setting('statement_timeout') AS statement_timeout
		`).Scan(&observed).Error
	})
	require.NoError(t, err)
	require.Equal(t, "on", observed.ReadOnly)
	require.Equal(t, "250ms", observed.StatementTimeout)

	err = database.DB.RunInReadOnlyTx(ctx, 250*time.Millisecond, func(txCtx context.Context) error {
		return dbinfra.FromContext(txCtx).Exec(
			"INSERT INTO tags (name) VALUES (?)", "roprobe",
		).Error
	})
	require.ErrorContains(t, err, "read-only transaction")

	started := time.Now()
	err = database.DB.RunInReadOnlyTx(ctx, 20*time.Millisecond, func(txCtx context.Context) error {
		return dbinfra.FromContext(txCtx).Exec("SELECT pg_sleep(0.2)").Error
	})
	require.ErrorContains(t, err, "statement timeout")
	require.Less(t, time.Since(started), 150*time.Millisecond)
}
