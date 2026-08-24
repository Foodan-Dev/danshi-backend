package model

import "time"

// ImageAccessOutboxState 是审核图片 ACL 与 EdgeOne 刷新的持久状态机。
type ImageAccessOutboxState string

const (
	// ImageAccessPendingACL 等待幂等设置 COS ACL。
	ImageAccessPendingACL ImageAccessOutboxState = "pending_acl"
	// ImageAccessPendingSubmit 尚未开始本次 EdgeOne Create。
	ImageAccessPendingSubmit ImageAccessOutboxState = "pending_submit"
	// ImageAccessSubmitting 已持久化提交开始时间，未知结果只能先对账。
	ImageAccessSubmitting ImageAccessOutboxState = "submitting"
	// ImageAccessSubmitted 已持有 JobId，等待三个 Target 的终态。
	ImageAccessSubmitted ImageAccessOutboxState = "submitted"
	// ImageAccessSucceeded 表示 ACL 与 purge 均已收敛。
	ImageAccessSucceeded ImageAccessOutboxState = "succeeded"
	// ImageAccessDeadLetter 表示自动预算耗尽或存在不可安全重放的歧义。
	ImageAccessDeadLetter ImageAccessOutboxState = "dead_letter"
)

// ImageAccessIntent 把一条不可变审核流水映射为一次幂等的访问状态意图。
type ImageAccessIntent struct {
	ID                       uint64 `gorm:"primaryKey"`
	ImageAssetID             uint64
	SourceModerationRecordID uint64
	DesiredPublic            bool
	CreatedAt                time.Time
}

// TableName 返回访问状态意图表名。
func (ImageAccessIntent) TableName() string { return "image_access_intents" }

// ImageAccessDelivery 只保存资产 ID 与供应商控制字段；对象键和公开 URL 仍由 image_assets 管理。
type ImageAccessDelivery struct {
	ImageAssetID        uint64 `gorm:"primaryKey"`
	DesiredIntentID     uint64
	DesiredPublic       bool
	PurgeRequired       bool
	Generation          int64
	State               ImageAccessOutboxState
	ACLAttempts         int
	SubmitAttempts      int
	PollAttempts        int
	UnknownChecks       int
	ProviderJobID       *string
	SubmissionStartedAt *time.Time
	NextAttemptAt       time.Time
	LeaseToken          *string
	LeaseUntil          *time.Time
	LastErrorCode       *string
	CompletedAt         *time.Time
	DeadLetteredAt      *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// TableName 返回 durable outbox 的表名。
func (ImageAccessDelivery) TableName() string { return "image_access_deliveries" }
