package db_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	dbinfra "github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestCurrentContentRevisionMigrationBackfillsPointersAndPreservesModerationBindings(t *testing.T) {
	database := testutil.OpenPostgres(t)
	ctx := context.Background()

	version, err := dbinfra.Version(ctx, database.SQL)
	require.NoError(t, err)
	for version > 16 {
		require.NoError(t, dbinfra.DownOne(ctx, database.SQL))
		version, err = dbinfra.Version(ctx, database.SQL)
		require.NoError(t, err)
	}
	require.EqualValues(t, 16, version)

	_, err = database.SQL.ExecContext(ctx, `
		INSERT INTO users (id,email,password_hash,name)
		VALUES (17101,'current-revision@fdueat.com','x','版本指针迁移');

		INSERT INTO posts
			(id,author_id,post_type,share_type,status,category,title,content,updated_at)
		VALUES
			(17201,17101,'share','recommend','approved','food','第二版','当前帖子正文',
			 '2026-08-01 00:00:00+00');
		INSERT INTO post_histories
			(id,post_id,revision,edited_by,edited_at,snapshot)
		VALUES
			(17301,17201,1,17101,'2026-07-01 00:00:00+00',
			 '{"title":"第一版","content":"旧帖子正文"}');

		INSERT INTO comments
			(id,post_id,author_id,reply_to_user_id,content,moderation,updated_at)
		VALUES
			(17401,17201,17101,17101,'当前评论正文','review','2026-08-02 00:00:00+00');

		INSERT INTO moderation_records
			(id,post_id,content_revision,scene,provider,verdict,created_at)
		VALUES
			(17501,17201,1,'text','migration_machine','block','2026-07-01 00:00:00+00'),
			(17502,17201,2,'text','migration_machine','pass','2026-08-01 00:00:00+00');
		INSERT INTO moderation_records
			(id,comment_id,content_revision,scene,provider,verdict,created_at)
		VALUES
			(17503,17401,1,'text','migration_machine','review','2026-08-02 00:00:00+00');
	`)
	require.NoError(t, err)

	require.NoError(t, dbinfra.Up(ctx, database.SQL))

	var postRevision, commentRevision int32
	require.NoError(t, database.SQL.QueryRowContext(ctx,
		`SELECT current_revision FROM posts WHERE id=17201`).Scan(&postRevision))
	require.NoError(t, database.SQL.QueryRowContext(ctx,
		`SELECT current_revision FROM comments WHERE id=17401`).Scan(&commentRevision))
	require.EqualValues(t, 2, postRevision)
	require.EqualValues(t, 1, commentRevision)

	var postTitle, postContent, commentContent string
	require.NoError(t, database.SQL.QueryRowContext(ctx, `
		SELECT snapshot->>'title', snapshot->>'content'
		FROM post_histories WHERE post_id=17201 AND revision=2
	`).Scan(&postTitle, &postContent))
	require.Equal(t, "第二版", postTitle)
	require.Equal(t, "当前帖子正文", postContent)
	require.NoError(t, database.SQL.QueryRowContext(ctx, `
		SELECT content FROM comment_histories WHERE comment_id=17401 AND revision=1
	`).Scan(&commentContent))
	require.Equal(t, "当前评论正文", commentContent)

	var moderationRevisions []int32
	rows, err := database.SQL.QueryContext(ctx, `
		SELECT content_revision FROM moderation_records
		WHERE id IN (17501,17502,17503) ORDER BY id
	`)
	require.NoError(t, err)
	for rows.Next() {
		var revision int32
		require.NoError(t, rows.Scan(&revision))
		moderationRevisions = append(moderationRevisions, revision)
	}
	require.NoError(t, rows.Close())
	require.Equal(t, []int32{1, 2, 1}, moderationRevisions,
		"00017 不得改写 00015 已建立的语义版本绑定")

	_, err = database.SQL.ExecContext(ctx, `UPDATE posts SET current_revision=99 WHERE id=17201`)
	require.Error(t, err, "主表指针必须指向同一对象的真实历史版本")
	_, err = database.SQL.ExecContext(ctx, `
		INSERT INTO moderation_records
			(post_id,content_revision,scene,provider,verdict)
		VALUES (17201,99,'text','migration_machine','pass')
	`)
	require.Error(t, err, "审核版本号必须指向同一对象的真实历史版本")
	_, err = database.SQL.ExecContext(ctx, `
		UPDATE post_histories SET snapshot='{"tampered":true}' WHERE id=17301
	`)
	require.ErrorContains(t, err, "追加不可篡改", "迁移后历史不可变触发器必须仍启用")
	_, err = database.SQL.ExecContext(ctx, `
		UPDATE moderation_records SET verdict='pass' WHERE id=17501
	`)
	require.ErrorContains(t, err, "不可覆盖", "迁移后审核不可变触发器必须仍启用")
}
