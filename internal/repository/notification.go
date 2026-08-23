package repository

import (
	"context"
	"time"

	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/pagination"
)

// NotificationRepository 是无状态的站内通知仓储。
type NotificationRepository struct{}

// Create 创建一条通知。
func (NotificationRepository) Create(ctx context.Context, notification *model.Notification) error {
	return db.FromContext(ctx).Create(notification).Error
}

// NotificationFilter 是通知列表的可选筛选条件。
type NotificationFilter struct {
	IsRead *bool
	Type   *model.NotificationType
}

// NotificationRecord 是通知主体、发送者与跳转帖子的一次查询结果。
type NotificationRecord struct {
	model.Notification
	SenderName      string
	SenderAvatarURL *string    `gorm:"column:sender_avatar_url"`
	SenderDeletedAt *time.Time `gorm:"column:sender_deleted_at"`
	CommentPostID   *uint64    `gorm:"column:comment_post_id"`
}

// FindPage 以 (created_at, id) 复合游标返回收件人的通知，不执行全表 COUNT。
func (NotificationRepository) FindPage(
	ctx context.Context,
	recipientID uint64,
	filter NotificationFilter,
	params pagination.CursorParams,
) ([]NotificationRecord, bool, error) {
	query := db.FromContext(ctx).Table("notifications AS n").Where("n.recipient_id = ?", recipientID)
	if filter.IsRead != nil {
		query = query.Where("n.is_read = ?", *filter.IsRead)
	}
	if filter.Type != nil {
		query = query.Where("n.type = ?", *filter.Type)
	}
	if params.After != nil {
		query = query.Where(
			"(n.created_at, n.id) < (?, ?)", params.After.CreatedAt, params.After.ID,
		)
	}
	rows := make([]NotificationRecord, 0, params.Limit+1)
	err := query.Select(notificationRecordColumns).
		Joins("JOIN users AS sender ON sender.id = n.sender_id").
		Joins("LEFT JOIN image_assets AS sender_avatar ON sender_avatar.id = sender.avatar_image_asset_id").
		Joins("LEFT JOIN comments AS related_comment ON related_comment.id = n.related_comment_id").
		Order("n.created_at DESC, n.id DESC").Limit(params.Limit + 1).
		Scan(&rows).Error
	if err != nil {
		return nil, false, err
	}
	hasMore := len(rows) > params.Limit
	if hasMore {
		rows = rows[:params.Limit]
	}
	return rows, hasMore, nil
}

// UnreadCount 返回收件人的全部未读数，不受列表筛选影响。
func (NotificationRepository) UnreadCount(ctx context.Context, recipientID uint64) (int64, error) {
	var count int64
	err := db.FromContext(ctx).Model(&model.Notification{}).
		Where("recipient_id = ? AND NOT is_read", recipientID).Count(&count).Error
	return count, err
}

// MarkRead 按收件人归属标记单条通知，并隐藏“不存在”和“不属于你”的差异。
func (NotificationRepository) MarkRead(ctx context.Context, notificationID, recipientID uint64) (bool, error) {
	result := db.FromContext(ctx).Model(&model.Notification{}).
		Where("id = ? AND recipient_id = ?", notificationID, recipientID).
		Updates(map[string]any{"is_read": true, "updated_at": time.Now().UTC()})
	return result.RowsAffected == 1, result.Error
}

// MarkAllRead 批量标记收件人当前全部未读通知。
func (NotificationRepository) MarkAllRead(ctx context.Context, recipientID uint64) (int64, error) {
	result := db.FromContext(ctx).Model(&model.Notification{}).
		Where("recipient_id = ? AND NOT is_read", recipientID).
		Updates(map[string]any{"is_read": true, "updated_at": time.Now().UTC()})
	return result.RowsAffected, result.Error
}

const notificationRecordColumns = `
	n.*,
	sender.name AS sender_name,
	sender.deleted_at AS sender_deleted_at,
	sender_avatar.public_url AS sender_avatar_url,
	related_comment.post_id AS comment_post_id`
