package repository

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/pagination"
)

// CommentRepository 是无状态的评论仓储；事务句柄始终来自 context。
type CommentRepository struct{}

const commentVisibilityPredicate = `(
	(c.deleted_at IS NULL AND c.moderation = ?)
	OR (
		c.author_id = ? AND c.moderation <> ?
		AND (c.deleted_at IS NULL OR c.deleted_reason = ?)
	)
)`

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

// FindRootPage 以稳定复合游标返回当前用户可见的楼主评论。
func (CommentRepository) FindRootPage(
	ctx context.Context,
	postID uint64,
	currentUserID uint64,
	sortBy string,
	params pagination.CursorParams,
) ([]model.Comment, bool, error) {
	query := visibleCommentQuery(ctx, currentUserID).
		Where("c.post_id = ? AND c.root_id IS NULL", postID)
	query, err := applyRootCommentCursor(query, sortBy, params.After)
	if err != nil {
		return nil, false, err
	}
	comments := make([]model.Comment, 0, params.Limit+1)
	if err := query.Limit(params.Limit + 1).Scan(&comments).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(comments) > params.Limit
	if hasMore {
		comments = comments[:params.Limit]
	}
	return comments, hasMore, nil
}

func applyRootCommentCursor(
	query *gorm.DB,
	sortBy string,
	after *pagination.Cursor,
) (*gorm.DB, error) {
	if sortBy == "hot" {
		return applyHotCommentCursor(query, after)
	}
	if after != nil {
		query = query.Where("(c.created_at, c.id) > (?, ?)", after.CreatedAt, after.ID)
	}
	return query.Order("c.created_at ASC, c.id ASC"), nil
}

func applyHotCommentCursor(query *gorm.DB, after *pagination.Cursor) (*gorm.DB, error) {
	if after != nil && after.Rank == nil {
		return nil, pagination.ErrInvalidCursor
	}
	if after != nil {
		query = query.Where(
			"(c.like_count, c.created_at, c.id) < (?, ?, ?)",
			*after.Rank, after.CreatedAt, after.ID,
		)
	}
	return query.Order("c.like_count DESC, c.created_at DESC, c.id DESC"), nil
}

// FindReplyPreviews 为每个楼批量取最新 limit 条回复，最终按时间正序展示。
func (CommentRepository) FindReplyPreviews(
	ctx context.Context,
	rootIDs []uint64,
	currentUserID uint64,
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
			WHERE c.root_id IN ? AND `+commentVisibilityPredicate+`
		)
		SELECT *
		FROM ranked
		WHERE preview_rank <= ?
		ORDER BY root_id, created_at, id
	`, rootIDs, model.ModerationStatusPass, currentUserID, model.ModerationStatusPass,
		model.DeleteReasonModeration, limit).Scan(&comments).Error
	return comments, err
}

// FindReplyPage 以正序复合游标返回某一楼内当前用户可见的回复。
func (CommentRepository) FindReplyPage(
	ctx context.Context,
	rootID uint64,
	currentUserID uint64,
	params pagination.CursorParams,
) ([]model.Comment, bool, error) {
	query := visibleCommentQuery(ctx, currentUserID).Where("c.root_id = ?", rootID)
	if params.After != nil {
		query = query.Where(
			"(c.created_at, c.id) > (?, ?)", params.After.CreatedAt, params.After.ID,
		)
	}
	comments := make([]model.Comment, 0, params.Limit+1)
	err := query.Order("c.created_at, c.id").Limit(params.Limit + 1).Scan(&comments).Error
	if err != nil {
		return nil, false, err
	}
	hasMore := len(comments) > params.Limit
	if hasMore {
		comments = comments[:params.Limit]
	}
	return comments, hasMore, nil
}

func visibleCommentQuery(ctx context.Context, currentUserID uint64) *gorm.DB {
	return db.FromContext(ctx).Table("comments AS c").Where(
		commentVisibilityPredicate,
		model.ModerationStatusPass, currentUserID, model.ModerationStatusPass,
		model.DeleteReasonModeration,
	)
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

// ApplyModeration 写回评论当前审核状态；block 软删除，重新送审通过或待复核时撤销机审删除。
func (CommentRepository) ApplyModeration(
	ctx context.Context,
	commentID uint64,
	moderation model.ModerationStatus,
	now time.Time,
) error {
	updates := map[string]any{"moderation": moderation}
	if moderation == model.ModerationStatusBlock {
		updates["deleted_at"] = now
		updates["deleted_reason"] = model.DeleteReasonModeration
		updates["deleted_by"] = nil
	} else {
		updates["deleted_at"] = nil
		updates["deleted_reason"] = nil
		updates["deleted_by"] = nil
	}
	result := db.FromContext(ctx).Model(&model.Comment{}).Where("id = ?", commentID).
		UpdateColumns(updates)
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

// CurrentContentRevision 返回主表当前正文的版本号，即最大历史 revision + 1。
func (CommentRepository) CurrentContentRevision(ctx context.Context, commentID uint64) (int32, error) {
	var revision int32
	err := db.FromContext(ctx).Model(&model.CommentHistory{}).
		Select("COALESCE(max(revision), 0) + 1").Where("comment_id = ?", commentID).Scan(&revision).Error
	return revision, err
}

// NextHistoryRevision 返回下一条旧版本正文的连续 revision。调用方必须已锁定评论主体。
func (r CommentRepository) NextHistoryRevision(ctx context.Context, commentID uint64) (int32, error) {
	return r.CurrentContentRevision(ctx, commentID)
}

// CreateHistory 追加不可篡改的被替换评论正文。
func (CommentRepository) CreateHistory(ctx context.Context, history *model.CommentHistory) error {
	return db.FromContext(ctx).Create(history).Error
}

// ListHistories 按版本倒序返回评论被替换的旧正文。
func (CommentRepository) ListHistories(ctx context.Context, commentID uint64) ([]model.CommentHistory, error) {
	histories := make([]model.CommentHistory, 0)
	err := db.FromContext(ctx).Where("comment_id = ?", commentID).
		Order("revision DESC").Find(&histories).Error
	return histories, err
}

// ListHistoryModeration 返回每个旧版本最新一次机器文本审核结论。
func (CommentRepository) ListHistoryModeration(
	ctx context.Context,
	commentID uint64,
) ([]model.ModerationRecord, error) {
	records := make([]model.ModerationRecord, 0)
	err := db.FromContext(ctx).Raw(`
		SELECT DISTINCT ON (content_revision) mr.*
		FROM moderation_records AS mr
		WHERE mr.comment_id = ? AND mr.content_revision IS NOT NULL AND mr.reviewer_id IS NULL
		ORDER BY content_revision, created_at DESC, id DESC
	`, commentID).Scan(&records).Error
	return records, err
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

// ActiveUsers 返回一组 id 中当前存在且未注销的用户及昵称。
func (CommentRepository) ActiveUsers(ctx context.Context, userIDs []uint64) ([]model.User, error) {
	userIDs = uniqueSortedIDs(userIDs)
	if len(userIDs) == 0 {
		return []model.User{}, nil
	}
	var found []model.User
	err := db.FromContext(ctx).Model(&model.User{}).
		Select("id", "name").Where("id IN ? AND deleted_at IS NULL", userIDs).
		Order("id").Find(&found).Error
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
