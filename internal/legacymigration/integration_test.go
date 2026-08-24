package legacymigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestInspectAndPlanSafetyGatesAgainstPostgres(t *testing.T) {
	sourceFixture := openLegacyPostgres(t)
	source := sourceFixture.SQL
	targetFixture := testutil.OpenPostgres(t)
	target := targetFixture.SQL
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	t.Run("inspect is redacted and uses exact source transaction mode", func(t *testing.T) {
		report, err := Run(ctx, source, target, ModeInspect)
		if err != nil {
			t.Fatalf("Run(inspect): %v", err)
		}
		if report.Source.Transaction.Isolation != "repeatable read" ||
			!report.Source.Transaction.ReadOnly || !report.Source.Transaction.Deferrable {
			t.Fatalf("来源事务模式不安全：%+v", report.Source.Transaction)
		}
		if report.Target.PostgresMajor != 18 || report.Target.GooseVersion != 11 || !report.Target.SeedOnly {
			t.Fatalf("目标门禁不完整：%+v", report.Target)
		}
		assertRedacted(t, report)
	})

	t.Run("plan takes fixed lock and does not write business data", func(t *testing.T) {
		before := targetStateFingerprint(ctx, t, target)
		report, err := Run(ctx, source, target, ModePlan)
		if err != nil {
			t.Fatalf("Run(plan): %v", err)
		}
		after := targetStateFingerprint(ctx, t, target)
		if before != after {
			t.Fatalf("plan 改写了目标 public schema 或数据：before=%s after=%s", before, after)
		}
		if report.Plan == nil || report.Plan.Executable || report.ApplyEnabled {
			t.Fatalf("plan 错误开放 apply：%+v", report.Plan)
		}
		assertRedacted(t, report)
	})

	t.Run("inspect and plan require independent artifact approvals", func(t *testing.T) {
		artifactRoot := t.TempDir()
		datasetPath := filepath.Join(artifactRoot, "dataset.json")
		planPath := filepath.Join(artifactRoot, "plan.json")
		manifestData := emptyManifestJSON()
		manifestPath := writeManifest(t, manifestData, 0o600)
		manifestDigest := ManifestDigest(sha256.Sum256(manifestData))
		before := targetStateFingerprint(ctx, t, target)

		inspectReport, err := Execute(ctx, source, target, Command{
			Mode: ModeInspect, DatasetArtifactPath: datasetPath,
		})
		if err != nil {
			t.Fatalf("Execute(inspect): %v", err)
		}
		datasetDigest, err := ParseArtifactDigest(inspectReport.DatasetArtifact.SHA256, "dataset_digest")
		if err != nil {
			t.Fatalf("ParseArtifactDigest: %v", err)
		}
		if inspectReport.ApplyEnabled || inspectReport.PlanArtifact != nil {
			t.Fatalf("inspect 不得开放 plan/apply：%+v", inspectReport)
		}
		approvedDataset, approvedDatasetBytes, err := loadDatasetArtifact(datasetPath, datasetDigest)
		if err != nil {
			t.Fatalf("loadDatasetArtifact: %v", err)
		}
		approvalPath, _, approval := writeSignedPlanApproval(t, approvedDataset, datasetDigest, manifestDigest)

		planCommand := Command{
			Mode:                   ModePlan,
			DatasetArtifactPath:    datasetPath,
			ManifestPath:           manifestPath,
			PlanArtifactPath:       planPath,
			ApprovalReceiptPath:    approvalPath,
			ApprovalPublicKeyPath:  "/root-owned/key-required",
			ExpectedDatasetDigest:  datasetDigest,
			ExpectedManifestDigest: manifestDigest,
		}
		planReport, err := executePlanWithVerifiedApproval(
			ctx, source, target, planCommand, approvedDataset, approvedDatasetBytes, approval,
		)
		if err != nil {
			t.Fatalf("Execute(plan): %v", err)
		}
		if planReport.PlanArtifact == nil || planReport.ApplyEnabled ||
			planReport.Inspection.Plan == nil || planReport.Inspection.Plan.Executable {
			t.Fatalf("plan 错误开放 apply：%+v", planReport)
		}
		planDigest, err := ParseArtifactDigest(planReport.PlanArtifact.SHA256, "plan_digest")
		if err != nil {
			t.Fatalf("ParseArtifactDigest(plan): %v", err)
		}
		if err := ValidateFutureApplyInputs(FutureApplyInputs{
			DatasetArtifactPath:    datasetPath,
			ManifestPath:           manifestPath,
			PlanArtifactPath:       planPath,
			ExpectedDatasetDigest:  datasetDigest,
			ExpectedManifestDigest: manifestDigest,
			ExpectedPlanDigest:     planDigest,
		}); err == nil || err.Error() != "current_plan_not_executable" {
			t.Fatalf("当前 coverage=false plan 必须永久拒绝 apply，实际 %v", err)
		}
		if after := targetStateFingerprint(ctx, t, target); before != after {
			t.Fatalf("两阶段门禁改写了目标：before=%s after=%s", before, after)
		}
		assertRedacted(t, planReport.Inspection)
		encoded, err := json.Marshal(planReport)
		if err != nil {
			t.Fatalf("json.Marshal execution report: %v", err)
		}
		for _, forbidden := range []string{
			"private@example.invalid", "legacy-body-do-not-print", manifestPath, datasetPath, planPath,
		} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("执行报告泄露敏感值或本地路径：%s", encoded)
			}
		}

		if _, err := source.ExecContext(ctx, `UPDATE public.users SET name = 'changed-after-approval'
WHERE id = '11111111-1111-1111-1111-111111111111'`); err != nil {
			t.Fatalf("修改来源数据漂移探针: %v", err)
		}
		changedPlanPath := filepath.Join(artifactRoot, "changed-plan.json")
		changedCommand := planCommand
		changedCommand.PlanArtifactPath = changedPlanPath
		_, planErr := executePlanWithVerifiedApproval(
			ctx, source, target, changedCommand, approvedDataset, approvedDatasetBytes, approval,
		)
		if _, err := source.ExecContext(ctx, `UPDATE public.users SET name = 'name'
WHERE id = '11111111-1111-1111-1111-111111111111'`); err != nil {
			t.Fatalf("恢复来源数据漂移探针: %v", err)
		}
		if planErr == nil || planErr.Error() != "dataset_snapshot_changed" {
			t.Fatalf("审批后来源漂移必须拒绝 plan，实际 %v", planErr)
		}
		if _, err := os.Stat(changedPlanPath); !os.IsNotExist(err) {
			t.Fatalf("失败 plan 不得留下 artifact，实际 %v", err)
		}
	})

	t.Run("target inspection transaction rejects writes", func(t *testing.T) {
		tx, _, err := beginTargetInspection(ctx, target, false)
		if err != nil {
			t.Fatalf("beginTargetInspection: %v", err)
		}
		defer func() { _ = tx.Rollback() }()
		_, err = tx.ExecContext(ctx, `INSERT INTO public.tags (name, moderation) VALUES ('readonly', 'pass')`)
		if err == nil {
			t.Fatal("目标只读事务意外接受业务写入")
		}
		var pgError *pgconn.PgError
		if !errors.As(err, &pgError) || pgError.Code != "25006" {
			t.Fatalf("目标写入应由 PostgreSQL 以 25006 拒绝，实际 %v", err)
		}
	})

	t.Run("plan lock is first target query", func(t *testing.T) {
		recorder := &queryRecorder{}
		observedTarget := openTracedDatabase(t, targetFixture.DSN, recorder, nil)
		if _, err := Run(ctx, source, observedTarget, ModePlan); err != nil {
			t.Fatalf("Run(plan with tracer): %v", err)
		}
		statements := recorder.snapshot()
		want := []string{
			"begin isolation level repeatable read read only",
			targetAdvisoryLockSQL,
			setSafeSearchPathSQL,
		}
		if len(statements) < len(want) {
			t.Fatalf("目标实际查询不足：%q", statements)
		}
		for index := range want {
			if statements[index] != want[index] {
				t.Fatalf("目标事务首查询顺序错误：got=%q want-prefix=%q", statements, want)
			}
		}
	})

	t.Run("shadow search path cannot redirect checks", func(t *testing.T) {
		installShadowObjects(ctx, t, source, target)
		runtimeParams := map[string]string{"search_path": "shadow,pg_catalog,public"}
		shadowSource := openTracedDatabase(t, sourceFixture.DSN, nil, runtimeParams)
		shadowTarget := openTracedDatabase(t, targetFixture.DSN, nil, runtimeParams)
		report, err := Run(ctx, shadowSource, shadowTarget, ModePlan)
		if err != nil {
			t.Fatalf("Run(plan with hostile search_path): %v", err)
		}
		if report.Target.BusinessRows != 0 || !report.Target.SeedOnly ||
			report.Target.Transaction.SearchPath != "pg_catalog" ||
			report.Source.Transaction.SearchPath != "pg_catalog" {
			t.Fatalf("shadow search_path 影响了检查结果：%+v", report)
		}
	})

	t.Run("stable database identity ignores connection options", func(t *testing.T) {
		first := openTracedDatabase(t, targetFixture.DSN, nil, map[string]string{
			"TimeZone": "UTC", "application_name": "identity-one",
		})
		second := openTracedDatabase(t, targetFixture.DSN, nil, map[string]string{
			"TimeZone": "Asia/Shanghai", "application_name": "identity-two",
		})
		firstTx, err := first.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatalf("first BeginTx: %v", err)
		}
		defer func() { _ = firstTx.Rollback() }()
		secondTx, err := second.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatalf("second BeginTx: %v", err)
		}
		defer func() { _ = secondTx.Rollback() }()
		firstIdentity, err := inspectDatabaseIdentity(ctx, firstTx)
		if err != nil {
			t.Fatalf("inspect first identity: %v", err)
		}
		secondIdentity, err := inspectDatabaseIdentity(ctx, secondTx)
		if err != nil {
			t.Fatalf("inspect second identity: %v", err)
		}
		if firstIdentity != secondIdentity {
			t.Fatalf("同库不同 DSN/runtime options 身份不稳定：first=%+v second=%+v", firstIdentity, secondIdentity)
		}
	})

	for _, fixture := range []struct {
		name string
		dsn  string
		db   *sql.DB
	}{{"postgres16", sourceFixture.DSN, source}, {"postgres18", targetFixture.DSN, target}} {
		fixture := fixture
		t.Run(fixture.name+" identity privilege gate", func(t *testing.T) {
			testIdentityPrivilegeGate(ctx, t, fixture.db, fixture.dsn)
		})
	}

	for _, column := range []string{"hometown", "bio"} {
		column := column
		t.Run("missing legacy users "+column+" is rejected", func(t *testing.T) {
			if _, err := source.ExecContext(ctx, "ALTER TABLE public.users DROP COLUMN "+column); err != nil {
				t.Fatalf("删除 legacy users.%s 探针: %v", column, err)
			}
			_, runErr := Run(ctx, source, target, ModeInspect)
			if _, err := source.ExecContext(ctx, "ALTER TABLE public.users ADD COLUMN "+column+" text"); err != nil {
				t.Fatalf("恢复 legacy users.%s 探针: %v", column, err)
			}
			if runErr == nil || runErr.Error() != "source_schema_mismatch" {
				t.Fatalf("缺少 legacy users.%s 应 fail closed，实际 %v", column, runErr)
			}
		})
	}

	t.Run("plan fails closed when lock is busy", func(t *testing.T) {
		connection, err := target.Conn(ctx)
		if err != nil {
			t.Fatalf("target.Conn: %v", err)
		}
		t.Cleanup(func() {
			_, _ = connection.ExecContext(ctx, "SELECT pg_catalog.pg_advisory_unlock($1)", AdvisoryLockKey)
			if closeErr := connection.Close(); closeErr != nil {
				t.Errorf("关闭 advisory lock 连接: %v", closeErr)
			}
		})
		if _, err := connection.ExecContext(ctx, "SELECT pg_catalog.pg_advisory_lock($1)", AdvisoryLockKey); err != nil {
			t.Fatalf("pg_advisory_lock: %v", err)
		}
		_, err = Run(ctx, source, target, ModePlan)
		if err == nil || err.Error() != "target_lock_busy" {
			t.Fatalf("锁冲突应 fail closed，实际 %v", err)
		}
	})

	t.Run("incomplete goose history is rejected", func(t *testing.T) {
		if _, err := target.ExecContext(ctx, "UPDATE goose_db_version SET is_applied = false WHERE version_id = 5"); err != nil {
			t.Fatalf("修改 goose 历史探针: %v", err)
		}
		t.Cleanup(func() {
			if _, err := target.ExecContext(ctx, "UPDATE goose_db_version SET is_applied = true WHERE version_id = 5"); err != nil {
				t.Errorf("恢复 goose 历史探针: %v", err)
			}
		})
		_, err := Run(ctx, source, target, ModeInspect)
		if err == nil || err.Error() != "target_goose_history_incomplete" {
			t.Fatalf("goose 历史缺口应 fail closed，实际 %v", err)
		}
	})

	t.Run("disabled critical trigger is rejected", func(t *testing.T) {
		if _, err := target.ExecContext(ctx, "ALTER TABLE comments DISABLE TRIGGER trg_comments_sync_counts"); err != nil {
			t.Fatalf("禁用关键触发器探针: %v", err)
		}
		t.Cleanup(func() {
			if _, err := target.ExecContext(ctx, "ALTER TABLE comments ENABLE TRIGGER trg_comments_sync_counts"); err != nil {
				t.Errorf("恢复关键触发器探针: %v", err)
			}
		})
		_, err := Run(ctx, source, target, ModeInspect)
		if err == nil || err.Error() != "target_schema_contract_mismatch" {
			t.Fatalf("关键触发器漂移应 fail closed，实际 %v", err)
		}
	})

	t.Run("altered seed content is rejected", func(t *testing.T) {
		if _, err := target.ExecContext(ctx, "UPDATE cuisines SET name = 'changed' WHERE name = '中式'"); err != nil {
			t.Fatalf("修改词表种子探针: %v", err)
		}
		t.Cleanup(func() {
			if _, err := target.ExecContext(ctx, "UPDATE cuisines SET name = '中式' WHERE name = 'changed'"); err != nil {
				t.Errorf("恢复词表种子探针: %v", err)
			}
		})
		_, err := Run(ctx, source, target, ModeInspect)
		if err == nil || err.Error() != "target_not_seed_only" {
			t.Fatalf("被修改的固定种子应 fail closed，实际 %v", err)
		}
	})

	t.Run("target with business rows is rejected", func(t *testing.T) {
		if _, err := target.ExecContext(ctx, "INSERT INTO tags (name, moderation) VALUES ('dirty', 'pass')"); err != nil {
			t.Fatalf("插入脏目标探针: %v", err)
		}
		_, err := Run(ctx, source, target, ModeInspect)
		if err == nil || err.Error() != "target_not_seed_only" {
			t.Fatalf("非空目标应 fail closed，实际 %v", err)
		}
	})
}

type postgresFixture struct {
	SQL *sql.DB
	DSN string
}

func openLegacyPostgres(t *testing.T) postgresFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	container, err := tcpostgres.Run(
		ctx,
		"postgres:16",
		tcpostgres.WithDatabase("legacy"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("启动 PostgreSQL 16: %v", err)
	}
	testcontainers.CleanupContainer(t, container)
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("ConnectionString: %v", err)
	}
	database, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.ExecContext(ctx, legacyFixtureSQL); err != nil {
		t.Fatalf("创建 legacy fixture: %v", err)
	}
	return postgresFixture{SQL: database, DSN: dsn}
}

type queryRecorder struct {
	mu         sync.Mutex
	statements []string
}

func (recorder *queryRecorder) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.statements = append(recorder.statements, data.SQL)
	return ctx
}

func (*queryRecorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (recorder *queryRecorder) snapshot() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]string(nil), recorder.statements...)
}

func openTracedDatabase(
	t *testing.T,
	dsn string,
	tracer pgx.QueryTracer,
	runtimeParams map[string]string,
) *sql.DB {
	t.Helper()
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgx.ParseConfig: %v", err)
	}
	config.Tracer = tracer
	for key, value := range runtimeParams {
		config.RuntimeParams[key] = value
	}
	database := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func installShadowObjects(ctx context.Context, t *testing.T, source, target *sql.DB) {
	t.Helper()
	if _, err := source.ExecContext(ctx, `
CREATE SCHEMA shadow;
CREATE TABLE shadow.users (LIKE public.users INCLUDING ALL);
CREATE FUNCTION shadow.current_setting(text) RETURNS text
LANGUAGE sql IMMUTABLE AS $$ SELECT 'shadowed'::text $$`); err != nil {
		t.Fatalf("创建来源 shadow objects: %v", err)
	}
	if _, err := target.ExecContext(ctx, `
CREATE SCHEMA shadow;
CREATE TABLE shadow.tags (LIKE public.tags INCLUDING ALL);
INSERT INTO shadow.tags (name, moderation) VALUES ('shadow', 'pass');
CREATE FUNCTION shadow.pg_try_advisory_xact_lock(bigint) RETURNS boolean
LANGUAGE sql IMMUTABLE AS $$ SELECT false $$;
CREATE FUNCTION shadow.current_setting(text) RETURNS text
LANGUAGE sql IMMUTABLE AS $$ SELECT 'shadowed'::text $$`); err != nil {
		t.Fatalf("创建目标 shadow objects: %v", err)
	}
}

func testIdentityPrivilegeGate(
	ctx context.Context,
	t *testing.T,
	admin *sql.DB,
	dsn string,
) {
	t.Helper()
	const role = "legacy_identity_probe"
	if _, err := admin.ExecContext(ctx, `CREATE ROLE legacy_identity_probe LOGIN PASSWORD 'probe-password'`); err != nil {
		t.Fatalf("创建普通身份探针角色: %v", err)
	}
	cleanupCtx := context.WithoutCancel(ctx)
	t.Cleanup(func() {
		_, _ = admin.ExecContext(cleanupCtx, `GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO PUBLIC`)
		_, _ = admin.ExecContext(cleanupCtx, `REVOKE EXECUTE ON FUNCTION pg_catalog.pg_control_system() FROM legacy_identity_probe`)
		_, _ = admin.ExecContext(cleanupCtx, `DROP ROLE IF EXISTS legacy_identity_probe`)
	})
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgx.ParseConfig: %v", err)
	}
	config.User = role
	config.Password = "probe-password"
	probe := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = probe.Close() })
	inspect := func() error {
		tx, beginErr := probe.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if beginErr != nil {
			return beginErr
		}
		defer func() { _ = tx.Rollback() }()
		_, inspectErr := inspectDatabaseIdentity(ctx, tx)
		return inspectErr
	}
	if err := inspect(); err != nil {
		t.Fatalf("普通角色默认读取稳定身份失败: %v", err)
	}
	if _, err := admin.ExecContext(ctx, `REVOKE EXECUTE ON FUNCTION pg_catalog.pg_control_system() FROM PUBLIC`); err != nil {
		t.Fatalf("撤销 PUBLIC identity 权限: %v", err)
	}
	if err := inspect(); err == nil || err.Error() != "database_identity_privilege_missing" {
		t.Fatalf("缺少 identity 权限应 fail closed，实际 %v", err)
	}
	if _, err := admin.ExecContext(ctx, `GRANT EXECUTE ON FUNCTION pg_catalog.pg_control_system() TO legacy_identity_probe`); err != nil {
		t.Fatalf("授予探针角色 identity 权限: %v", err)
	}
	if err := inspect(); err != nil {
		t.Fatalf("最小直接授权后读取稳定身份失败: %v", err)
	}
}

func assertRedacted(t *testing.T, report Report) {
	t.Helper()
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	text := string(encoded)
	for _, forbidden := range []string{
		"private@example.invalid",
		"legacy-body-do-not-print",
		"https://private.invalid/object",
		"11111111-1111-1111-1111-111111111111",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("报告泄露敏感来源值 %q：%s", forbidden, text)
		}
	}
}

func targetStateFingerprint(ctx context.Context, t *testing.T, database *sql.DB) string {
	t.Helper()
	tables := append([]string{"goose_db_version"}, targetBusinessTables...)
	for _, seed := range targetSeedTables {
		tables = append(tables, seed.name)
	}
	sort.Strings(tables)
	digest := sha256.New()
	for _, table := range tables {
		query := fmt.Sprintf(`SELECT pg_catalog.count(*), COALESCE(
			pg_catalog.md5(pg_catalog.string_agg(pg_catalog.to_jsonb(row_value)::text, '' ORDER BY pg_catalog.to_jsonb(row_value)::text)),
			'empty') FROM public.%s AS row_value`, table)
		var count int64
		var rowsHash string
		if err := database.QueryRowContext(ctx, query).Scan(&count, &rowsHash); err != nil {
			t.Fatalf("fingerprint 目标表 %s: %v", table, err)
		}
		_, _ = fmt.Fprintf(digest, "%s\t%d\t%s\n", table, count, rowsHash)
	}
	tx, _, err := beginTargetInspection(ctx, database, false)
	if err != nil {
		t.Fatalf("beginTargetInspection: %v", err)
	}
	schemaHash, err := targetSchemaFingerprint(ctx, tx)
	_ = tx.Rollback()
	if err != nil {
		t.Fatalf("targetSchemaFingerprint: %v", err)
	}
	_, _ = fmt.Fprintf(digest, "schema\t%s\n", schemaHash)
	return fmt.Sprintf("%x", digest.Sum(nil))
}

const legacyFixtureSQL = `
CREATE TABLE users (
 id uuid PRIMARY KEY, email text, password text, name text, gender text, avatar_url text,
 hometown text, bio text, role text, is_active boolean, created_at timestamptz, updated_at timestamptz
);
CREATE TABLE image_assets (
 id uuid PRIMARY KEY, uploader_id uuid, purpose text, object_key text, public_url text,
 content_type text, size bigint, status text, created_at timestamptz, updated_at timestamptz
);
CREATE TABLE posts (
 id uuid PRIMARY KEY, post_type text, title text, content text, category text, canteen text,
 tags jsonb, share_type text, cuisine text, flavors jsonb, price numeric, images jsonb,
 like_count integer, favorite_count integer, budget_range jsonb, preferences jsonb,
 author_id uuid, status text, comment_count integer, view_count integer,
 created_at timestamptz, updated_at timestamptz
);
CREATE TABLE comments (
 id uuid PRIMARY KEY, content text, post_id uuid, author_id uuid, parent_id uuid,
 reply_to_user_id uuid, mentioned_user_ids jsonb, like_count integer, reply_count integer,
 created_at timestamptz, updated_at timestamptz
);
CREATE TABLE follows (id uuid PRIMARY KEY, follower_id uuid, following_id uuid, created_at timestamptz);
CREATE TABLE favorites (id uuid PRIMARY KEY, user_id uuid, post_id uuid, created_at timestamptz);
CREATE TABLE likes (id uuid PRIMARY KEY, user_id uuid, likeable_type text, likeable_id uuid, created_at timestamptz);
CREATE TABLE notifications (
 id uuid PRIMARY KEY, recipient_id uuid, sender_id uuid, type text, related_id uuid,
 related_type text, content text, is_read boolean, created_at timestamptz, updated_at timestamptz
);
CREATE TABLE email_verification_codes (
 id uuid PRIMARY KEY, email text, purpose text, code_digest text, expires_at timestamptz,
 created_at timestamptz, updated_at timestamptz
);
INSERT INTO users VALUES (
 '11111111-1111-1111-1111-111111111111', 'private@example.invalid', 'hash', 'name', NULL,
 'https://private.invalid/object', NULL, NULL, 'admin', false, now(), now()
);
INSERT INTO posts VALUES (
 '22222222-2222-2222-2222-222222222222', 'share', 'title', 'legacy-body-do-not-print',
 'food', NULL, '[]', 'recommend', NULL, '[]', 10, '[]', 0, 0, 'null', 'null',
 '11111111-1111-1111-1111-111111111111', 'approved', 1, 7, now(), now()
);
INSERT INTO comments VALUES (
 '33333333-3333-3333-3333-333333333333', 'legacy-body-do-not-print',
 '22222222-2222-2222-2222-222222222222', '11111111-1111-1111-1111-111111111111',
 NULL, '11111111-1111-1111-1111-111111111111', '[]', 0, 0, now(), now()
);
`
