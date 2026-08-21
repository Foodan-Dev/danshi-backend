package model

import "time"

// Notification 是六类站内通知之一。
type Notification struct {
	ID               uint64 `gorm:"primaryKey"`
	RecipientID      uint64
	SenderID         uint64
	Type             NotificationType
	RelatedPostID    *uint64
	RelatedCommentID *uint64
	Content          *string
	IsRead           bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// TableName 返回站内通知表名。
func (Notification) TableName() string { return "notifications" }
