package model

import (
	"encoding/json"
	"time"

	"github.com/lib/pq"
	"github.com/shopspring/decimal"
)

// ModerationRecord 是一次不可覆盖、不可删除的内容审核流水。
type ModerationRecord struct {
	ID               uint64 `gorm:"primaryKey"`
	PostID           *uint64
	CommentID        *uint64
	ImageAssetID     *uint64
	TagID            *uint64
	UserID           *uint64
	Field            *ModerationField
	PostHistoryID    *uint64
	CommentHistoryID *uint64
	Scene            ModerationScene
	Provider         ModerationProvider
	ProviderJobID    *string
	Verdict          ModerationVerdict
	Labels           pq.StringArray   `gorm:"type:text[]"`
	Score            *decimal.Decimal `gorm:"type:numeric(5,2)"`
	RawResponse      json.RawMessage  `gorm:"type:jsonb"`
	ReviewerID       *uint64
	ReviewedAt       *time.Time
	SupersedesID     *uint64
	CreatedAt        time.Time
}

// TableName 返回内容审核流水表名。
func (ModerationRecord) TableName() string { return "moderation_records" }
