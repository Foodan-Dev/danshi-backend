package repository

import (
	"context"
	"time"

	"gorm.io/gorm/clause"

	"github.com/jingyijun/danshi_backend_go/internal/infra/db"
	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/pagination"
)

// CommentRepository 是无状态的评论仓储；事务句柄始终来自 context。
type CommentRepository struct{}

// FindByID 返回评论主体，包括软删除行。
func (CommentRepository) FindByID(ctx context.Context, commentID uint64) (*model.Comment, error) {
	var comment model.Comment
	if err := db.FromContext(ctx).Where("id = ?", commentID).First(&comment).Error; err != nil {
		return nil, NormalizeError(err)
	}
	return &comment, nil
}

// LockByID 在编辑、删除和回复前锁定评论主体。
func (CommentRepository) LockByID(ctx context.Context, commentID uint64) (*model.Comment, error) {
	var comment model.Comment
	err := db.FromContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", commentID).First(&comment).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &comment, nil
}

// Create 同事务创建评论主体；计数列只能使用数据库默认值。
func (CommentRepository) Create(ctx context.Context, comment *model.Comment) error {
	return db.FromContext(ctx).Create(comment).Error
}

// FindRootPage 分页返回一帖的楼主评论；软删除行保留为展示占位。
func (CommentRepository) FindRootPage(
	ctx context.Context,
	postID uint64,
	sortBy string,
	params pagination.Params,
) ([]model.Comment, pagination.Meta, error) {
	query := db.FromContext(ctx).Model(&model.Comment{}).
		Where("post_id = ? AND root_id IS NULL", postID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, pagination.Meta{}, err
	}
	if sortBy == "hot" {
		query = query.Order("like_count DESC, created_at DESC, id DESC")
	} else {
		query = query.Order("created_at DESC, id DESC")
	}
	comments := make([]model.Comment, 0, params.Limit)
	if err := query.Offset(params.Offset()).Limit(params.Limit).Find(&comments).Error; err != nil {
		return nil, pagination.Meta{}, err
	}
	return comments, pagination.NewMeta(params, total), nil
}

// FindReplyPreviews 为每个楼批量取最新 limit 条回复，最终按时间正序展示。
func (CommentRepository) FindReplyPreviews(
	ctx context.Context,
	rootIDs []uint64,
	limit int,
) ([]model.Comment, error) {
	rootIDs = uniqueSortedIDs(rootIDs)
	if len(rootIDs) == 0 || limit <= 0 {
		return []model.Comment{}, nil
	}
	comments := make([]model.Comment, 0, len(rootIDs)*limit)
	err := db.FromContext(ctx).Raw(`
		WITH ranked AS (
			SELECT c.*,
			       row_number() OVER (
			           PARTITION BY c.root_id
			           ORDER BY c.created_at DESC, c.id DESC
			       ) AS preview_rank
			FROM comments AS c
			WHERE c.root_id IN ?
		)
		SELECT *
		FROM ranked
		WHERE preview_rank <= ?
		ORDER BY root_id, created_at, id
	`, rootIDs, limit).Scan(&comments).Error
	return comments, err
}

// FindReplyPage 分页返回某一楼内的全部回复，按时间正序拍扁展示。
func (CommentRepository) FindReplyPage(
	ctx context.Context,
	rootID uint64,
	params pagination.Params,
) ([]model.Comment, pagination.Meta, error) {
	query := db.FromContext(ctx).Model(&model.Comment{}).Where("root_id = ?", rootID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, pagination.Meta{}, err
	}
	comments := make([]model.Comment, 0, params.Limit)
	err := query.Order("created_at, id").Offset(params.Offset()).Limit(params.Limit).
		Find(&comments).Error
	if err != nil {
		return nil, pagination.Meta{}, err
	}
	return comments, pagination.NewMeta(params, total), nil
}

// UpdateContent 写入评论主体的新正文。调用方必须先锁行并在同事务追加历史。
func (CommentRepository) UpdateContent(
	ctx context.Context,
	commentID uint64,
	content string,
	updatedAt time.Time,
) error {
	result := db.FromContext(ctx).Model(&model.Comment{}).Where("id = ?", commentID).
		Updates(map[string]any{"content": content, "updated_at": updatedAt})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// SoftDelete 按指定来源软删除评论，不清空正文，也不物理删除回复。
func (CommentRepository) SoftDelete(
	ctx context.Context,
	commentID uint64,
	reason model.DeleteReason,
	actorID *uint64,
	deletedAt time.Time,
) error {
	result := db.FromContext(ctx).Model(&model.Comment{}).
		Where("id = ? AND deleted_at IS NULL", commentID).
		UpdateColumns(map[string]any{
			"deleted_at": deletedAt, "deleted_reason": reason, "deleted_by": actorID,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// LatestHistory 返回评论当前最新的全量正文版本。
func (CommentRepository) LatestHistory(ctx context.Context, commentID uint64) (*model.CommentHistory, error) {
	var history model.CommentHistory
	err := db.FromContext(ctx).Where("comment_id = ?", commentID).
		Order("revision DESC").First(&history).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &history, nil
}

// CreateHistory 追加不可篡改的评论正文版本。
func (CommentRepository) CreateHistory(ctx context.Context, history *model.CommentHistory) error {
	return db.FromContext(ctx).Create(history).Error
}

// ListHistories 按版本倒序返回评论全量历史。
func (CommentRepository) ListHistories(ctx context.Context, commentID uint64) ([]model.CommentHistory, error) {
	histories := make([]model.CommentHistory, 0)
	err := db.FromContext(ctx).Where("comment_id = ?", commentID).
		Order("revision DESC").Find(&histories).Error
	return histories, err
}

// ReplaceMentions 物理替换评论正文中的提及关联。
func (CommentRepository) ReplaceMentions(ctx context.Context, commentID uint64, userIDs []uint64) error {
	if err := db.FromContext(ctx).Where("comment_id = ?", commentID).
		Delete(&model.CommentMention{}).Error; err != nil {
		return err
	}
	userIDs = uniqueSortedIDs(userIDs)
	if len(userIDs) == 0 {
		return nil
	}
	mentions := make([]model.CommentMention, 0, len(userIDs))
	for _, userID := range userIDs {
		mentions = append(mentions, model.CommentMention{CommentID: commentID, UserID: userID})
	}
	return db.FromContext(ctx).Create(&mentions).Error
}

// MentionIDs 返回评论当前提及的用户 id。
func (CommentRepository) MentionIDs(ctx context.Context, commentID uint64) ([]uint64, error) {
	var userIDs []uint64
	err := db.FromContext(ctx).Model(&model.CommentMention{}).Where("comment_id = ?", commentID).
		Order("user_id").Pluck("user_id", &userIDs).Error
	return userIDs, err
}

// ActiveUserIDs 返回一组 id 中当前存在且未注销的用户。
func (CommentRepository) ActiveUserIDs(ctx context.Context, userIDs []uint64) ([]uint64, error) {
	userIDs = uniqueSortedIDs(userIDs)
	if len(userIDs) == 0 {
		return []uint64{}, nil
	}
	var found []uint64
	err := db.FromContext(ctx).Model(&model.User{}).
		Where("id IN ? AND deleted_at IS NULL", userIDs).Order("id").Pluck("id", &found).Error
	return found, err
}

// Like 幂等创建点赞动作，并返回本次是否真的插入了新行。
func (CommentRepository) Like(ctx context.Context, userID, commentID uint64) (bool, error) {
	result := db.FromContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
		Create(&model.CommentLike{UserID: userID, CommentID: commentID})
	return result.RowsAffected == 1, result.Error
}

// Unlike 幂等物理删除点赞动作。
func (CommentRepository) Unlike(ctx context.Context, userID, commentID uint64) error {
	return DeleteAssociation(ctx, &model.CommentLike{UserID: userID, CommentID: commentID})
}

// LikeCount 读取数据库触发器维护后的评论点赞数。
func (CommentRepository) LikeCount(ctx context.Context, commentID uint64) (int32, error) {
	var count int32
	result := db.FromContext(ctx).Model(&model.Comment{}).Select("like_count").
		Where("id = ?", commentID).Scan(&count)
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected == 0 {
		return 0, ErrNotFound
	}
	return count, nil
}

// CreateModerationRecord 追加评论审核流水。
func (CommentRepository) CreateModerationRecord(
	ctx context.Context,
	record *model.ModerationRecord,
) error {
	return db.FromContext(ctx).Create(record).Error
}

// CreateNotifications 批量写入同一评论动作产生的通知。
func (CommentRepository) CreateNotifications(ctx context.Context, rows []model.Notification) error {
	if len(rows) == 0 {
		return nil
	}
	return db.FromContext(ctx).Create(&rows).Error
}
