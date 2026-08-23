-- +goose Up

-- 复合游标与 ORDER BY 必须共享完整全序键；partial 条件同时排除公开信息流永不返回的软删行。
CREATE INDEX idx_posts_status_created_id
    ON posts (status, created_at DESC, id DESC)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_notifications_recipient_created_id
    ON notifications (recipient_id, created_at DESC, id DESC);


-- +goose Down

DROP INDEX IF EXISTS idx_notifications_recipient_created_id;
DROP INDEX IF EXISTS idx_posts_status_created_id;
