package legacymigration

import (
	"context"
	"database/sql"
)

const (
	targetAdvisoryLockSQL   = "SELECT pg_catalog.pg_try_advisory_xact_lock($1)"
	setSafeSearchPathSQL    = "SET LOCAL search_path = pg_catalog"
	setCanonicalTimeZoneSQL = "SET LOCAL TIME ZONE 'UTC'"
)

func beginSourceSnapshot(ctx context.Context, database *sql.DB) (*sql.Tx, TransactionInspection, error) {
	tx, err := database.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelRepeatableRead,
		ReadOnly:  true,
	})
	if err != nil {
		return nil, TransactionInspection{}, gateError("source_snapshot_failed", "无法建立来源库一致性快照")
	}
	if _, err := tx.ExecContext(ctx, "SET TRANSACTION DEFERRABLE"); err != nil {
		_ = tx.Rollback()
		return nil, TransactionInspection{}, gateError("source_snapshot_mode_failed", "无法启用来源库 DEFERRABLE 只读快照")
	}
	if _, err := tx.ExecContext(ctx, setSafeSearchPathSQL); err != nil {
		_ = tx.Rollback()
		return nil, TransactionInspection{}, gateError("source_search_path_failed", "无法固定来源事务 search_path")
	}
	if _, err := tx.ExecContext(ctx, setCanonicalTimeZoneSQL); err != nil {
		_ = tx.Rollback()
		return nil, TransactionInspection{}, gateError("source_timezone_failed", "无法固定来源事务时区")
	}
	if err := verifyCanonicalTimeZone(ctx, tx, "source"); err != nil {
		_ = tx.Rollback()
		return nil, TransactionInspection{}, err
	}
	settings, err := inspectTransaction(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		return nil, TransactionInspection{}, err
	}
	if settings.Isolation != "repeatable read" || !settings.ReadOnly || !settings.Deferrable ||
		settings.SearchPath != "pg_catalog" {
		_ = tx.Rollback()
		return nil, TransactionInspection{}, gateError("source_snapshot_mode_mismatch", "来源事务未满足 REPEATABLE READ READ ONLY DEFERRABLE")
	}
	return tx, settings, nil
}

func beginTargetInspection(ctx context.Context, database *sql.DB, lock bool) (*sql.Tx, TransactionInspection, error) {
	tx, err := database.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelRepeatableRead,
		ReadOnly:  true,
	})
	if err != nil {
		return nil, TransactionInspection{}, gateError("target_snapshot_failed", "无法建立目标库只读快照")
	}
	// advisory lock 必须是事务里的第一条查询。REPEATABLE READ 在第一条查询建立快照；
	// 如果先读 GUC 再取锁，锁交接期间提交的业务写入可能落在旧快照之外。
	if lock {
		var acquired bool
		if err := tx.QueryRowContext(ctx, targetAdvisoryLockSQL, AdvisoryLockKey).Scan(&acquired); err != nil {
			_ = tx.Rollback()
			return nil, TransactionInspection{}, gateError("target_lock_failed", "无法获取迁移 advisory lock")
		}
		if !acquired {
			_ = tx.Rollback()
			return nil, TransactionInspection{}, gateError("target_lock_busy", "另一迁移勘察或执行进程正持有目标锁")
		}
	}
	if _, err := tx.ExecContext(ctx, setSafeSearchPathSQL); err != nil {
		_ = tx.Rollback()
		return nil, TransactionInspection{}, gateError("target_search_path_failed", "无法固定目标事务 search_path")
	}
	if _, err := tx.ExecContext(ctx, setCanonicalTimeZoneSQL); err != nil {
		_ = tx.Rollback()
		return nil, TransactionInspection{}, gateError("target_timezone_failed", "无法固定目标事务时区")
	}
	if err := verifyCanonicalTimeZone(ctx, tx, "target"); err != nil {
		_ = tx.Rollback()
		return nil, TransactionInspection{}, err
	}
	settings, err := inspectTransaction(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		return nil, TransactionInspection{}, err
	}
	if settings.Isolation != "repeatable read" || !settings.ReadOnly ||
		settings.SearchPath != "pg_catalog" {
		_ = tx.Rollback()
		return nil, TransactionInspection{}, gateError("target_snapshot_mode_mismatch", "目标事务未满足 REPEATABLE READ READ ONLY")
	}
	return tx, settings, nil
}

func verifyCanonicalTimeZone(ctx context.Context, tx *sql.Tx, side string) error {
	var timezone string
	if err := tx.QueryRowContext(ctx, "SELECT pg_catalog.current_setting('TimeZone')").Scan(&timezone); err != nil {
		return gateError(side+"_timezone_inspection_failed", "无法核验事务时区")
	}
	if timezone != "UTC" {
		return gateError(side+"_timezone_mismatch", "事务时区未固定为 UTC")
	}
	return nil
}

func inspectTransaction(ctx context.Context, tx *sql.Tx) (TransactionInspection, error) {
	var isolation, readOnly, deferrable, searchPath string
	err := tx.QueryRowContext(ctx, `SELECT pg_catalog.current_setting('transaction_isolation'),
pg_catalog.current_setting('transaction_read_only'), pg_catalog.current_setting('transaction_deferrable'),
pg_catalog.current_setting('search_path')`).
		Scan(&isolation, &readOnly, &deferrable, &searchPath)
	if err != nil {
		return TransactionInspection{}, gateError("transaction_mode_inspection_failed", "无法核验数据库事务模式")
	}
	return TransactionInspection{
		Isolation: isolation, ReadOnly: readOnly == "on", Deferrable: deferrable == "on",
		SearchPath: searchPath,
	}, nil
}
