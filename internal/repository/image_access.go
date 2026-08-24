package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
)

// ImageAccessDeliveryClaim 是一次带 fencing generation/token 的领取结果。
// ObjectKey 与 PublicURL 只从 image_assets 临时联表读取，不复制进 durable payload。
type ImageAccessDeliveryClaim struct {
	model.ImageAccessDelivery
	ObjectKey string
	PublicURL string
}

// ImageAccessOutboxRepository 管理审核事务内意图与 worker 的短事务状态迁移。
type ImageAccessOutboxRepository struct{}

// Enqueue 只在 source 审核流水首次出现时追加 intent，并把该图片的 delivery 投影到新代际。
// 重复回调命中 uq_image_access_intent_source 后不会 bump generation。
func (ImageAccessOutboxRepository) Enqueue(
	ctx context.Context,
	imageAssetID uint64,
	sourceModerationRecordID uint64,
	desiredPublic bool,
	purgeRequired bool,
	now time.Time,
) (bool, error) {
	result := db.FromContext(ctx).Exec(`
WITH inserted AS (
    INSERT INTO image_access_intents
        (image_asset_id, source_moderation_record_id, desired_public, created_at)
    SELECT ?, ?, ?, ?
    WHERE NOT EXISTS (
        SELECT 1 FROM image_access_intents WHERE source_moderation_record_id = ?
    )
    ON CONFLICT (source_moderation_record_id) DO NOTHING
    RETURNING id, image_asset_id, desired_public, created_at
)
INSERT INTO image_access_deliveries (
    image_asset_id, desired_intent_id, desired_public, purge_required, generation, state,
    next_attempt_at, created_at, updated_at
)
SELECT image_asset_id, id, desired_public, ?, 1, 'pending_acl', created_at, created_at, created_at
FROM inserted
ON CONFLICT (image_asset_id) DO UPDATE SET
    desired_intent_id = EXCLUDED.desired_intent_id,
    desired_public = EXCLUDED.desired_public,
    purge_required = EXCLUDED.purge_required,
    generation = image_access_deliveries.generation + 1,
    state = 'pending_acl',
    acl_attempts = 0,
    submit_attempts = 0,
    poll_attempts = 0,
    unknown_checks = 0,
    provider_job_id = NULL,
    submission_started_at = NULL,
    next_attempt_at = EXCLUDED.next_attempt_at,
    last_error_code = NULL,
    completed_at = NULL,
    dead_lettered_at = NULL,
    updated_at = EXCLUDED.updated_at
`, imageAssetID, sourceModerationRecordID, desiredPublic, now.UTC(), sourceModerationRecordID,
		purgeRequired)
	return result.RowsAffected == 1, result.Error
}

// ClaimDue 用 SKIP LOCKED 有界领取到期 delivery；外部调用必须在该短事务提交后执行。
func (ImageAccessOutboxRepository) ClaimDue(
	ctx context.Context,
	now time.Time,
	leaseUntil time.Time,
	leaseToken string,
	limit int,
) ([]ImageAccessDeliveryClaim, error) {
	if limit <= 0 || limit > 1000 {
		return nil, errors.New("image access claim limit must be between 1 and 1000")
	}
	claims := make([]ImageAccessDeliveryClaim, 0, limit)
	err := db.FromContext(ctx).Raw(`
WITH candidates AS (
    SELECT d.image_asset_id
    FROM image_access_deliveries AS d
    WHERE d.state NOT IN ('succeeded', 'dead_letter')
      AND d.next_attempt_at <= ?
      AND (d.lease_until IS NULL OR d.lease_until <= ?)
    ORDER BY d.next_attempt_at, d.image_asset_id
    FOR UPDATE SKIP LOCKED
    LIMIT ?
), claimed AS (
    UPDATE image_access_deliveries AS d
    SET lease_token = ?, lease_until = ?, updated_at = ?
    FROM candidates AS c
    WHERE d.image_asset_id = c.image_asset_id
    RETURNING d.*
)
SELECT claimed.*, asset.object_key, asset.public_url
FROM claimed
JOIN image_assets AS asset ON asset.id = claimed.image_asset_id
ORDER BY claimed.next_attempt_at, claimed.image_asset_id
`, now.UTC(), now.UTC(), limit, leaseToken, leaseUntil.UTC(), now.UTC()).Scan(&claims).Error
	return claims, err
}

// UpdateClaim 以 image_asset_id + generation + lease_token fencing 后更新状态。
func (ImageAccessOutboxRepository) UpdateClaim(
	ctx context.Context,
	claim ImageAccessDeliveryClaim,
	updates map[string]any,
) (bool, error) {
	if len(updates) == 0 {
		return false, errors.New("image access claim update is empty")
	}
	result := db.FromContext(ctx).Model(&model.ImageAccessDelivery{}).
		Where("image_asset_id = ? AND generation = ? AND lease_token = ?",
			claim.ImageAssetID, claim.Generation, valueOrEmpty(claim.LeaseToken)).
		Updates(updates)
	return result.RowsAffected == 1, result.Error
}

// ReleaseSuperseded 只释放仍属于旧 worker 的 lease，并让新代际立即可领取。
func (ImageAccessOutboxRepository) ReleaseSuperseded(
	ctx context.Context,
	imageAssetID uint64,
	leaseToken string,
	now time.Time,
) error {
	return db.FromContext(ctx).Model(&model.ImageAccessDelivery{}).
		Where("image_asset_id = ? AND lease_token = ?", imageAssetID, leaseToken).
		Updates(map[string]any{
			"lease_token": nil, "lease_until": nil, "next_attempt_at": now.UTC(),
			"updated_at": now.UTC(),
		}).Error
}

// CountByState 返回固定状态集合的 durable backlog；调用方不得把数据库自由文本变成 label。
func (ImageAccessOutboxRepository) CountByState(ctx context.Context) (map[string]int64, error) {
	type row struct {
		State string
		Count int64
	}
	var rows []row
	if err := db.FromContext(ctx).Model(&model.ImageAccessDelivery{}).
		Select("state, count(*) AS count").Group("state").Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[string]int64, len(rows))
	for _, item := range rows {
		counts[item.State] = item.Count
	}
	return counts, nil
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
