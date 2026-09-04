package model

import "time"

// VerificationEmailDeliveryState 是验证码邮件 outbox 的生命周期状态。
type VerificationEmailDeliveryState string

const (
	VerificationEmailDeliveryPending    VerificationEmailDeliveryState = "pending"
	VerificationEmailDeliverySending    VerificationEmailDeliveryState = "sending"
	VerificationEmailDeliverySent       VerificationEmailDeliveryState = "sent"
	VerificationEmailDeliveryCanceled   VerificationEmailDeliveryState = "canceled"
	VerificationEmailDeliveryDeadLetter VerificationEmailDeliveryState = "dead_letter"
)

// VerificationEmailDelivery 是验证码邮件的 durable outbox 行；验证码只保存加密密文。
type VerificationEmailDelivery struct {
	ID             uint64 `gorm:"primaryKey"`
	ChallengeID    uint64
	Email          string
	Purpose        VerificationPurpose
	CodeDigest     string
	CodeCiphertext []byte `gorm:"type:bytea"`
	State          VerificationEmailDeliveryState
	Attempts       int32
	NextAttemptAt  *time.Time
	LeaseToken     *string
	LeaseUntil     *time.Time
	LastErrorCode  *string
	SentAt         *time.Time
	CanceledAt     *time.Time
	DeadLetteredAt *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// TableName 返回验证码邮件 outbox 表名。
func (VerificationEmailDelivery) TableName() string { return "verification_email_deliveries" }
