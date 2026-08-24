package legacymigration

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

// ParseMode 只接受单个 inspect 或 plan 参数，不回显未知参数内容。
func ParseMode(args []string) (Mode, error) {
	if len(args) != 1 {
		return "", gateError("invalid_cli_arguments", "必须且只能指定 inspect 或 plan；当前阶段不提供 apply")
	}
	mode := Mode(args[0])
	if mode != ModeInspect && mode != ModePlan {
		return "", gateError("unsupported_mode", "只支持 inspect 或 plan；当前阶段不提供 apply")
	}
	return mode, nil
}

// Run 对显式数据库连接执行只读勘察或计划；主要供隔离测试与受控编排调用。
func Run(ctx context.Context, source, target *sql.DB, mode Mode) (Report, error) {
	observation, err := inspectMigrationDatabases(ctx, source, target, mode)
	return observation.report, err
}

type migrationObservation struct {
	report                       Report
	sourceDatabaseIdentitySHA256 string
	targetDatabaseIdentitySHA256 string
	sourceSnapshotSHA256         string
}

func inspectMigrationDatabases(
	ctx context.Context,
	source, target *sql.DB,
	mode Mode,
) (migrationObservation, error) {
	if mode != ModeInspect && mode != ModePlan {
		return migrationObservation{}, gateError("unsupported_mode", "只支持 inspect 或 plan；当前阶段不提供 apply")
	}
	targetTx, targetTransaction, err := beginTargetInspection(ctx, target, mode == ModePlan)
	if err != nil {
		return migrationObservation{}, err
	}
	defer func() { _ = targetTx.Rollback() }()
	targetInspection, err := inspectTarget(ctx, targetTx, targetTransaction)
	if err != nil {
		return migrationObservation{}, err
	}
	sourceTx, sourceTransaction, err := beginSourceSnapshot(ctx, source)
	if err != nil {
		return migrationObservation{}, err
	}
	defer func() { _ = sourceTx.Rollback() }()
	sourceInspection, err := inspectSource(ctx, sourceTx, sourceTransaction)
	if err != nil {
		return migrationObservation{}, err
	}
	sourceIdentity, err := inspectDatabaseIdentity(ctx, sourceTx)
	if err != nil {
		return migrationObservation{}, err
	}
	targetIdentity, err := inspectDatabaseIdentity(ctx, targetTx)
	if err != nil {
		return migrationObservation{}, err
	}
	if sourceIdentity == targetIdentity {
		return migrationObservation{}, gateError("source_target_database_identical", "来源与目标连接解析到了同一个实际数据库")
	}
	sourceSnapshotSHA256, err := sourceDatasetSnapshotDigest(ctx, sourceTx)
	if err != nil {
		return migrationObservation{}, err
	}
	report := Report{
		SchemaVersion:   ReportSchemaVersion,
		InspectionLevel: "foundation_preflight",
		Mode:            mode,
		ApplyEnabled:    false,
		Source:          sourceInspection,
		Target:          targetInspection,
	}
	if mode == ModePlan {
		report.Plan = buildPlan(sourceInspection, targetInspection)
	}
	if err := sourceTx.Commit(); err != nil {
		return migrationObservation{}, gateError("source_snapshot_commit_failed", "无法正常结束来源只读快照")
	}
	if err := targetTx.Commit(); err != nil {
		return migrationObservation{}, gateError("target_snapshot_commit_failed", "无法正常结束目标只读快照")
	}
	return migrationObservation{
		report:                       report,
		sourceDatabaseIdentitySHA256: databaseIdentityDigest(sourceIdentity),
		targetDatabaseIdentitySHA256: databaseIdentityDigest(targetIdentity),
		sourceSnapshotSHA256:         sourceSnapshotSHA256,
	}, nil
}

func buildPlan(source SourceInspection, target TargetInspection) *MigrationPlan {
	return &MigrationPlan{
		LockKey:                  AdvisoryLockKey,
		Executable:               false,
		BaselineBlockersClear:    len(source.Blockers) == 0,
		FullSourceReviewComplete: false,
		TargetReady:              target.SeedOnly,
		Stages: []PlanStage{
			{Code: "users_and_rbac", Semantics: "admin 仅授予 moderator；super_admin 授予 super_admin；同步追加角色与封禁起点记录"},
			{Code: "images_and_tags", Semantics: "历史图片与标签 grandfather 为 pass，并各自追加 legacy_migration 审核记录"},
			{Code: "posts", Semantics: "派生互动计数从关系重建；view_count 不可重建，插入后原值更新且不得改 updated_at"},
			{Code: "comments", Semantics: "历史评论 grandfather 为 moderation=pass，并逐条追加 legacy_migration 文本审核记录"},
			{Code: "relations", Semantics: "评论父链先拓扑化；关注收藏与多态点赞通过 ID 映射装载"},
			{Code: "notifications", Semantics: "通知目标与缺失预览只能唯一映射，禁止取第一条或最近一条"},
			{Code: "histories", Semantics: "来源只有当前版本时 post_histories 与 comment_histories 保持为空，不伪造 revision=1"},
			{Code: "verify", Semantics: "映射、外键、审核、计数、sequence、时间戳与显式排除清单全部 fail closed"},
		},
		SafetyRules: []string{
			"source_repeatable_read_read_only_deferrable",
			"target_repeatable_read_read_only",
			"target_postgresql_18_goose_v11_seed_only",
			"source_target_actual_database_identity_distinct",
			"source_schema_fingerprint_and_primary_key_gate_not_implemented",
			"full_manifest_and_dictionary_review_not_implemented",
			"single_transaction_apply_not_implemented",
			"no_sensitive_values_in_report",
		},
	}
}

type databaseIdentity struct {
	systemIdentifier string
	databaseOID      string
}

func inspectDatabaseIdentity(ctx context.Context, tx *sql.Tx) (databaseIdentity, error) {
	var allowed bool
	if err := tx.QueryRowContext(ctx, `SELECT pg_catalog.has_function_privilege(
current_user, 'pg_catalog.pg_control_system()', 'EXECUTE'
)`).Scan(&allowed); err != nil {
		return databaseIdentity{}, gateError("database_identity_privilege_inspection_failed", "无法核验稳定数据库身份读取权限")
	}
	if !allowed {
		return databaseIdentity{}, gateError("database_identity_privilege_missing", "迁移检查角色缺少 pg_control_system() EXECUTE 权限")
	}
	identity := databaseIdentity{}
	err := tx.QueryRowContext(ctx, `SELECT control.system_identifier::text, database.oid::text
FROM pg_catalog.pg_control_system() AS control
JOIN pg_catalog.pg_database AS database
  ON database.datname = pg_catalog.current_database()`).Scan(
		&identity.systemIdentifier, &identity.databaseOID,
	)
	if err != nil {
		return databaseIdentity{}, gateError(
			"database_identity_inspection_failed",
			"无法核验稳定数据库身份",
		)
	}
	return identity, nil
}

func postgresMajor(ctx context.Context, tx *sql.Tx, side string) (int, error) {
	var versionText string
	if err := tx.QueryRowContext(ctx, "SHOW server_version_num").Scan(&versionText); err != nil {
		return 0, gateError(side+"_version_inspection_failed", "无法核验数据库主版本")
	}
	version, err := strconv.Atoi(strings.TrimSpace(versionText))
	if err != nil {
		return 0, gateError(side+"_version_invalid", "数据库版本信息不是预期格式")
	}
	return version / 10000, nil
}

func aggregateQueries(
	ctx context.Context,
	tx *sql.Tx,
	queries []struct{ code, sql string },
	errorCode, message string,
) ([]AggregateCount, error) {
	result := make([]AggregateCount, 0, len(queries))
	for _, query := range queries {
		var count int64
		if err := tx.QueryRowContext(ctx, query.sql).Scan(&count); err != nil {
			return nil, gateError(errorCode, message)
		}
		result = append(result, AggregateCount{Code: query.code, Count: count})
	}
	return result, nil
}
