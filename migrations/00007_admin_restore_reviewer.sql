-- +goose Up

ALTER TABLE moderation_records DROP CONSTRAINT mr_manual_shape_check;

-- manual 始终是人工复核，必须携带成对的复核人与时间；admin_restore 也是人工操作，
-- 新记录可以携带同一对字段，但兼容迁移前已经存在且无法回填操作者的恢复流水。
--
-- 为什么不把存量行回填后收紧约束：存量操作者确实还在 raw_response.reviewer_id 里，
-- 但 moderation_records 有 append-only 触发器禁止任何 UPDATE（审核流水不可覆盖写）。
-- 回填就必须先关掉审计表的不可覆盖写保护，为强化一条约束去破坏一条更重的约束
-- 并不划算，因此存量行保持 reviewer_id 为空。
-- 其余 provider 仍是机器记录，人工字段必须为空。
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


-- +goose Down

-- 旧约束禁止任何非 manual 记录携带人工字段。新版本写入的 admin_restore 流水
-- 无法无损降级；显式拒绝回滚，不能篡改或丢弃追加不可变的审核记录。
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (
      SELECT 1
        FROM moderation_records
       WHERE provider <> 'manual'
         AND (reviewer_id IS NOT NULL OR reviewed_at IS NOT NULL)
  ) THEN
    RAISE EXCEPTION
      'cannot restore mr_manual_shape_check: non-manual moderation records carry reviewer metadata'
      USING ERRCODE = 'check_violation',
            HINT = 'Keep migration 00007 applied; reviewer metadata is append-only and cannot be discarded.';
  END IF;
END
$$;
-- +goose StatementEnd

ALTER TABLE moderation_records DROP CONSTRAINT mr_manual_shape_check;

ALTER TABLE moderation_records
    ADD CONSTRAINT mr_manual_shape_check CHECK (
        CASE WHEN provider = 'manual'
             THEN reviewer_id IS NOT NULL AND provider_job_id IS NULL
             ELSE reviewer_id IS NULL AND reviewed_at IS NULL
        END
    );
