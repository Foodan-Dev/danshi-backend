package service

import (
	"context"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/pagination"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/ptime"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

// NotificationListInput 是通知列表筛选条件。
type NotificationListInput struct {
	IsRead     *bool
	Type       string
	Pagination pagination.CursorRequest
}

// NotificationSender 是通知发送者的公开信息。
type NotificationSender struct {
	ID        uint64  `json:"id"`
	Name      string  `json:"name"`
	AvatarURL *string `json:"avatar_url"`
}

// NotificationItem 是 API 的通知列表项。
type NotificationItem struct {
	ID          uint64                 `json:"id"`
	Type        model.NotificationType `json:"type"`
	Sender      NotificationSender     `json:"sender"`
	RelatedID   *uint64                `json:"related_id"`
	RelatedType *string                `json:"related_type"`
	PostID      *uint64                `json:"post_id"`
	Content     *string                `json:"content"`
	IsRead      bool                   `json:"is_read"`
	CreatedAt   ptime.Time             `json:"created_at"`
}

// NotificationList 是通知列表、分页和未读总数。
type NotificationList struct {
	Notifications []NotificationItem    `json:"notifications"`
	Pagination    pagination.CursorMeta `json:"pagination"`
	UnreadCount   int64                 `json:"unread_count"`
}

// NotificationStats 是未读通知统计。
type NotificationStats struct {
	UnreadCount int64 `json:"unread_count"`
}

// NotificationMarked 是 read-all 的实际更新数量。
type NotificationMarked struct {
	MarkedCount int64 `json:"marked_count"`
}

// NotificationService 实现通知查询与已读状态流转。
type NotificationService struct {
	notifications repository.NotificationRepository
	cursor        *pagination.CursorCodec
}

// NewNotificationService 创建通知服务。
func NewNotificationService() *NotificationService {
	return &NotificationService{
		cursor: pagination.NewEphemeralCursorCodec("notifications"),
	}
}

// NewNotificationServiceWithCursorSecret 创建使用运行时密钥认证游标的通知服务。
func NewNotificationServiceWithCursorSecret(cursorSecret string) *NotificationService {
	return &NotificationService{
		cursor: pagination.NewCursorCodec(cursorSecret, "notifications"),
	}
}

// List 分页返回当前用户通知，并附带不受筛选影响的未读总数。
func (s *NotificationService) List(
	ctx context.Context,
	recipientID uint64,
	input NotificationListInput,
) (*NotificationList, error) {
	typeFilter, err := parseNotificationType(input.Type)
	if err != nil {
		return nil, err
	}
	params, err := s.cursor.DecodeRequest(input.Pagination)
	if err != nil {
		return nil, err
	}
	rows, hasMore, err := s.notifications.FindPage(ctx, recipientID, repository.NotificationFilter{
		IsRead: input.IsRead, Type: typeFilter,
	}, params)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	meta, err := s.cursorMeta(rows, params.Limit, hasMore)
	if err != nil {
		return nil, err
	}
	unread, err := s.notifications.UnreadCount(ctx, recipientID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	items := make([]NotificationItem, 0, len(rows))
	for index := range rows {
		items = append(items, buildNotificationItem(&rows[index]))
	}
	return &NotificationList{Notifications: items, Pagination: meta, UnreadCount: unread}, nil
}

func (s *NotificationService) cursorMeta(
	rows []repository.NotificationRecord,
	limit int,
	hasMore bool,
) (pagination.CursorMeta, error) {
	meta := pagination.CursorMeta{Limit: limit, HasMore: hasMore}
	if !hasMore || len(rows) == 0 {
		return meta, nil
	}
	last := rows[len(rows)-1]
	token, err := s.cursor.Encode(pagination.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
	if err != nil {
		return pagination.CursorMeta{}, apierr.Internal(err)
	}
	meta.NextCursor = &token
	return meta, nil
}

// UnreadCount 返回当前用户未读通知数。
func (s *NotificationService) UnreadCount(ctx context.Context, recipientID uint64) (*NotificationStats, error) {
	count, err := s.notifications.UnreadCount(ctx, recipientID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return &NotificationStats{UnreadCount: count}, nil
}

// MarkRead 将属于当前用户的单条通知标记为已读。
func (s *NotificationService) MarkRead(ctx context.Context, notificationID, recipientID uint64) error {
	found, err := s.notifications.MarkRead(ctx, notificationID, recipientID)
	if err != nil {
		return apierr.Internal(err)
	}
	if !found {
		return apierr.NotFound(apierr.BizNotificationNotFound, "通知")
	}
	return nil
}

// MarkAllRead 标记当前用户的全部未读通知。
func (s *NotificationService) MarkAllRead(
	ctx context.Context,
	recipientID uint64,
) (*NotificationMarked, error) {
	count, err := s.notifications.MarkAllRead(ctx, recipientID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return &NotificationMarked{MarkedCount: count}, nil
}

func buildNotificationItem(record *repository.NotificationRecord) NotificationItem {
	name, avatarURL := record.SenderName, record.SenderAvatarURL
	if record.SenderDeletedAt != nil {
		name, avatarURL = "已注销用户", nil
	}
	item := NotificationItem{
		ID: record.ID, Type: record.Type,
		Sender:  NotificationSender{ID: record.SenderID, Name: name, AvatarURL: avatarURL},
		Content: record.Content, IsRead: record.IsRead, CreatedAt: ptime.Time(record.CreatedAt),
	}
	if record.RelatedPostID != nil {
		relatedType := "post"
		item.RelatedID, item.RelatedType, item.PostID = record.RelatedPostID, &relatedType, record.RelatedPostID
	} else if record.RelatedCommentID != nil {
		relatedType := "comment"
		item.RelatedID, item.RelatedType = record.RelatedCommentID, &relatedType
		item.PostID = record.CommentPostID
	}
	return item
}

func parseNotificationType(raw string) (*model.NotificationType, error) {
	if raw == "" {
		return nil, nil
	}
	value := model.NotificationType(raw)
	if value != model.NotificationTypeLikePost && value != model.NotificationTypeLikeComment &&
		value != model.NotificationTypeComment && value != model.NotificationTypeReply &&
		value != model.NotificationTypeMention && value != model.NotificationTypeFollow {
		return nil, apierr.InvalidField("type", apierr.FieldInvalidEnum, "type 取值不合法")
	}
	return &value, nil
}
