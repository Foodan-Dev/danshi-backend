package repository

import (
	"context"
	"time"

	"gorm.io/gorm/clause"

	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
)

// UserBanState 是一次完整的封禁字段组更新。
type UserBanState struct {
	IsPermanent bool
	Until       *time.Time
	Reason      *string
	ActorID     *uint64
}

// LockUserByID 锁定管理操作的目标用户。
func (AdminRepository) LockUserByID(
	ctx context.Context,
	userID uint64,
	opts QueryOptions,
) (*model.User, error) {
	var user model.User
	query := db.FromContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", userID)
	if !opts.IncludeDeleted {
		query = query.Where("deleted_at IS NULL")
	}
	if err := query.First(&user).Error; err != nil {
		return nil, NormalizeError(err)
	}
	return &user, nil
}

// UpdateUserBan 原子写入完整封禁字段组；数据库 CHECK 是最终防线。
func (AdminRepository) UpdateUserBan(
	ctx context.Context,
	userID uint64,
	state UserBanState,
	now time.Time,
) error {
	result := db.FromContext(ctx).Model(&model.User{}).Where("id = ? AND deleted_at IS NULL", userID).
		UpdateColumns(map[string]any{
			"ban_is_permanent": state.IsPermanent,
			"banned_until":     state.Until,
			"ban_reason":       state.Reason,
			"banned_by":        state.ActorID,
			"updated_at":       now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateUserBanRecord 追加一条不可变封禁状态变更记录。
func (AdminRepository) CreateUserBanRecord(
	ctx context.Context,
	record *model.UserBanRecord,
) error {
	return db.FromContext(ctx).Create(record).Error
}

// GrantUserRole 幂等创建角色绑定，并报告是否发生真实状态变更。
func (AdminRepository) GrantUserRole(
	ctx context.Context,
	userID uint64,
	role model.UserRole,
	actorID uint64,
	now time.Time,
) (bool, error) {
	result := db.FromContext(ctx).Exec(`
		INSERT INTO user_roles (user_id, role, granted_by, granted_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT DO NOTHING
	`, userID, role, actorID, now)
	return result.RowsAffected == 1, result.Error
}

// RevokeUserRole 幂等物理删除角色绑定，并报告是否发生真实状态变更。
func (AdminRepository) RevokeUserRole(
	ctx context.Context,
	userID uint64,
	role model.UserRole,
) (bool, error) {
	result := db.FromContext(ctx).Where("user_id = ? AND role = ?", userID, role).
		Delete(&model.UserRoleBinding{})
	return result.RowsAffected == 1, result.Error
}

// CreateUserRoleRecord 追加一条不可变角色变更记录。
func (AdminRepository) CreateUserRoleRecord(
	ctx context.Context,
	record *model.UserRoleRecord,
) error {
	return db.FromContext(ctx).Create(record).Error
}

// SoftDeletePost 标记管理员删除帖子，不触碰帖子内容、计数或关联。
func (AdminRepository) SoftDeletePost(
	ctx context.Context,
	postID uint64,
	actorID uint64,
	now time.Time,
) error {
	result := db.FromContext(ctx).Model(&model.Post{}).
		Where("id = ? AND deleted_at IS NULL", postID).
		UpdateColumns(map[string]any{
			"deleted_at":     now,
			"deleted_reason": model.DeleteReasonAdmin,
			"deleted_by":     actorID,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// RestorePost 清空机审软删除字段，内容与关联保持原样。
func (AdminRepository) RestorePost(ctx context.Context, postID uint64) error {
	result := db.FromContext(ctx).Model(&model.Post{}).
		Where("id = ? AND deleted_at IS NOT NULL AND deleted_reason = ?", postID, model.DeleteReasonModeration).
		UpdateColumns(map[string]any{
			"deleted_at":     nil,
			"deleted_reason": nil,
			"deleted_by":     nil,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// RestoreComment 清空机审软删除字段并把恢复审计的 pass 结论写回当前状态。
func (AdminRepository) RestoreComment(ctx context.Context, commentID uint64) error {
	result := db.FromContext(ctx).Model(&model.Comment{}).
		Where("id = ? AND deleted_at IS NOT NULL AND deleted_reason = ?", commentID, model.DeleteReasonModeration).
		UpdateColumns(map[string]any{
			"deleted_at":     nil,
			"deleted_reason": nil,
			"deleted_by":     nil,
			"moderation":     model.ModerationStatusPass,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateRestorationRecord 追加一条不可变恢复审计记录。
func (AdminRepository) CreateRestorationRecord(
	ctx context.Context,
	record *model.ModerationRecord,
) error {
	return db.FromContext(ctx).Create(record).Error
}
