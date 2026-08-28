package model

import "time"

// ImageModerationRetryState 是首次送审失败后的持久补审状态。
type ImageModerationRetryState string

const (
	// ImageModerationRetryPending 等待下一次有界补审。
	ImageModerationRetryPending ImageModerationRetryState = "pending"
	// ImageModerationRetryDeadLetter 表示自动重试预算已经耗尽。
	ImageModerationRetryDeadLetter ImageModerationRetryState = "dead_letter"
)

// ImageModerationRetry 每张图片最多保留一条补审记录；成功后删除，死信保留。
type ImageModerationRetry struct {
	ImageAssetID   uint64 `gorm:"primaryKey"`
	State          ImageModerationRetryState
	Attempts       int
	NextAttemptAt  *time.Time
	LeaseToken     *string
	LeaseUntil     *time.Time
	LastErrorCode  string
	DeadLetteredAt *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// TableName 返回图片补审队列表名。
func (ImageModerationRetry) TableName() string { return "image_moderation_retries" }
