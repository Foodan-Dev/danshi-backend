-- +goose Up

-- 人工复核仍须指向同一对象、同一字段；内容版本不再是审核流水的一部分。
-- 先改写触发器函数，再删除 history 列，避免留下引用已删除列的死代码。
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION danshi_check_moderation_review()
RETURNS trigger
LANGUAGE plpgsql
AS $func$
DECLARE t moderation_records%ROWTYPE;
BEGIN
  IF (NEW.provider = 'manual') <> (NEW.supersedes_id IS NOT NULL) THEN
    RAISE EXCEPTION '人工复核记录必须且只能带 supersedes_id，机审记录不得带'
      USING ERRCODE = 'restrict_violation';
  END IF;
  IF NEW.supersedes_id IS NULL THEN
    RETURN NEW;
  END IF;
  SELECT * INTO t FROM moderation_records WHERE id = NEW.supersedes_id FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION '被复核的审核记录不存在' USING ERRCODE = 'restrict_violation';
  END IF;
  IF t.provider = 'manual' THEN
    RAISE EXCEPTION '不能复核另一条人工复核记录' USING ERRCODE = 'restrict_violation';
  END IF;
  IF t.verdict <> 'review' THEN
    RAISE EXCEPTION '只能复核机审判定为 review 的记录，当前被复核记录 verdict=%', t.verdict
      USING ERRCODE = 'restrict_violation';
  END IF;
  IF (NEW.post_id, NEW.comment_id, NEW.image_asset_id, NEW.tag_id, NEW.user_id,
      NEW.scene, NEW.field)
     IS DISTINCT FROM
     (t.post_id, t.comment_id, t.image_asset_id, t.tag_id, t.user_id,
      t.scene, t.field) THEN
    RAISE EXCEPTION '人工复核必须针对同一对象、同一字段'
      USING ERRCODE = 'restrict_violation';
  END IF;
  RETURN NEW;
END;
$func$;
-- +goose StatementEnd

ALTER TABLE moderation_records
    DROP CONSTRAINT fk_mr_post_history,
    DROP CONSTRAINT fk_mr_comment_history,
    DROP CONSTRAINT mr_post_history_paired_check,
    DROP CONSTRAINT mr_comment_history_paired_check,
    DROP COLUMN post_history_id,
    DROP COLUMN comment_history_id;

-- 这两个复合唯一约束只为已删除的复合外键服务，不再保留无消费者的约束面。
ALTER TABLE post_histories DROP CONSTRAINT uq_post_histories_id_post;
ALTER TABLE comment_histories DROP CONSTRAINT uq_comment_histories_id_comment;

-- 必须在审核锚定解除后再清理历史。删除没有后续历史、且快照仍与主表完整内容一致的
-- 最新行：未编辑对象是 revision 1，已编辑对象则是更高 revision；它们都是旧模型写入的
-- 当前版本副本。存在后续 revision 的旧行已经是被替换的真实版本，不能因内容后来恰好
-- 回退而误删。
SET danshi.allow_hard_delete = 'on';

DELETE FROM post_histories AS ph
USING posts AS p
WHERE ph.post_id = p.id
  AND NOT EXISTS (
      SELECT 1 FROM post_histories AS later
      WHERE later.post_id = ph.post_id AND later.revision > ph.revision
  )
  AND ph.snapshot = jsonb_build_object(
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
  );

DELETE FROM comment_histories AS ch
USING comments AS c
WHERE ch.comment_id = c.id
  AND ch.content = c.content
  AND NOT EXISTS (
      SELECT 1 FROM comment_histories AS later
      WHERE later.comment_id = ch.comment_id AND later.revision > ch.revision
  );

RESET danshi.allow_hard_delete;

COMMENT ON TABLE post_histories IS
  '帖子被编辑替换掉的旧版本表。创建不写历史；每次编辑先追加当时主表及关联的完整快照，'
  '再更新当前版本。追加不可篡改，不参与业务查询。';
COMMENT ON COLUMN post_histories.revision IS
  '旧版本排序号，从首次编辑产生的 1 起连续递增；主表不保存 revision 计数器。';
COMMENT ON TABLE comment_histories IS
  '评论被编辑替换掉的旧正文表。创建不写历史；首次编辑写 revision 1。';
COMMENT ON TABLE moderation_records IS
  '内容审核流水，一次审核一行，只归属于内容对象及字段并始终针对其当前版本；'
  '行不可修改，人工复核通过 supersedes_id 指回同对象同字段的机器 review 行。';


-- +goose Down

-- 本迁移删除了审核版本锚定列，也可能删除创建期重复历史。迁移后产生的审核记录没有
-- 可恢复的 history id，现有主表当前版本也没有足够元数据重建原来的全量 history 行。
-- 因此有任何帖子或评论数据时必须显式拒绝回滚；静默猜测版本会伪造审计事实。
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM posts) OR EXISTS (SELECT 1 FROM comments) THEN
    RAISE EXCEPTION
      'cannot restore version-anchored moderation and full histories while posts or comments exist'
      USING ERRCODE = 'check_violation',
            HINT = 'Keep migration 00008 applied; deleted anchors and creation-time histories cannot be reconstructed truthfully.';
  END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE post_histories
    ADD CONSTRAINT uq_post_histories_id_post UNIQUE (id, post_id);
ALTER TABLE comment_histories
    ADD CONSTRAINT uq_comment_histories_id_comment UNIQUE (id, comment_id);

ALTER TABLE moderation_records
    ADD COLUMN post_history_id bigint,
    ADD COLUMN comment_history_id bigint,
    ADD CONSTRAINT fk_mr_post_history
        FOREIGN KEY (post_history_id, post_id)
        REFERENCES post_histories (id, post_id) ON DELETE RESTRICT,
    ADD CONSTRAINT fk_mr_comment_history
        FOREIGN KEY (comment_history_id, comment_id)
        REFERENCES comment_histories (id, comment_id) ON DELETE RESTRICT,
    ADD CONSTRAINT mr_post_history_paired_check
        CHECK ((post_id IS NULL) = (post_history_id IS NULL)),
    ADD CONSTRAINT mr_comment_history_paired_check
        CHECK ((comment_id IS NULL) = (comment_history_id IS NULL));

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION danshi_check_moderation_review()
RETURNS trigger
LANGUAGE plpgsql
AS $func$
DECLARE t moderation_records%ROWTYPE;
BEGIN
  IF (NEW.provider = 'manual') <> (NEW.supersedes_id IS NOT NULL) THEN
    RAISE EXCEPTION '人工复核记录必须且只能带 supersedes_id，机审记录不得带'
      USING ERRCODE = 'restrict_violation';
  END IF;
  IF NEW.supersedes_id IS NULL THEN
    RETURN NEW;
  END IF;
  SELECT * INTO t FROM moderation_records WHERE id = NEW.supersedes_id FOR UPDATE;
  IF NOT FOUND THEN
    RAISE EXCEPTION '被复核的审核记录不存在' USING ERRCODE = 'restrict_violation';
  END IF;
  IF t.provider = 'manual' THEN
    RAISE EXCEPTION '不能复核另一条人工复核记录' USING ERRCODE = 'restrict_violation';
  END IF;
  IF t.verdict <> 'review' THEN
    RAISE EXCEPTION '只能复核机审判定为 review 的记录，当前被复核记录 verdict=%', t.verdict
      USING ERRCODE = 'restrict_violation';
  END IF;
  IF (NEW.post_id, NEW.comment_id, NEW.image_asset_id, NEW.tag_id, NEW.user_id,
      NEW.scene, NEW.field, NEW.post_history_id, NEW.comment_history_id)
     IS DISTINCT FROM
     (t.post_id, t.comment_id, t.image_asset_id, t.tag_id, t.user_id,
      t.scene, t.field, t.post_history_id, t.comment_history_id) THEN
    RAISE EXCEPTION '人工复核必须针对同一对象、同一字段、同一版本'
      USING ERRCODE = 'restrict_violation';
  END IF;
  RETURN NEW;
END;
$func$;
-- +goose StatementEnd

COMMENT ON TABLE post_histories IS
  '帖子的全量版本表。创建写 revision 1，每次编辑追加一版，主表等于最新历史。';
COMMENT ON COLUMN post_histories.revision IS
  '版本号，从 1 起单调递增，并供审核流水锚定被审版本。';
COMMENT ON TABLE comment_histories IS
  '评论的全量版本表：创建写 revision 1，每次编辑追加一版。';
COMMENT ON TABLE moderation_records IS
  '内容审核流水，一次审核一行；帖子和评论记录通过 history id 锚定被审版本。';
