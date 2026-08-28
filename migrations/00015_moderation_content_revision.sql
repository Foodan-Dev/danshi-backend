-- +goose Up

ALTER TABLE moderation_records ADD COLUMN content_revision integer;

-- 存量同步审核都在对应内容写入后落库，可由审核时间点之前已经形成的旧版本数
-- 恢复其被审版本号。当前版本号始终是当时最大历史 revision + 1。
-- 只在本迁移事务的回填窗口关闭不可变触发器；迁移提交前立即恢复，运行期写保护不变。
ALTER TABLE moderation_records DISABLE TRIGGER trg_moderation_records_immutable;

UPDATE moderation_records AS mr
SET content_revision = 1 + COALESCE((
    SELECT max(ph.revision)
    FROM post_histories AS ph
    WHERE ph.post_id = mr.post_id AND ph.edited_at <= mr.created_at
), 0)
WHERE mr.post_id IS NOT NULL;

UPDATE moderation_records AS mr
SET content_revision = 1 + COALESCE((
    SELECT max(ch.revision)
    FROM comment_histories AS ch
    WHERE ch.comment_id = mr.comment_id AND ch.edited_at <= mr.created_at
), 0)
WHERE mr.comment_id IS NOT NULL;

-- 人工复核必须继承当初机审行的版本；复核发生前即使对象又被编辑，也不能把人工结论
-- 错绑到回调/复核到达时的当前版本。
UPDATE moderation_records AS manual
SET content_revision = machine.content_revision
FROM moderation_records AS machine
WHERE manual.supersedes_id = machine.id;

ALTER TABLE moderation_records ENABLE TRIGGER trg_moderation_records_immutable;

ALTER TABLE moderation_records
    ADD CONSTRAINT mr_content_revision_target_check CHECK (
        (content_revision IS NULL) = (post_id IS NULL AND comment_id IS NULL)
        AND (content_revision IS NULL OR content_revision >= 1)
    );

COMMENT ON COLUMN moderation_records.content_revision IS
  '帖子或评论被审内容的版本号；当前版本 = 最大历史 revision + 1，无历史时为 1。'
  '这里明确不加历史表外键：审核发生时内容仍在主表，尚未成为历史行，外键无处可指；'
  '若等内容被替换后再回填，就必须修改已经写入的只追加审核流水。';

DROP INDEX idx_mr_pending_review;
CREATE INDEX idx_mr_pending_review ON moderation_records (created_at)
    WHERE verdict IN ('review', 'block') AND provider <> 'manual';

-- review 与 block 都是机审未通过，均允许人工复核。人工行仍必须指向同一对象、
-- 同一字段和同一被审版本，且不能复核另一条人工记录。
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
  IF t.verdict NOT IN ('review', 'block') THEN
    RAISE EXCEPTION '只能复核机审未通过的 review 或 block 记录，当前被复核记录 verdict=%', t.verdict
      USING ERRCODE = 'restrict_violation';
  END IF;
  IF (NEW.post_id, NEW.comment_id, NEW.image_asset_id, NEW.tag_id, NEW.user_id,
      NEW.scene, NEW.field, NEW.content_revision)
     IS DISTINCT FROM
     (t.post_id, t.comment_id, t.image_asset_id, t.tag_id, t.user_id,
      t.scene, t.field, t.content_revision) THEN
    RAISE EXCEPTION '人工复核必须针对同一对象、同一字段、同一版本'
      USING ERRCODE = 'restrict_violation';
  END IF;
  RETURN NEW;
END;
$func$;
-- +goose StatementEnd

COMMENT ON COLUMN moderation_records.supersedes_id IS
  '本条人工复核所针对的机审记录。待复核队列 = 当前版本的机器 review/block 且没有任何行 supersedes 它。';


-- +goose Down

COMMENT ON COLUMN moderation_records.supersedes_id IS
  '本条人工复核所针对的机审记录。待复核队列 = 机审 verdict=review 且没有任何行 supersedes 它。';

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

DROP INDEX idx_mr_pending_review;
CREATE INDEX idx_mr_pending_review ON moderation_records (created_at)
    WHERE verdict = 'review' AND provider <> 'manual';

ALTER TABLE moderation_records
    DROP CONSTRAINT mr_content_revision_target_check,
    DROP COLUMN content_revision;
