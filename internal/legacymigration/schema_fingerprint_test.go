package legacymigration

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestTargetV11SchemaFingerprintMatchesMigrations(t *testing.T) {
	database := testutil.OpenPostgres(t).SQL
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	fingerprint := currentTargetSchemaFingerprint(ctx, t, database)
	if fingerprint != expectedTargetSchemaFingerprint {
		t.Fatalf("canonical v11 schema fingerprint 漂移：got=%s want=%s", fingerprint, expectedTargetSchemaFingerprint)
	}

	t.Run("missing unsampled unique index", func(t *testing.T) {
		var definition string
		if err := database.QueryRowContext(ctx, `SELECT pg_catalog.pg_get_indexdef(
			'public.uq_image_assets_object_key'::pg_catalog.regclass, 0, false
		)`).Scan(&definition); err != nil {
			t.Fatalf("读取 index definition: %v", err)
		}
		assertSchemaMutationRejected(ctx, t, database,
			`DROP INDEX public.uq_image_assets_object_key`, definition)
	})

	for _, constraint := range []struct {
		name string
		kind string
	}{{"follows_following_id_fkey", "FK"}, {"follows_no_self_check", "CHECK"}} {
		constraint := constraint
		t.Run("missing "+constraint.kind, func(t *testing.T) {
			var definition string
			if err := database.QueryRowContext(ctx, `SELECT pg_catalog.pg_get_constraintdef(oid, false)
				FROM pg_catalog.pg_constraint WHERE conrelid='public.follows'::pg_catalog.regclass AND conname=$1`,
				constraint.name).Scan(&definition); err != nil {
				t.Fatalf("读取 %s definition: %v", constraint.kind, err)
			}
			restore := `ALTER TABLE public.follows ADD CONSTRAINT ` + constraint.name + ` ` + definition
			assertSchemaMutationRejected(ctx, t, database,
				`ALTER TABLE public.follows DROP CONSTRAINT `+constraint.name, restore)
		})
	}

	t.Run("tampered function body", func(t *testing.T) {
		var definition string
		if err := database.QueryRowContext(ctx, `SELECT pg_catalog.pg_get_functiondef(
			'public.danshi_recount_all()'::pg_catalog.regprocedure
		)`).Scan(&definition); err != nil {
			t.Fatalf("读取 function definition: %v", err)
		}
		assertSchemaMutationRejected(ctx, t, database, `
CREATE OR REPLACE FUNCTION public.danshi_recount_all()
RETURNS TABLE(table_name text, fixed_rows bigint)
LANGUAGE plpgsql
AS $func$ BEGIN RETURN; END $func$`, definition)
	})

	t.Run("same trigger name on wrong table", func(t *testing.T) {
		var definition string
		if err := database.QueryRowContext(ctx, `SELECT pg_catalog.pg_get_triggerdef(trigger_value.oid, false)
			FROM pg_catalog.pg_trigger AS trigger_value
			WHERE trigger_value.tgrelid='public.comments'::pg_catalog.regclass
			  AND trigger_value.tgname='trg_comments_sync_counts'`).Scan(&definition); err != nil {
			t.Fatalf("读取 trigger definition: %v", err)
		}
		mutation := `DROP TRIGGER trg_comments_sync_counts ON public.comments;
CREATE TRIGGER trg_comments_sync_counts AFTER INSERT ON public.post_likes
FOR EACH ROW EXECUTE FUNCTION public.danshi_sync_post_like_count()`
		restore := `DROP TRIGGER trg_comments_sync_counts ON public.post_likes; ` + definition
		assertSchemaMutationRejected(ctx, t, database, mutation, restore)
	})

	if got := currentTargetSchemaFingerprint(ctx, t, database); got != expectedTargetSchemaFingerprint {
		t.Fatalf("所有 mutation 恢复后 schema 未回到 v11：got=%s", got)
	}
}

func currentTargetSchemaFingerprint(ctx context.Context, t *testing.T, database *sql.DB) string {
	t.Helper()
	tx, _, err := beginTargetInspection(ctx, database, false)
	if err != nil {
		t.Fatalf("beginTargetInspection: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	fingerprint, err := targetSchemaFingerprint(ctx, tx)
	if err != nil {
		t.Fatalf("targetSchemaFingerprint: %v", err)
	}
	return fingerprint
}

func assertSchemaMutationRejected(
	ctx context.Context,
	t *testing.T,
	database *sql.DB,
	mutation string,
	restore string,
) {
	t.Helper()
	if _, err := database.ExecContext(ctx, mutation); err != nil {
		t.Fatalf("执行 schema mutation: %v", err)
	}
	got := currentTargetSchemaFingerprint(ctx, t, database)
	if _, err := database.ExecContext(ctx, restore); err != nil {
		t.Fatalf("恢复 schema mutation: %v", err)
	}
	if got == expectedTargetSchemaFingerprint {
		t.Fatal("canonical schema fingerprint 未拒绝结构漂移")
	}
}
