-- +goose Up

ALTER TABLE posts ADD COLUMN current_revision integer;
ALTER TABLE comments ADD COLUMN current_revision integer;

-- 00015 已把每条审核流水绑定到审核发生时的内容版本。旧模型中，主表当前内容的
-- 版本号恰好是 max(history.revision) + 1；因此这里把主表副本追加为这个 revision，
-- 既不需要也绝不能改写既有审核流水。
--
-- 与 00015 一样，只在本迁移事务的最小回填窗口临时关闭需要写入表的不可变触发器，
-- 并在继续建立指针和外键前立即恢复。审核表完全只读，因此其不可变触发器无需关闭。
ALTER TABLE post_histories DISABLE TRIGGER trg_post_histories_immutable;
ALTER TABLE comment_histories DISABLE TRIGGER trg_comment_histories_immutable;

INSERT INTO post_histories (post_id, revision, edited_by, edited_at, snapshot)
SELECT
    p.id,
    1 + COALESCE((
        SELECT max(ph.revision) FROM post_histories AS ph WHERE ph.post_id = p.id
    ), 0),
    p.author_id,
    p.updated_at,
    jsonb_build_object(
        'post_type', p.post_type,
        'share_type', p.share_type,
        'title', p.title,
        'content', p.content,
        'category', p.category,
        'canteen_id', p.canteen_id,
        'canteen_window_id', p.canteen_window_id,
        'cuisine_id', p.cuisine_id,
        'price', CASE WHEN p.price IS NULL THEN 'null'::jsonb ELSE to_jsonb(p.price::text) END,
        'budget_min', p.budget_min,
        'budget_max', p.budget_max,
        'tags', COALESCE((
            SELECT jsonb_agg(t.name ORDER BY lower(t.name), t.name)
            FROM post_tags AS pt
            JOIN tags AS t ON t.id = pt.tag_id
            WHERE pt.post_id = p.id
        ), '[]'::jsonb),
        'flavors', COALESCE((
            SELECT jsonb_agg(
                jsonb_build_object('name', f.name, 'stance', pf.stance)
                ORDER BY f.sort_order, f.id
            )
            FROM post_flavors AS pf
            JOIN flavors AS f ON f.id = pf.flavor_id
            WHERE pf.post_id = p.id
        ), '[]'::jsonb),
        'images', COALESCE((
            SELECT jsonb_agg(a.public_url ORDER BY pi.position)
            FROM post_images AS pi
            JOIN image_assets AS a ON a.id = pi.image_asset_id
            WHERE pi.post_id = p.id
        ), '[]'::jsonb)
    )
FROM posts AS p;

INSERT INTO comment_histories (comment_id, revision, edited_by, edited_at, content)
SELECT
    c.id,
    1 + COALESCE((
        SELECT max(ch.revision) FROM comment_histories AS ch WHERE ch.comment_id = c.id
    ), 0),
    c.author_id,
    c.updated_at,
    c.content
FROM comments AS c;

ALTER TABLE post_histories ENABLE TRIGGER trg_post_histories_immutable;
ALTER TABLE comment_histories ENABLE TRIGGER trg_comment_histories_immutable;

UPDATE posts AS p
SET current_revision = latest.revision
FROM (
    SELECT post_id, max(revision) AS revision
    FROM post_histories
    GROUP BY post_id
) AS latest
WHERE latest.post_id = p.id;

UPDATE comments AS c
SET current_revision = latest.revision
FROM (
    SELECT comment_id, max(revision) AS revision
    FROM comment_histories
    GROUP BY comment_id
) AS latest
WHERE latest.comment_id = c.id;

ALTER TABLE posts
    ALTER COLUMN current_revision SET NOT NULL,
    ALTER COLUMN current_revision SET DEFAULT 1,
    ADD CONSTRAINT posts_current_revision_check CHECK (current_revision >= 1),
    ADD CONSTRAINT fk_posts_current_revision
        FOREIGN KEY (id, current_revision)
        REFERENCES post_histories (post_id, revision)
        DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE comments
    ALTER COLUMN current_revision SET NOT NULL,
    ALTER COLUMN current_revision SET DEFAULT 1,
    ADD CONSTRAINT comments_current_revision_check CHECK (current_revision >= 1),
    ADD CONSTRAINT fk_comments_current_revision
        FOREIGN KEY (id, current_revision)
        REFERENCES comment_histories (comment_id, revision)
        DEFERRABLE INITIALLY DEFERRED;

-- 当前版本进入历史表后，00015 当时无法建立的审核版本外键终于有了真实目标。
-- 这两条约束会在本迁移内验证所有存量 content_revision，确保两次回填语义一致。
ALTER TABLE moderation_records
    ADD CONSTRAINT fk_mr_post_revision
        FOREIGN KEY (post_id, content_revision)
        REFERENCES post_histories (post_id, revision)
        DEFERRABLE INITIALLY DEFERRED,
    ADD CONSTRAINT fk_mr_comment_revision
        FOREIGN KEY (comment_id, content_revision)
        REFERENCES comment_histories (comment_id, revision)
        DEFERRABLE INITIALLY DEFERRED;

COMMENT ON COLUMN posts.current_revision IS
  '主表当前副本所指向的历史 revision；回退只移动该指针并同步副本，不追加历史。';
COMMENT ON COLUMN comments.current_revision IS
  '主表当前副本所指向的历史 revision；版本真源在 comment_histories。';
COMMENT ON TABLE post_histories IS
  '帖子的全量不可变版本表，包含当前版；创建写 revision 1，编辑追加，回退不追加。';
COMMENT ON COLUMN post_histories.revision IS
  '从 1 起单调递增的版本号；posts.current_revision 可指向任一真实版本。';
COMMENT ON TABLE comment_histories IS
  '评论的全量不可变版本表，包含当前版；创建写 revision 1，编辑追加。';
COMMENT ON COLUMN moderation_records.content_revision IS
  '帖子或评论被审内容的版本号，通过复合外键锚定同一对象的不可变历史版本。';


-- +goose Down

-- 删除指针会让旧应用重新把 max(revision)+1 当成当前版本；只要存在内容，这会把
-- 当前版误判成一个不存在的新版本。又因历史只追加，down 不能删除 00017 回填行。
-- 因此有数据时显式拒绝有损回滚；空库仍支持 schema-test 的 up/down/up。
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM posts) OR EXISTS (SELECT 1 FROM comments) THEN
    RAISE EXCEPTION 'cannot remove current revision pointers while posts or comments exist'
      USING ERRCODE = 'check_violation',
            HINT = 'Keep migration 00017 applied; full-version history rows are append-only.';
  END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE moderation_records
    DROP CONSTRAINT fk_mr_comment_revision,
    DROP CONSTRAINT fk_mr_post_revision;

ALTER TABLE comments
    DROP CONSTRAINT fk_comments_current_revision,
    DROP CONSTRAINT comments_current_revision_check,
    DROP COLUMN current_revision;

ALTER TABLE posts
    DROP CONSTRAINT fk_posts_current_revision,
    DROP CONSTRAINT posts_current_revision_check,
    DROP COLUMN current_revision;

COMMENT ON TABLE post_histories IS
  '帖子被编辑替换掉的旧版本表。创建不写历史；每次编辑先追加当时主表及关联的完整快照，再更新当前版本。';
COMMENT ON COLUMN post_histories.revision IS
  '旧版本排序号，从首次编辑产生的 1 起连续递增；主表不保存 revision 计数器。';
COMMENT ON TABLE comment_histories IS
  '评论被编辑替换掉的旧正文表。创建不写历史；首次编辑写 revision 1。';
COMMENT ON COLUMN moderation_records.content_revision IS
  '帖子或评论被审内容的版本号；当前版本 = 最大历史 revision + 1，无历史时为 1。';
