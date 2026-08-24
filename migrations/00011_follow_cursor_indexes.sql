-- +goose Up

CREATE INDEX idx_follows_follower_created_following
    ON follows (follower_id, created_at DESC, following_id DESC);

CREATE INDEX idx_follows_following_created_follower
    ON follows (following_id, created_at DESC, follower_id DESC);


-- +goose Down

DROP INDEX IF EXISTS idx_follows_following_created_follower;
DROP INDEX IF EXISTS idx_follows_follower_created_following;
