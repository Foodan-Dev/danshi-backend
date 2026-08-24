package legacymigration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/Foodan-Dev/danshi-backend/internal/legacymigration"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestInspectAndPlanSafetyGatesAgainstPostgres(t *testing.T) {
	source := openLegacyPostgres(t)
	target := testutil.OpenPostgres(t).SQL
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	t.Run("inspect is redacted and uses exact source transaction mode", func(t *testing.T) {
		report, err := legacymigration.Run(ctx, source, target, legacymigration.ModeInspect)
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
		before := targetBusinessRows(ctx, t, target)
		report, err := legacymigration.Run(ctx, source, target, legacymigration.ModePlan)
		if err != nil {
			t.Fatalf("Run(plan): %v", err)
		}
		after := targetBusinessRows(ctx, t, target)
		if before != after || after != 0 {
			t.Fatalf("plan 改写了业务数据：before=%d after=%d", before, after)
		}
		if report.Plan == nil || report.Plan.Executable || report.ApplyEnabled {
			t.Fatalf("plan 错误开放 apply：%+v", report.Plan)
		}
		assertRedacted(t, report)
	})

	t.Run("plan fails closed when lock is busy", func(t *testing.T) {
		connection, err := target.Conn(ctx)
		if err != nil {
			t.Fatalf("target.Conn: %v", err)
		}
		t.Cleanup(func() {
			_, _ = connection.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", legacymigration.AdvisoryLockKey)
			if closeErr := connection.Close(); closeErr != nil {
				t.Errorf("关闭 advisory lock 连接: %v", closeErr)
			}
		})
		if _, err := connection.ExecContext(ctx, "SELECT pg_advisory_lock($1)", legacymigration.AdvisoryLockKey); err != nil {
			t.Fatalf("pg_advisory_lock: %v", err)
		}
		_, err = legacymigration.Run(ctx, source, target, legacymigration.ModePlan)
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
		_, err := legacymigration.Run(ctx, source, target, legacymigration.ModeInspect)
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
		_, err := legacymigration.Run(ctx, source, target, legacymigration.ModeInspect)
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
		_, err := legacymigration.Run(ctx, source, target, legacymigration.ModeInspect)
		if err == nil || err.Error() != "target_not_seed_only" {
			t.Fatalf("被修改的固定种子应 fail closed，实际 %v", err)
		}
	})

	t.Run("target with business rows is rejected", func(t *testing.T) {
		if _, err := target.ExecContext(ctx, "INSERT INTO tags (name, moderation) VALUES ('dirty', 'pass')"); err != nil {
			t.Fatalf("插入脏目标探针: %v", err)
		}
		_, err := legacymigration.Run(ctx, source, target, legacymigration.ModeInspect)
		if err == nil || err.Error() != "target_not_seed_only" {
			t.Fatalf("非空目标应 fail closed，实际 %v", err)
		}
	})
}

func openLegacyPostgres(t *testing.T) *sql.DB {
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
	return database
}

func assertRedacted(t *testing.T, report legacymigration.Report) {
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

func targetBusinessRows(ctx context.Context, t *testing.T, database *sql.DB) int64 {
	t.Helper()
	var count int64
	if err := database.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM users) +
		(SELECT count(*) FROM posts) +
		(SELECT count(*) FROM comments) +
		(SELECT count(*) FROM image_assets) +
		(SELECT count(*) FROM tags) +
		(SELECT count(*) FROM moderation_records)`).Scan(&count); err != nil {
		t.Fatalf("统计目标业务行: %v", err)
	}
	return count
}

const legacyFixtureSQL = `
CREATE TABLE users (
 id uuid PRIMARY KEY, email text, password text, name text, gender text, avatar_url text,
 role text, is_active boolean, created_at timestamptz, updated_at timestamptz
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
 'https://private.invalid/object', 'admin', false, now(), now()
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
