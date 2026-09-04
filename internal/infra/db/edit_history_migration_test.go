package db_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	dbinfra "github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestEditHistoryAndCurrentRevisionMigrationsPreserveVersionSemantics(t *testing.T) {
	database := testutil.OpenPostgres(t)
	ctx := context.Background()

	version, err := dbinfra.Version(ctx, database.SQL)
	require.NoError(t, err)
	for version > 7 {
		require.NoError(t, dbinfra.DownOne(ctx, database.SQL))
		version, err = dbinfra.Version(ctx, database.SQL)
		require.NoError(t, err)
	}
	require.EqualValues(t, 7, version)

	_, err = database.SQL.ExecContext(ctx, `
		INSERT INTO users (id, email, password_hash, name)
		VALUES (8101, 'history-migration@fdueat.com', 'x', '历史迁移');

		INSERT INTO posts
			(id, author_id, post_type, share_type, status, category, title, content)
		VALUES
			(8201, 8101, 'share', 'recommend', 'approved', 'food', '未编辑', '当前正文'),
			(8202, 8101, 'share', 'recommend', 'approved', 'food', '已编辑', '第二版');

		INSERT INTO post_histories (id, post_id, revision, edited_by, snapshot)
		VALUES
			(8301, 8201, 1, 8101, '{
				"post_type":"share","share_type":"recommend","title":"未编辑","content":"当前正文",
				"category":"food","canteen_id":null,"canteen_window_id":null,"cuisine_id":null,
				"price":null,"budget_min":null,"budget_max":null,"tags":[],"flavors":[],"images":[]
			}'::jsonb),
			(8302, 8202, 1, 8101, '{
				"post_type":"share","share_type":"recommend","title":"已编辑","content":"第一版",
				"category":"food","canteen_id":null,"canteen_window_id":null,"cuisine_id":null,
				"price":null,"budget_min":null,"budget_max":null,"tags":[],"flavors":[],"images":[]
			}'::jsonb),
			(8303, 8202, 2, 8101, '{
				"post_type":"share","share_type":"recommend","title":"已编辑","content":"第二版",
				"category":"food","canteen_id":null,"canteen_window_id":null,"cuisine_id":null,
				"price":null,"budget_min":null,"budget_max":null,"tags":[],"flavors":[],"images":[]
			}'::jsonb);

		INSERT INTO comments
			(id, post_id, author_id, reply_to_user_id, content)
		VALUES (8401, 8201, 8101, 8101, '未编辑评论');
		INSERT INTO comment_histories (id, comment_id, revision, edited_by, content)
		VALUES (8501, 8401, 1, 8101, '未编辑评论');

		INSERT INTO moderation_records
			(id, post_id, post_history_id, scene, provider, verdict)
		VALUES (8601, 8201, 8301, 'text', 'migration_machine', 'pass');
		INSERT INTO moderation_records
			(id, comment_id, comment_history_id, scene, provider, verdict)
		VALUES (8602, 8401, 8501, 'text', 'migration_machine', 'pass');
	`)
	require.NoError(t, err)

	require.NoError(t, dbinfra.Up(ctx, database.SQL))
	version, err = dbinfra.Version(ctx, database.SQL)
	require.NoError(t, err)
	require.Equal(t, dbinfra.ExpectedVersion, version)

	var count int
	require.NoError(t, database.SQL.QueryRowContext(ctx,
		`SELECT count(*) FROM post_histories WHERE post_id = 8201`).Scan(&count))
	require.Equal(t, 1, count, "00008 清理旧模型的创建期副本后，00017 必须重新写入当前完整版本")
	require.NoError(t, database.SQL.QueryRowContext(ctx,
		`SELECT count(*) FROM comment_histories WHERE comment_id = 8401`).Scan(&count))
	require.Equal(t, 1, count, "评论当前版必须由 00017 写回全量历史")
	require.NoError(t, database.SQL.QueryRowContext(ctx,
		`SELECT count(*) FROM post_histories WHERE post_id = 8202`).Scan(&count))
	require.Equal(t, 2, count, "已编辑帖子应保留第一版并追加当前第二版")

	var revision int
	var content string
	require.NoError(t, database.SQL.QueryRowContext(ctx, `
		SELECT revision, snapshot->>'content'
		FROM post_histories WHERE post_id = 8202 AND revision = 1
	`).Scan(&revision, &content))
	require.Equal(t, 1, revision)
	require.Equal(t, "第一版", content)

	var postRevision, commentRevision int
	require.NoError(t, database.SQL.QueryRowContext(ctx,
		`SELECT current_revision FROM posts WHERE id = 8202`).Scan(&postRevision))
	require.Equal(t, 2, postRevision)
	require.NoError(t, database.SQL.QueryRowContext(ctx,
		`SELECT current_revision FROM comments WHERE id = 8401`).Scan(&commentRevision))
	require.Equal(t, 1, commentRevision)

	require.NoError(t, database.SQL.QueryRowContext(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'moderation_records'
		  AND column_name IN ('post_history_id', 'comment_history_id')
	`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, database.SQL.QueryRowContext(ctx,
		`SELECT count(*) FROM moderation_records WHERE id IN (8601, 8602)`).Scan(&count))
	require.Equal(t, 2, count, "解除版本锚定不得删除审核流水")

	// 00019 与内容历史无关，可先回滚 00019、00018；随后验证 00017 仍拒绝破坏现存内容。
	require.NoError(t, dbinfra.DownOne(ctx, database.SQL))
	require.NoError(t, dbinfra.DownOne(ctx, database.SQL))
	err = dbinfra.DownOne(ctx, database.SQL)
	require.ErrorContains(t, err,
		"cannot remove current revision pointers while posts or comments exist")
	version, versionErr := dbinfra.Version(ctx, database.SQL)
	require.NoError(t, versionErr)
	require.EqualValues(t, 17, version)
}
