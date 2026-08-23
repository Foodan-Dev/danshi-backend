-- +goose Up

CREATE TABLE moderation_alert_states (
    alert_key varchar(64) PRIMARY KEY,
    active boolean NOT NULL DEFAULT false,
    last_observed_count bigint NOT NULL DEFAULT 0,
    last_alerted_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT moderation_alert_states_key_check
        CHECK (alert_key = 'review_backlog'),
    CONSTRAINT moderation_alert_states_count_check
        CHECK (last_observed_count >= 0),
    CONSTRAINT moderation_alert_states_active_check
        CHECK (NOT active OR last_alerted_at IS NOT NULL)
);

COMMENT ON TABLE moderation_alert_states IS
  '审核运维告警的跨任务抑制状态；当前仅保存待复核队列积压状态。';
COMMENT ON COLUMN moderation_alert_states.alert_key IS
  '告警状态键；当前唯一合法值为 review_backlog。';
COMMENT ON COLUMN moderation_alert_states.active IS
  '最近一次检查时积压是否达到阈值。';
COMMENT ON COLUMN moderation_alert_states.last_observed_count IS
  '最近一次检查得到的帖子粒度待复核队列条目数。';
COMMENT ON COLUMN moderation_alert_states.last_alerted_at IS
  '最近一次安排发送积压告警的时间，用于跨 CronJob 冷却。';
COMMENT ON COLUMN moderation_alert_states.updated_at IS
  '应用层最后一次完成积压检查的时间。';

-- +goose Down

DROP TABLE moderation_alert_states;
