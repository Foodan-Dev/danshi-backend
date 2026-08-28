-- +goose Up

CREATE TABLE image_moderation_retries (
    image_asset_id    bigint PRIMARY KEY REFERENCES image_assets(id) ON DELETE CASCADE,
    state             text NOT NULL DEFAULT 'pending'
                      CHECK (state IN ('pending', 'dead_letter')),
    attempts          integer NOT NULL DEFAULT 1 CHECK (attempts > 0),
    next_attempt_at   timestamptz,
    lease_token       text,
    lease_until       timestamptz,
    last_error_code   text NOT NULL
                      CHECK (last_error_code IN (
                          'submit_failed', 'invalid_submission', 'submit_exhausted'
                      )),
    dead_lettered_at  timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CHECK ((lease_token IS NULL) = (lease_until IS NULL)),
    CHECK (lease_token IS NULL OR length(lease_token) BETWEEN 16 AND 64),
    CHECK ((state = 'dead_letter') = (next_attempt_at IS NULL)),
    CHECK ((state = 'dead_letter') = (dead_lettered_at IS NOT NULL)),
    CHECK (state = 'pending' OR lease_token IS NULL)
);

COMMENT ON TABLE image_moderation_retries IS
    '图片首次送审失败后的有界补审队列；成功受理或审核结论到达后删除，死信保留观测';

CREATE INDEX idx_image_moderation_retries_due
    ON image_moderation_retries (next_attempt_at, image_asset_id)
    WHERE state = 'pending';


-- +goose Down

DROP TABLE IF EXISTS image_moderation_retries;
