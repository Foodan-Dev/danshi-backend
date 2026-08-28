package db_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	dbinfra "github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestModerationContentRevisionMigrationBackfillsAndAllowsBlockReview(t *testing.T) {
	database := testutil.OpenPostgres(t)
	ctx := context.Background()

	// 回滚到 00015 之前那一格。用循环而不是单次 DownOne：后续再加迁移时，
	// 00015 就不再是最新一格，单次回滚会停在别处。
	version, err := dbinfra.Version(ctx, database.SQL)
	require.NoError(t, err)
	for version > 14 {
		require.NoError(t, dbinfra.DownOne(ctx, database.SQL))
		version, err = dbinfra.Version(ctx, database.SQL)
		require.NoError(t, err)
	}
	require.EqualValues(t, 14, version)

	_, err = database.SQL.ExecContext(ctx, `
		INSERT INTO users (id, email, password_hash, name)
		VALUES (9101, 'moderation-version@fdueat.com', 'x', '版本审核');

		INSERT INTO posts
			(id, author_id, post_type, share_type, status, category, title, content)
		VALUES (9201, 9101, 'share', 'recommend', 'rejected', 'food', '第三版', '第三版正文');

		INSERT INTO post_histories
			(id, post_id, revision, edited_by, edited_at, snapshot)
		VALUES
			(9301, 9201, 1, 9101, '2026-01-02 00:00:00+00', '{"title":"第一版"}'),
			(9302, 9201, 2, 9101, '2026-01-03 00:00:00+00', '{"title":"第二版"}');

		INSERT INTO comments
			(id, post_id, author_id, reply_to_user_id, content)
		VALUES (9401, 9201, 9101, 9101, '第二版评论');
		INSERT INTO comment_histories
			(id, comment_id, revision, edited_by, edited_at, content)
		VALUES (9501, 9401, 1, 9101, '2026-01-02 00:00:00+00', '第一版评论');

		INSERT INTO moderation_records
			(id, post_id, scene, provider, verdict, created_at)
		VALUES
			(9601, 9201, 'text', 'migration_machine', 'review', '2026-01-01 00:00:00+00'),
			(9602, 9201, 'text', 'migration_machine', 'review', '2026-01-02 12:00:00+00'),
			(9603, 9201, 'text', 'migration_machine', 'block',  '2026-01-03 12:00:00+00');
		INSERT INTO moderation_records
			(id, comment_id, scene, provider, verdict, created_at)
		VALUES
			(9701, 9401, 'text', 'migration_machine', 'review', '2026-01-01 00:00:00+00'),
			(9702, 9401, 'text', 'migration_machine', 'block',  '2026-01-02 12:00:00+00');
		INSERT INTO moderation_records
			(id, post_id, scene, provider, verdict, reviewer_id, reviewed_at, supersedes_id, created_at)
		VALUES
			(9604, 9201, 'text', 'manual', 'pass', 9101, '2026-01-04 00:00:00+00', 9601,
			 '2026-01-04 00:00:00+00');
	`)
	require.NoError(t, err)

	require.NoError(t, dbinfra.Up(ctx, database.SQL))

	var postRevisions, commentRevisions []int32
	rows, err := database.SQL.QueryContext(ctx, `
		SELECT content_revision FROM moderation_records
		WHERE post_id = 9201 ORDER BY id
	`)
	require.NoError(t, err)
	for rows.Next() {
		var revision int32
		require.NoError(t, rows.Scan(&revision))
		postRevisions = append(postRevisions, revision)
	}
	require.NoError(t, rows.Close())
	require.Equal(t, []int32{1, 2, 3, 1}, postRevisions,
		"延迟到达的人工写回必须继承送审行的第一版，而不是到达时的当前第三版")

	rows, err = database.SQL.QueryContext(ctx, `
		SELECT content_revision FROM moderation_records
		WHERE comment_id = 9401 ORDER BY id
	`)
	require.NoError(t, err)
	for rows.Next() {
		var revision int32
		require.NoError(t, rows.Scan(&revision))
		commentRevisions = append(commentRevisions, revision)
	}
	require.NoError(t, rows.Close())
	require.Equal(t, []int32{1, 2}, commentRevisions)

	_, err = database.SQL.ExecContext(ctx, `
		INSERT INTO moderation_records
			(post_id, content_revision, scene, provider, verdict, reviewer_id, reviewed_at, supersedes_id)
		VALUES (9201, 3, 'text', 'manual', 'pass', 9101, now(), 9603)
	`)
	require.NoError(t, err, "机器 block 必须能够被人工复核")

	_, err = database.SQL.ExecContext(ctx, `
		INSERT INTO moderation_records
			(comment_id, content_revision, scene, provider, verdict, reviewer_id, reviewed_at, supersedes_id)
		VALUES (9401, 1, 'text', 'manual', 'pass', 9101, now(), 9702)
	`)
	require.ErrorContains(t, err, "同一对象、同一字段、同一版本")

	var foreignKeys int
	require.NoError(t, database.SQL.QueryRowContext(ctx, `
		SELECT count(*)
		FROM pg_constraint AS constraint_row
		JOIN pg_class AS table_row ON table_row.oid = constraint_row.conrelid
		JOIN pg_namespace AS schema_row ON schema_row.oid = table_row.relnamespace
		JOIN LATERAL unnest(constraint_row.conkey) AS key_column(attnum) ON true
		JOIN pg_attribute AS column_row
		  ON column_row.attrelid = table_row.oid AND column_row.attnum = key_column.attnum
		WHERE constraint_row.contype = 'f'
		  AND schema_row.nspname = 'public'
		  AND table_row.relname = 'moderation_records'
		  AND column_row.attname = 'content_revision'
	`).Scan(&foreignKeys))
	require.Equal(t, 2, foreignKeys,
		"00017 把当前版本写入历史后，帖子与评论审核版本号都必须建立真实外键")
}
