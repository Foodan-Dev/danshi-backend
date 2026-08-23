package repository

import (
	"context"
	"time"

	"gorm.io/gorm/clause"

	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
)

// ModerationAlertRepository 读取帖子粒度待复核队列并锁定跨任务抑制状态。
type ModerationAlertRepository struct{}

// CountPendingReviewQueue 使用与管理端分页完全相同的 queue_items 语义计数。
// 一个帖子无论有多少正文或图片 review 记录都只算一个条目。
func (ModerationAlertRepository) CountPendingReviewQueue(ctx context.Context) (int64, error) {
	cte, args := pendingModerationItemsCTE(nil)
	var count int64
	err := db.FromContext(ctx).Raw(cte+`SELECT count(*) FROM queue_items`, args...).Scan(&count).Error
	return count, err
}

// EnsureAndLockState 幂等创建并行检查共享的状态行，然后持有行锁直到当前事务结束。
func (ModerationAlertRepository) EnsureAndLockState(
	ctx context.Context,
	alertKey string,
	now time.Time,
) (*model.ModerationAlertState, error) {
	tx := db.FromContext(ctx)
	initial := model.ModerationAlertState{AlertKey: alertKey, UpdatedAt: now}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&initial).Error; err != nil {
		return nil, err
	}
	var state model.ModerationAlertState
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("alert_key = ?", alertKey).First(&state).Error
	return &state, NormalizeError(err)
}

// SaveState 保存本次检查结果；updated_at 由知道检查语义的应用层显式提供。
func (ModerationAlertRepository) SaveState(
	ctx context.Context,
	state *model.ModerationAlertState,
) error {
	result := db.FromContext(ctx).Model(&model.ModerationAlertState{}).
		Where("alert_key = ?", state.AlertKey).
		Updates(map[string]any{
			"active":              state.Active,
			"last_observed_count": state.LastObservedCount,
			"last_alerted_at":     state.LastAlertedAt,
			"updated_at":          state.UpdatedAt,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
