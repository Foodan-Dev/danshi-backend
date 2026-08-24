package legacymigration

import (
	"context"
	"database/sql"
)

var targetBusinessTables = []string{
	"canteen_windows", "comment_histories", "comment_likes", "comment_mentions", "comments",
	"dictionary_suggestions", "email_verification_codes", "favorites", "follows", "image_assets",
	"moderation_alert_states", "moderation_records", "notifications", "post_flavors", "post_histories",
	"post_images", "post_likes", "post_tags", "posts", "tags", "user_ban_records", "user_role_records",
	"user_roles", "user_sessions", "users",
}

var targetSeedTables = []struct {
	name          string
	expected      int64
	validationSQL string
}{
	{name: "canteens", expected: 15, validationSQL: `SELECT count(*) = 15 AND count(*) FILTER (WHERE
(code, name, campus, sort_order, is_active) IN (
 ('canteen-danyuan','旦苑食堂','邯郸校区',10,true),
 ('canteen-beiqu','北区食堂','邯郸校区',20,true),
 ('canteen-nanqu','南区食堂','邯郸校区',30,true),
 ('canteen-nanyuan','南苑食堂','邯郸校区',40,true),
 ('canteen-jiaogong','教工餐厅','邯郸校区',50,true),
 ('canteen-nanqu-minzu','南区民族餐厅','邯郸校区',60,true),
 ('canteen-jiangwan','江湾食堂','江湾校区',110,true),
 ('canteen-jiangwan-guanghua','江湾光华餐厅','江湾校区',120,true),
 ('canteen-jiangwan-bei','江湾北食堂','江湾校区',130,true),
 ('canteen-jiangwan-minzu','江湾民族餐厅','江湾校区',140,true),
 ('canteen-jiangwan-canche','江湾餐车','江湾校区',150,true),
 ('canteen-fenglin','枫林食堂','枫林校区',210,true),
 ('canteen-fenglin-minzu','枫林民族餐厅','枫林校区',220,true),
 ('canteen-huli','护理学院餐厅','枫林校区',230,true),
 ('canteen-zhangjiang','张江食堂','张江校区',310,true)
)) = 15 FROM canteens`},
	{name: "cuisines", expected: 6, validationSQL: `SELECT count(*) = 6 AND count(*) FILTER (WHERE
(name, sort_order, is_active) IN (
 ('中式',10,true), ('西式',20,true), ('日式',30,true),
 ('韩式',40,true), ('东南亚',50,true), ('其他',99,true)
)) = 6 FROM cuisines`},
	{name: "flavors", expected: 16, validationSQL: `SELECT count(*) = 16 AND count(*) FILTER (WHERE
(name, sort_order, is_active) IN (
 ('清淡',10,true), ('微辣',20,true), ('中辣',30,true), ('麻辣',40,true),
 ('特辣',50,true), ('香辣',60,true), ('酸辣',70,true), ('甜味',80,true),
 ('咸鲜',90,true), ('浓郁',100,true), ('爽口',110,true), ('家常',120,true),
 ('川菜',130,true), ('湘菜',140,true), ('粤菜',150,true), ('其他',999,true)
)) = 16 FROM flavors`},
}

func inspectTarget(ctx context.Context, tx *sql.Tx, transaction TransactionInspection) (TargetInspection, error) {
	major, err := postgresMajor(ctx, tx, "target")
	if err != nil {
		return TargetInspection{}, err
	}
	if major != ExpectedTargetMajor {
		return TargetInspection{}, gateError("target_postgres_version_mismatch", "目标库必须是 PostgreSQL 18")
	}
	version, err := gooseVersion(ctx, tx)
	if err != nil {
		return TargetInspection{}, err
	}
	if version != ExpectedGooseVersion {
		return TargetInspection{}, gateError("target_goose_version_mismatch", "目标库 goose schema 必须严格位于 v11")
	}
	if err := validateTargetV11Contract(ctx, tx); err != nil {
		return TargetInspection{}, err
	}
	seedRows, seedOK, err := inspectSeeds(ctx, tx)
	if err != nil {
		return TargetInspection{}, err
	}
	businessRows, err := countTargetBusinessRows(ctx, tx)
	if err != nil {
		return TargetInspection{}, err
	}
	unexpected, err := countUnexpectedTables(ctx, tx)
	if err != nil {
		return TargetInspection{}, err
	}
	seedOnly := seedOK && businessRows == 0 && unexpected == 0
	if !seedOnly {
		return TargetInspection{}, gateError("target_not_seed_only", "目标库必须只含 v11 固定词表种子且业务表为空")
	}
	return TargetInspection{
		PostgresMajor: major, GooseVersion: version, Transaction: transaction,
		SeedRows: seedRows, BusinessRows: businessRows, UnexpectedTableCount: unexpected, SeedOnly: true,
	}, nil
}

func gooseVersion(ctx context.Context, tx *sql.Tx) (int64, error) {
	var version int64
	var historyComplete bool
	err := tx.QueryRowContext(ctx, `SELECT
COALESCE(max(version_id) FILTER (WHERE is_applied), 0),
count(DISTINCT version_id) FILTER (WHERE version_id BETWEEN 1 AND 11 AND is_applied) = 11
AND count(*) FILTER (WHERE version_id BETWEEN 1 AND 11 AND NOT is_applied) = 0
AND count(*) FILTER (WHERE version_id > 11) = 0
FROM goose_db_version`).Scan(&version, &historyComplete)
	if err != nil {
		return 0, gateError("target_goose_inspection_failed", "无法核验目标库 goose 版本")
	}
	if !historyComplete {
		return 0, gateError("target_goose_history_incomplete", "目标库 goose v1 到 v11 历史不完整")
	}
	return version, nil
}

func validateTargetV11Contract(ctx context.Context, tx *sql.Tx) error {
	var valid bool
	err := tx.QueryRowContext(ctx, `SELECT
  EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='public' AND table_name='comments' AND column_name='moderation'
      AND is_nullable='NO' AND column_default LIKE '%pending%'
  )
  AND EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='public' AND table_name='posts' AND column_name='view_count'
      AND is_nullable='NO'
  )
  AND NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='public' AND table_name='users' AND column_name='role'
  )
  AND NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema='public' AND table_name='moderation_records'
      AND column_name IN ('post_history_id', 'comment_history_id')
  )
  AND (
    SELECT count(*) = 6 FROM pg_trigger
    WHERE NOT tgisinternal AND tgenabled = 'O'
      AND tgname IN (
        'trg_posts_counters_insert_zero',
        'trg_comments_sync_counts',
        'trg_comments_sync_counts_on_visibility',
        'trg_moderation_records_immutable',
        'trg_user_role_records_immutable',
        'trg_user_ban_records_immutable'
      )
  )
  AND to_regprocedure('public.danshi_recount_all()') IS NOT NULL
  AND to_regclass('public.user_roles') IS NOT NULL
  AND to_regclass('public.user_role_records') IS NOT NULL
  AND to_regclass('public.user_ban_records') IS NOT NULL`).Scan(&valid)
	if err != nil {
		return gateError("target_schema_contract_inspection_failed", "无法核验目标库 v11 关键 schema 契约")
	}
	if !valid {
		return gateError("target_schema_contract_mismatch", "目标库不符合 v11 的 RBAC、审核、历史与计数契约")
	}
	return nil
}

func inspectSeeds(ctx context.Context, tx *sql.Tx) ([]AggregateCount, bool, error) {
	result := make([]AggregateCount, 0, len(targetSeedTables))
	valid := true
	for _, seed := range targetSeedTables {
		query := "SELECT count(*) FROM " + seed.name
		var count int64
		if err := tx.QueryRowContext(ctx, query).Scan(&count); err != nil {
			return nil, false, gateError("target_seed_inspection_failed", "无法核验目标库固定词表种子")
		}
		var contentValid bool
		if err := tx.QueryRowContext(ctx, seed.validationSQL).Scan(&contentValid); err != nil {
			return nil, false, gateError("target_seed_inspection_failed", "无法核验目标库固定词表种子")
		}
		result = append(result, AggregateCount{Code: seed.name, Count: count})
		valid = valid && count == seed.expected && contentValid
	}
	return result, valid, nil
}

func countTargetBusinessRows(ctx context.Context, tx *sql.Tx) (int64, error) {
	var total int64
	for _, table := range targetBusinessTables {
		query := "SELECT count(*) FROM " + table
		var count int64
		if err := tx.QueryRowContext(ctx, query).Scan(&count); err != nil {
			return 0, gateError("target_business_inspection_failed", "无法核验目标业务表是否为空")
		}
		total += count
	}
	return total, nil
}

func countUnexpectedTables(ctx context.Context, tx *sql.Tx) (int64, error) {
	known := append([]string{"goose_db_version"}, targetBusinessTables...)
	for _, seed := range targetSeedTables {
		known = append(known, seed.name)
	}
	var count int64
	err := tx.QueryRowContext(ctx, `SELECT count(*)
FROM information_schema.tables
WHERE table_schema = 'public' AND table_type = 'BASE TABLE'
  AND NOT (table_name = ANY($1::text[]))`, known).Scan(&count)
	if err != nil {
		return 0, gateError("target_table_inventory_failed", "无法核验目标库表清单")
	}
	return count, nil
}
