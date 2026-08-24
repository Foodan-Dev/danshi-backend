package legacymigration

import (
	"context"
	"database/sql"
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
	settings, err := inspectTransaction(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		return nil, TransactionInspection{}, err
	}
	if settings.Isolation != "repeatable read" || !settings.ReadOnly || !settings.Deferrable {
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
		if err := tx.QueryRowContext(ctx, "SELECT pg_try_advisory_xact_lock($1)", AdvisoryLockKey).Scan(&acquired); err != nil {
			_ = tx.Rollback()
			return nil, TransactionInspection{}, gateError("target_lock_failed", "无法获取迁移 advisory lock")
		}
		if !acquired {
			_ = tx.Rollback()
			return nil, TransactionInspection{}, gateError("target_lock_busy", "另一迁移勘察或执行进程正持有目标锁")
		}
	}
	settings, err := inspectTransaction(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		return nil, TransactionInspection{}, err
	}
	if settings.Isolation != "repeatable read" || !settings.ReadOnly {
		_ = tx.Rollback()
		return nil, TransactionInspection{}, gateError("target_snapshot_mode_mismatch", "目标事务未满足 REPEATABLE READ READ ONLY")
	}
	return tx, settings, nil
}

func inspectTransaction(ctx context.Context, tx *sql.Tx) (TransactionInspection, error) {
	var isolation, readOnly, deferrable string
	err := tx.QueryRowContext(ctx, `SELECT current_setting('transaction_isolation'),
current_setting('transaction_read_only'), current_setting('transaction_deferrable')`).
		Scan(&isolation, &readOnly, &deferrable)
	if err != nil {
		return TransactionInspection{}, gateError("transaction_mode_inspection_failed", "无法核验数据库事务模式")
	}
	return TransactionInspection{
		Isolation: isolation, ReadOnly: readOnly == "on", Deferrable: deferrable == "on",
	}, nil
}
