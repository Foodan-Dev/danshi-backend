package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
)

// VerificationEmailDeliveryClaim 是一次带租约的验证码邮件 outbox 领取结果。
type VerificationEmailDeliveryClaim struct {
	model.VerificationEmailDelivery
	CurrentCodeDigest string `gorm:"column:current_code_digest"`
}

// VerificationEmailDeliveryRepository 管理验证码邮件 outbox 的短事务状态迁移。
type VerificationEmailDeliveryRepository struct{}

// Enqueue 为当前一次验证码发送追加一条邮件投递任务；同一 challenge 的旧任务
// 由 ClaimDue 的 digest 校验取消，不能复用或覆盖，否则会破坏重发的审计与重试状态。
func (VerificationEmailDeliveryRepository) Enqueue(
	ctx context.Context,
	challenge *model.EmailVerificationCode,
	email string,
	purpose model.VerificationPurpose,
	codeDigest string,
	ciphertext []byte,
	now time.Time,
) (uint64, error) {
	delivery := &model.VerificationEmailDelivery{
		ChallengeID: challenge.ID, Email: email, Purpose: purpose,
		CodeDigest: codeDigest, CodeCiphertext: ciphertext,
		State: model.VerificationEmailDeliveryPending, Attempts: 0,
		CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	nextAttemptAt := now.UTC()
	delivery.NextAttemptAt = &nextAttemptAt
	if err := db.FromContext(ctx).Create(delivery).Error; err != nil {
		return 0, err
	}
	return delivery.ID, nil
}

// ClaimDue 用 SKIP LOCKED 有界领取到期任务；外部发信必须在该短事务提交后执行。
func (VerificationEmailDeliveryRepository) ClaimDue(
	ctx context.Context,
	now time.Time,
	leaseUntil time.Time,
	leaseToken string,
	limit int,
) ([]VerificationEmailDeliveryClaim, error) {
	if limit <= 0 || limit > 1000 {
		return nil, errors.New("verification email delivery claim limit must be between 1 and 1000")
	}
	claims := make([]VerificationEmailDeliveryClaim, 0, limit)
	err := db.FromContext(ctx).Raw(`
WITH candidates AS (
    SELECT d.id
    FROM verification_email_deliveries AS d
    WHERE d.state IN ('pending', 'sending')
      AND d.next_attempt_at <= ?
      AND (d.lease_until IS NULL OR d.lease_until <= ?)
    ORDER BY d.next_attempt_at, d.id
    FOR UPDATE SKIP LOCKED
    LIMIT ?
), claimed AS (
    UPDATE verification_email_deliveries AS d
    SET state = 'sending', lease_token = ?, lease_until = ?, updated_at = ?
    FROM candidates AS c
    WHERE d.id = c.id
    RETURNING d.*
)
SELECT claimed.*,
       CASE WHEN challenge.code_digest = claimed.code_digest
                  AND challenge.consumed_at IS NULL
                  AND challenge.expires_at > ?
                  AND (claimed.purpose <> 'password_reset' OR EXISTS (
                      SELECT 1 FROM users
                      WHERE lower(users.email) = lower(claimed.email)
                        AND users.deleted_at IS NULL
                  ))
            THEN challenge.code_digest ELSE '' END AS current_code_digest
FROM claimed
LEFT JOIN email_verification_codes AS challenge ON challenge.id = claimed.challenge_id
ORDER BY claimed.next_attempt_at, claimed.id
`, now.UTC(), now.UTC(), limit, leaseToken, leaseUntil.UTC(), now.UTC(), now.UTC()).Scan(&claims).Error
	return claims, err
}

// UpdateClaim 以 id + lease token fencing 后更新状态，避免过期 worker 覆盖新状态。
func (VerificationEmailDeliveryRepository) UpdateClaim(
	ctx context.Context,
	claim VerificationEmailDeliveryClaim,
	updates map[string]any,
) (bool, error) {
	if len(updates) == 0 {
		return false, errors.New("verification email delivery claim update is empty")
	}
	result := db.FromContext(ctx).Model(&model.VerificationEmailDelivery{}).
		Where("id = ? AND state = 'sending' AND lease_token = ?", claim.ID, stringValue(claim.LeaseToken)).
		Updates(updates)
	return result.RowsAffected == 1, result.Error
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
