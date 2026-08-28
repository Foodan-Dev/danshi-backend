package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
)

// ImageModerationRetryClaim 是带租约 token 的一次补审领取结果。
type ImageModerationRetryClaim struct {
	model.ImageModerationRetry
	ObjectKey  string
	Moderation model.ModerationStatus
}

// ImageModerationRetryRepository 管理首次送审失败后的持久补审队列。
type ImageModerationRetryRepository struct{}

// Enqueue 在上传事务内登记首次失败；重复调用不会复活已经死信的记录。
func (ImageModerationRetryRepository) Enqueue(
	ctx context.Context,
	imageAssetID uint64,
	nextAttemptAt time.Time,
	errorCode string,
	now time.Time,
) error {
	return db.FromContext(ctx).Exec(`
INSERT INTO image_moderation_retries (
    image_asset_id, state, attempts, next_attempt_at, last_error_code, created_at, updated_at
) VALUES (?, 'pending', 1, ?, ?, ?, ?)
ON CONFLICT (image_asset_id) DO NOTHING
`, imageAssetID, nextAttemptAt.UTC(), errorCode, now.UTC(), now.UTC()).Error
}

// ClaimDue 用 SKIP LOCKED 有界领取到期记录；外部调用必须在短事务提交后执行。
func (ImageModerationRetryRepository) ClaimDue(
	ctx context.Context,
	now time.Time,
	leaseUntil time.Time,
	leaseToken string,
	limit int,
) ([]ImageModerationRetryClaim, error) {
	if limit <= 0 || limit > 100 {
		return nil, errors.New("image moderation retry claim limit must be between 1 and 100")
	}
	claims := make([]ImageModerationRetryClaim, 0, limit)
	err := db.FromContext(ctx).Raw(`
WITH candidates AS (
    SELECT retry.image_asset_id
    FROM image_moderation_retries AS retry
    WHERE retry.state = 'pending'
      AND retry.next_attempt_at <= ?
      AND (retry.lease_until IS NULL OR retry.lease_until <= ?)
    ORDER BY retry.next_attempt_at, retry.image_asset_id
    FOR UPDATE SKIP LOCKED
    LIMIT ?
), claimed AS (
    UPDATE image_moderation_retries AS retry
    SET lease_token = ?, lease_until = ?, updated_at = ?
    FROM candidates
    WHERE retry.image_asset_id = candidates.image_asset_id
    RETURNING retry.*
)
SELECT claimed.*, asset.object_key, asset.moderation
FROM claimed
JOIN image_assets AS asset ON asset.id = claimed.image_asset_id
ORDER BY claimed.next_attempt_at, claimed.image_asset_id
`, now.UTC(), now.UTC(), limit, leaseToken, leaseUntil.UTC(), now.UTC()).Scan(&claims).Error
	return claims, err
}

// RescheduleClaim 用资产 ID 与租约 token 围栏后释放记录并安排下一次尝试。
func (ImageModerationRetryRepository) RescheduleClaim(
	ctx context.Context,
	claim ImageModerationRetryClaim,
	attempts int,
	nextAttemptAt time.Time,
	errorCode string,
	now time.Time,
) (bool, error) {
	result := db.FromContext(ctx).Model(&model.ImageModerationRetry{}).
		Where("image_asset_id = ? AND state = 'pending' AND lease_token = ?",
			claim.ImageAssetID, valueOrEmpty(claim.LeaseToken)).
		Updates(map[string]any{
			"attempts": attempts, "next_attempt_at": nextAttemptAt.UTC(),
			"lease_token": nil, "lease_until": nil, "last_error_code": errorCode,
			"updated_at": now.UTC(),
		})
	return result.RowsAffected == 1, result.Error
}

// DeadLetterClaim 用租约围栏把预算耗尽的记录转入死信。
func (ImageModerationRetryRepository) DeadLetterClaim(
	ctx context.Context,
	claim ImageModerationRetryClaim,
	attempts int,
	now time.Time,
) (bool, error) {
	result := db.FromContext(ctx).Model(&model.ImageModerationRetry{}).
		Where("image_asset_id = ? AND state = 'pending' AND lease_token = ?",
			claim.ImageAssetID, valueOrEmpty(claim.LeaseToken)).
		Updates(map[string]any{
			"state": model.ImageModerationRetryDeadLetter, "attempts": attempts,
			"next_attempt_at": nil, "lease_token": nil, "lease_until": nil,
			"last_error_code": "submit_exhausted", "dead_lettered_at": now.UTC(),
			"updated_at": now.UTC(),
		})
	return result.RowsAffected == 1, result.Error
}

// DeleteClaim 在供应商受理后用租约围栏删除补审记录。
func (ImageModerationRetryRepository) DeleteClaim(
	ctx context.Context,
	claim ImageModerationRetryClaim,
) (bool, error) {
	result := db.FromContext(ctx).
		Where("image_asset_id = ? AND state = 'pending' AND lease_token = ?",
			claim.ImageAssetID, valueOrEmpty(claim.LeaseToken)).
		Delete(&model.ImageModerationRetry{})
	return result.RowsAffected == 1, result.Error
}

// DeleteForAsset 在审核结论事务内清除可能由 response-unknown 留下的补审记录。
func (ImageModerationRetryRepository) DeleteForAsset(
	ctx context.Context,
	imageAssetID uint64,
) error {
	return db.FromContext(ctx).Where("image_asset_id = ?", imageAssetID).
		Delete(&model.ImageModerationRetry{}).Error
}

// CountByState 返回固定状态集合的补审队列数量。
func (ImageModerationRetryRepository) CountByState(
	ctx context.Context,
) (map[string]int64, error) {
	type row struct {
		State string
		Count int64
	}
	var rows []row
	if err := db.FromContext(ctx).Model(&model.ImageModerationRetry{}).
		Select("state, count(*) AS count").Group("state").Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[string]int64, len(rows))
	for _, item := range rows {
		counts[item.State] = item.Count
	}
	return counts, nil
}
