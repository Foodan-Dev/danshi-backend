-- +goose Up

ALTER TABLE moderation_records DROP CONSTRAINT mr_manual_shape_check;

-- 管理员下架帖子导致图片不再被任何公开帖子引用时，会为图片追加一条独立审核事实。
-- 它与 admin_restore 一样是可追责的人工管理动作，但不是对某条机器 review 的复核，
-- 因此携带 reviewer_id/reviewed_at、不得携带 provider_job_id，也不设置 supersedes_id。
ALTER TABLE moderation_records
    ADD CONSTRAINT mr_manual_shape_check CHECK (
        CASE
            WHEN provider = 'manual'
                THEN reviewer_id IS NOT NULL AND provider_job_id IS NULL
            WHEN provider IN ('admin_restore', 'admin_post_delete')
                THEN provider_job_id IS NULL
            ELSE reviewer_id IS NULL AND reviewed_at IS NULL
        END
    );


-- +goose Down

-- admin_post_delete 流水只追加且携带人工字段，无法在不篡改审计历史的前提下降级到旧约束。
-- 与 00007 的 admin_restore 降级策略一致：存在新流水时显式拒绝回滚。
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (
      SELECT 1
        FROM moderation_records
       WHERE provider = 'admin_post_delete'
         AND (reviewer_id IS NOT NULL OR reviewed_at IS NOT NULL)
  ) THEN
    RAISE EXCEPTION
      'cannot restore mr_manual_shape_check: admin_post_delete records carry reviewer metadata'
      USING ERRCODE = 'check_violation',
            HINT = 'Keep migration 00013 applied; reviewer metadata is append-only and cannot be discarded.';
  END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE moderation_records DROP CONSTRAINT mr_manual_shape_check;

ALTER TABLE moderation_records
    ADD CONSTRAINT mr_manual_shape_check CHECK (
        CASE
            WHEN provider = 'manual'
                THEN reviewer_id IS NOT NULL AND provider_job_id IS NULL
            WHEN provider = 'admin_restore'
                THEN provider_job_id IS NULL
            ELSE reviewer_id IS NULL AND reviewed_at IS NULL
        END
    );
