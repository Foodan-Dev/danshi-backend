-- +goose Up

-- 评论需要一个受约束的当前审核状态，供公开查询和计数触发器共同使用；
-- moderation_records 继续作为追加不可篡改的完整审核流水。
ALTER TABLE comments ADD COLUMN moderation varchar(16);

UPDATE comments AS c
SET moderation = COALESCE(
    (
        SELECT mr.verdict
        FROM moderation_records AS mr
        WHERE mr.comment_id = c.id
        ORDER BY mr.created_at DESC, mr.id DESC
        LIMIT 1
    ),
    CASE WHEN c.deleted_reason = 'moderation' THEN 'block' ELSE 'pass' END
);

ALTER TABLE comments
    ALTER COLUMN moderation SET DEFAULT 'pending',
    ALTER COLUMN moderation SET NOT NULL,
    ADD CONSTRAINT comments_moderation_check
        CHECK (moderation IN ('pending', 'pass', 'review', 'block'));

COMMENT ON COLUMN comments.moderation IS
  '评论当前审核状态。公开路径只展示 pass；作者可见自己的 pending/review/block。';
COMMENT ON COLUMN comments.deleted_at IS
  '评论软删除标记。数据与回复链保留；公开路径不返回占位条目。';

DROP TRIGGER trg_comments_sync_counts_on_softdelete ON comments;
DROP TRIGGER trg_comments_sync_counts ON comments;

-- comment_count 只统计公开可见评论：评论自身必须 pass，回复所属根评论也必须 pass。
-- 根评论失去公开资格时整楼从 post.comment_count 扣除，但回复行及其状态保持不变。
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION danshi_sync_comment_counts()
RETURNS trigger
LANGUAGE plpgsql
AS $func$
DECLARE
  old_active boolean := false;
  new_active boolean := false;
  root_active boolean := false;
  delta integer := 0;
  p_id bigint;
  r_id bigint;
  floor_size integer := 1;
  prev text;
BEGIN
  prev := current_setting('danshi.allow_counter_write', true);
  PERFORM set_config('danshi.allow_counter_write', 'on', true);

  IF TG_OP <> 'INSERT' THEN
    old_active := OLD.deleted_at IS NULL AND OLD.moderation = 'pass';
  END IF;
  IF TG_OP <> 'DELETE' THEN
    new_active := NEW.deleted_at IS NULL AND NEW.moderation = 'pass';
  END IF;

  IF TG_OP = 'DELETE' THEN
    p_id := OLD.post_id;
    r_id := OLD.root_id;
  ELSE
    p_id := NEW.post_id;
    r_id := NEW.root_id;
  END IF;

  delta := new_active::integer - old_active::integer;
  IF r_id IS NULL THEN
    IF delta <> 0 THEN
      -- 普通软删除/审核流转不会删除回复，整楼可见性必须在根评论这一行一次结算。
      floor_size := 1 + CASE WHEN TG_OP = 'DELETE' THEN OLD.reply_count ELSE NEW.reply_count END;
      UPDATE posts
      SET comment_count = GREATEST(comment_count + delta * floor_size, 0)
      WHERE id = p_id;
    END IF;
  ELSE
    IF delta <> 0 THEN
      UPDATE comments
      SET reply_count = GREATEST(reply_count + delta, 0)
      WHERE id = r_id;

      SELECT deleted_at IS NULL AND moderation = 'pass'
      INTO root_active
      FROM comments
      WHERE id = r_id;

      IF COALESCE(root_active, false) THEN
        UPDATE posts
        SET comment_count = GREATEST(comment_count + delta, 0)
        WHERE id = p_id;
      END IF;
    END IF;
  END IF;

  PERFORM set_config('danshi.allow_counter_write', COALESCE(prev, ''), true);
  IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
END;
$func$;
-- +goose StatementEnd

CREATE TRIGGER trg_comments_sync_counts
    AFTER INSERT OR DELETE ON comments
    FOR EACH ROW EXECUTE FUNCTION danshi_sync_comment_counts();

CREATE TRIGGER trg_comments_sync_counts_on_visibility
    AFTER UPDATE OF deleted_at, moderation ON comments
    FOR EACH ROW
    WHEN (OLD.deleted_at IS DISTINCT FROM NEW.deleted_at
       OR OLD.moderation IS DISTINCT FROM NEW.moderation)
    EXECUTE FUNCTION danshi_sync_comment_counts();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION danshi_recount_all()
RETURNS TABLE (table_name text, fixed_rows bigint)
LANGUAGE plpgsql
AS $func$
DECLARE
  n bigint;
  prev text;
BEGIN
  prev := current_setting('danshi.allow_counter_write', true);
  PERFORM set_config('danshi.allow_counter_write', 'on', true);
  WITH actual AS (
    SELECT p.id,
           (SELECT count(*) FROM post_likes pl WHERE pl.post_id = p.id) AS lc,
           (SELECT count(*) FROM favorites f WHERE f.post_id = p.id) AS fc,
           (
             SELECT count(*)
             FROM comments c
             WHERE c.post_id = p.id
               AND c.deleted_at IS NULL
               AND c.moderation = 'pass'
               AND (
                 c.root_id IS NULL OR EXISTS (
                   SELECT 1
                   FROM comments root
                   WHERE root.id = c.root_id
                     AND root.deleted_at IS NULL
                     AND root.moderation = 'pass'
                 )
               )
           ) AS cc
    FROM posts p
  ), upd AS (
    UPDATE posts p
       SET like_count = a.lc, favorite_count = a.fc, comment_count = a.cc
      FROM actual a
     WHERE p.id = a.id
       AND (p.like_count, p.favorite_count, p.comment_count)
           IS DISTINCT FROM (a.lc, a.fc, a.cc)
    RETURNING 1
  ) SELECT count(*) INTO n FROM upd;
  table_name := 'posts'; fixed_rows := n; RETURN NEXT;

  WITH actual AS (
    SELECT c.id,
           (SELECT count(*) FROM comment_likes cl WHERE cl.comment_id = c.id) AS lc,
           (
             SELECT count(*)
             FROM comments r
             WHERE r.root_id = c.id
               AND r.deleted_at IS NULL
               AND r.moderation = 'pass'
           ) AS rc
    FROM comments c
  ), upd AS (
    UPDATE comments c
       SET like_count = a.lc, reply_count = a.rc
      FROM actual a
     WHERE c.id = a.id
       AND (c.like_count, c.reply_count) IS DISTINCT FROM (a.lc, a.rc)
    RETURNING 1
  ) SELECT count(*) INTO n FROM upd;
  table_name := 'comments'; fixed_rows := n; RETURN NEXT;
  PERFORM set_config('danshi.allow_counter_write', COALESCE(prev, ''), true);
END;
$func$;
-- +goose StatementEnd

SELECT * FROM danshi_recount_all();

CREATE INDEX idx_comments_public_roots_time
    ON comments (post_id, created_at, id)
    WHERE root_id IS NULL AND deleted_at IS NULL AND moderation = 'pass';
CREATE INDEX idx_comments_public_roots_hot
    ON comments (post_id, like_count DESC, created_at DESC, id DESC)
    WHERE root_id IS NULL AND deleted_at IS NULL AND moderation = 'pass';
CREATE INDEX idx_comments_public_replies_time
    ON comments (root_id, created_at, id)
    WHERE root_id IS NOT NULL AND deleted_at IS NULL AND moderation = 'pass';

-- +goose Down

DROP INDEX IF EXISTS idx_comments_public_replies_time;
DROP INDEX IF EXISTS idx_comments_public_roots_hot;
DROP INDEX IF EXISTS idx_comments_public_roots_time;

DROP TRIGGER trg_comments_sync_counts_on_visibility ON comments;
DROP TRIGGER trg_comments_sync_counts ON comments;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION danshi_sync_comment_counts()
RETURNS trigger
LANGUAGE plpgsql
AS $func$
DECLARE
  delta integer := 0;
  p_id bigint;
  r_id bigint;
  prev text;
BEGIN
  prev := current_setting('danshi.allow_counter_write', true);
  PERFORM set_config('danshi.allow_counter_write', 'on', true);
  IF TG_OP = 'INSERT' THEN
    IF NEW.deleted_at IS NULL THEN delta := 1; END IF;
    p_id := NEW.post_id; r_id := NEW.root_id;
  ELSIF TG_OP = 'DELETE' THEN
    IF OLD.deleted_at IS NULL THEN delta := -1; END IF;
    p_id := OLD.post_id; r_id := OLD.root_id;
  ELSE
    IF OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL THEN delta := -1;
    ELSIF OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS NULL THEN delta := 1;
    END IF;
    p_id := NEW.post_id; r_id := NEW.root_id;
  END IF;

  IF delta <> 0 THEN
    UPDATE posts SET comment_count = GREATEST(comment_count + delta, 0) WHERE id = p_id;
    IF r_id IS NOT NULL THEN
      UPDATE comments SET reply_count = GREATEST(reply_count + delta, 0) WHERE id = r_id;
    END IF;
  END IF;

  PERFORM set_config('danshi.allow_counter_write', COALESCE(prev, ''), true);
  IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
  RETURN NEW;
END;
$func$;
-- +goose StatementEnd

CREATE TRIGGER trg_comments_sync_counts
    AFTER INSERT OR DELETE ON comments
    FOR EACH ROW EXECUTE FUNCTION danshi_sync_comment_counts();
CREATE TRIGGER trg_comments_sync_counts_on_softdelete
    AFTER UPDATE OF deleted_at ON comments
    FOR EACH ROW
    WHEN (OLD.deleted_at IS DISTINCT FROM NEW.deleted_at)
    EXECUTE FUNCTION danshi_sync_comment_counts();

ALTER TABLE comments
    DROP CONSTRAINT comments_moderation_check,
    DROP COLUMN moderation;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION danshi_recount_all()
RETURNS TABLE (table_name text, fixed_rows bigint)
LANGUAGE plpgsql
AS $func$
DECLARE
  n bigint;
  prev text;
BEGIN
  prev := current_setting('danshi.allow_counter_write', true);
  PERFORM set_config('danshi.allow_counter_write', 'on', true);
  WITH actual AS (
    SELECT p.id,
           (SELECT count(*) FROM post_likes pl WHERE pl.post_id = p.id) AS lc,
           (SELECT count(*) FROM favorites f WHERE f.post_id = p.id) AS fc,
           (SELECT count(*) FROM comments c WHERE c.post_id = p.id
                                               AND c.deleted_at IS NULL) AS cc
    FROM posts p
  ), upd AS (
    UPDATE posts p
       SET like_count = a.lc, favorite_count = a.fc, comment_count = a.cc
      FROM actual a
     WHERE p.id = a.id
       AND (p.like_count, p.favorite_count, p.comment_count)
           IS DISTINCT FROM (a.lc, a.fc, a.cc)
    RETURNING 1
  ) SELECT count(*) INTO n FROM upd;
  table_name := 'posts'; fixed_rows := n; RETURN NEXT;

  WITH actual AS (
    SELECT c.id,
           (SELECT count(*) FROM comment_likes cl WHERE cl.comment_id = c.id) AS lc,
           (SELECT count(*) FROM comments r WHERE r.root_id = c.id
                                               AND r.deleted_at IS NULL) AS rc
    FROM comments c
  ), upd AS (
    UPDATE comments c
       SET like_count = a.lc, reply_count = a.rc
      FROM actual a
     WHERE c.id = a.id
       AND (c.like_count, c.reply_count) IS DISTINCT FROM (a.lc, a.rc)
    RETURNING 1
  ) SELECT count(*) INTO n FROM upd;
  table_name := 'comments'; fixed_rows := n; RETURN NEXT;
  PERFORM set_config('danshi.allow_counter_write', COALESCE(prev, ''), true);
END;
$func$;
-- +goose StatementEnd

SELECT * FROM danshi_recount_all();
