package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
)

// UserRepository 是无状态的用户仓储；零值即可使用。
type UserRepository struct{}

// FindByEmail 按邮箱大小写不敏感查找用户；默认排除已注销行。
// 注册唯一性检查可显式传 QueryOptions{IncludeDeleted: true}，与数据库唯一索引保持一致。
func (UserRepository) FindByEmail(
	ctx context.Context,
	email string,
	options ...QueryOptions,
) (*model.User, error) {
	var user model.User
	query := db.FromContext(ctx).Where("lower(email) = lower(?)", email)
	if len(options) == 0 || !options[0].IncludeDeleted {
		query = query.Where("deleted_at IS NULL")
	}
	err := query.First(&user).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &user, nil
}

// FindByName 按大小写不敏感的公开 name 查找可登录用户。
func (UserRepository) FindByName(ctx context.Context, name string) (*model.User, error) {
	var user model.User
	err := db.FromContext(ctx).
		Where("lower(name) = lower(?) AND deleted_at IS NULL", name).
		First(&user).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &user, nil
}

// Create 创建用户，唯一性依赖 schema 的 lower(email) 唯一索引。
func (UserRepository) Create(ctx context.Context, user *model.User) error {
	return db.FromContext(ctx).Create(user).Error
}

// ClaimName 追加一条 name 占用记录。同一账号回用自己的历史 name 是幂等的；
// 其他账号已占用时由唯一约束拒绝。
func (UserRepository) ClaimName(ctx context.Context, userID uint64, name string, now time.Time) error {
	result := db.FromContext(ctx).Exec(`
		INSERT INTO user_name_claims (user_id, name, created_at)
		VALUES (?, ?, ?)
		ON CONFLICT DO NOTHING
	`, userID, name, now)
	if result.Error != nil || result.RowsAffected == 1 {
		return result.Error
	}
	var ownerID uint64
	result = db.FromContext(ctx).Raw(
		"SELECT user_id FROM user_name_claims WHERE lower(name) = lower(?)", name,
	).Scan(&ownerID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	if ownerID != userID {
		return ErrAlreadyExists
	}
	return nil
}

// FindRoles 返回用户当前绑定的全部管理角色，顺序稳定。
func (UserRepository) FindRoles(ctx context.Context, userID uint64) ([]model.UserRole, error) {
	var row struct {
		Roles pq.StringArray `gorm:"column:roles;type:text[]"`
	}
	result := db.FromContext(ctx).Raw(`
		SELECT ARRAY(
			SELECT role FROM user_roles WHERE user_id = ? ORDER BY role
		) AS roles
	`, userID).Scan(&row)
	if result.Error != nil {
		return nil, result.Error
	}
	return roleValues(row.Roles), nil
}

// FindAvatarURL 返回用户当前头像资产的公开地址；未设置头像时返回 nil。
func (UserRepository) FindAvatarURL(ctx context.Context, userID uint64) (*string, error) {
	var url *string
	result := db.FromContext(ctx).Raw(`
		SELECT a.public_url
		FROM users AS u
		JOIN image_assets AS a ON a.id = u.avatar_image_asset_id
		WHERE u.id = ? AND u.deleted_at IS NULL
	`, userID).Scan(&url)
	if result.Error != nil {
		return nil, result.Error
	}
	return url, nil
}

// IsUniqueViolation 判断数据库唯一约束冲突，供 service 映射成稳定业务错误。
func IsUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

// VerificationCodeRepository 是无状态的邮箱验证码仓储。
type VerificationCodeRepository struct{}

// LockOrCreate 幂等创建挑战行并加行锁，串行化同一邮箱的发送计数更新。
func (VerificationCodeRepository) LockOrCreate(
	ctx context.Context,
	email string,
	purpose model.VerificationPurpose,
	now time.Time,
) (*model.EmailVerificationCode, error) {
	tx := db.FromContext(ctx)
	if err := tx.Exec(`
		INSERT INTO email_verification_codes
			(email, purpose, code_digest, expires_at, send_window_started_at,
			 send_count, failed_attempts, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 0, 0, ?, ?)
		ON CONFLICT DO NOTHING
	`, email, purpose, zeroDigest, now, now, now, now).Error; err != nil {
		return nil, err
	}

	var challenge model.EmailVerificationCode
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("lower(email) = lower(?) AND purpose = ?", email, purpose).
		First(&challenge).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &challenge, nil
}

// LockExisting 加行锁读取已有挑战行。
func (VerificationCodeRepository) LockExisting(
	ctx context.Context,
	email string,
	purpose model.VerificationPurpose,
) (*model.EmailVerificationCode, error) {
	var challenge model.EmailVerificationCode
	err := db.FromContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("lower(email) = lower(?) AND purpose = ?", email, purpose).
		First(&challenge).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &challenge, nil
}

// SaveState 写回验证码发送、校验和消费状态。
func (VerificationCodeRepository) SaveState(
	ctx context.Context,
	challenge *model.EmailVerificationCode,
	now time.Time,
) error {
	return db.FromContext(ctx).Model(&model.EmailVerificationCode{}).
		Where("id = ?", challenge.ID).
		Updates(map[string]any{
			"code_digest":            challenge.CodeDigest,
			"expires_at":             challenge.ExpiresAt,
			"last_sent_at":           challenge.LastSentAt,
			"send_window_started_at": challenge.SendWindowStartedAt,
			"send_count":             challenge.SendCount,
			"failed_attempts":        challenge.FailedAttempts,
			"consumed_at":            challenge.ConsumedAt,
			"updated_at":             now,
		}).Error
}

// SessionRepository 是无状态的会话仓储。
type SessionRepository struct{}

// Identity 是一次鉴权查询返回的会话与用户身份。
type Identity struct {
	Session model.UserSession
	User    model.User
}

type identityRow struct {
	SessionID        uint64     `gorm:"column:session_id"`
	SessionUserID    uint64     `gorm:"column:session_user_id"`
	SessionLastSeen  time.Time  `gorm:"column:session_last_seen_at"`
	SessionExpiresAt time.Time  `gorm:"column:session_expires_at"`
	SessionRevokedAt *time.Time `gorm:"column:session_revoked_at"`
	UserID           uint64     `gorm:"column:user_id"`
	Email            string     `gorm:"column:email"`
	PasswordHash     string     `gorm:"column:password_hash"`
	Name             string     `gorm:"column:name"`
	Gender           *model.Gender
	Bio              *string
	AvatarID         *uint64        `gorm:"column:avatar_image_asset_id"`
	Roles            pq.StringArray `gorm:"column:roles;type:text[]"`
	BanIsPermanent   bool
	BannedUntil      *time.Time
	BanReason        *string
	BannedBy         *uint64
	DeletedAt        *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// FindActiveIdentity 以一次 JOIN 同时验证 sid、sub、会话状态与用户状态。
func (SessionRepository) FindActiveIdentity(
	ctx context.Context,
	sessionID uint64,
	userID uint64,
	now time.Time,
) (*Identity, error) {
	var row identityRow
	result := db.FromContext(ctx).Raw(`
		SELECT
			s.id AS session_id, s.user_id AS session_user_id,
			s.last_seen_at AS session_last_seen_at, s.expires_at AS session_expires_at,
			s.revoked_at AS session_revoked_at,
			u.id AS user_id, u.email, u.password_hash, u.name, u.gender, u.bio,
			u.avatar_image_asset_id, u.ban_is_permanent, u.banned_until,
			u.ban_reason, u.banned_by, u.deleted_at, u.created_at, u.updated_at,
			COALESCE(
				array_agg(ur.role ORDER BY ur.role) FILTER (WHERE ur.role IS NOT NULL),
				ARRAY[]::varchar[]
			) AS roles
		FROM user_sessions AS s
		JOIN users AS u ON u.id = s.user_id
		LEFT JOIN user_roles AS ur ON ur.user_id = u.id
		WHERE s.id = ? AND s.user_id = ?
		  AND s.revoked_at IS NULL AND s.expires_at > ?
		  AND u.deleted_at IS NULL
		  AND NOT (u.ban_is_permanent OR COALESCE(u.banned_until > ?, false))
		GROUP BY s.id, u.id
	`, sessionID, userID, now, now).Scan(&row)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return row.identity(), nil
}

// FindRefreshIdentity 校验 refresh 摘要与会话状态，仍只执行一次 JOIN。
func (SessionRepository) FindRefreshIdentity(
	ctx context.Context,
	sessionID uint64,
	userID uint64,
	digest string,
	now time.Time,
) (*Identity, error) {
	var row identityRow
	result := db.FromContext(ctx).Raw(`
		SELECT
			s.id AS session_id, s.user_id AS session_user_id,
			s.last_seen_at AS session_last_seen_at, s.expires_at AS session_expires_at,
			s.revoked_at AS session_revoked_at,
			u.id AS user_id, u.email, u.password_hash, u.name, u.gender, u.bio,
			u.avatar_image_asset_id, u.ban_is_permanent, u.banned_until,
			u.ban_reason, u.banned_by, u.deleted_at, u.created_at, u.updated_at,
			COALESCE(
				array_agg(ur.role ORDER BY ur.role) FILTER (WHERE ur.role IS NOT NULL),
				ARRAY[]::varchar[]
			) AS roles
		FROM user_sessions AS s
		JOIN users AS u ON u.id = s.user_id
		LEFT JOIN user_roles AS ur ON ur.user_id = u.id
		WHERE s.id = ? AND s.user_id = ? AND s.refresh_token_digest = ?
		  AND s.revoked_at IS NULL AND s.expires_at > ?
		  AND u.deleted_at IS NULL
		  AND NOT (u.ban_is_permanent OR COALESCE(u.banned_until > ?, false))
		GROUP BY s.id, u.id
	`, sessionID, userID, digest, now, now).Scan(&row)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return row.identity(), nil
}

// Create 创建会话行。digest 可先写随机占位摘要，签发 refresh 后再原子替换。
func (SessionRepository) Create(ctx context.Context, session *model.UserSession) error {
	return db.FromContext(ctx).Create(session).Error
}

// SetRefreshDigest 写入最终 refresh token 摘要。
func (SessionRepository) SetRefreshDigest(ctx context.Context, sessionID uint64, digest string) error {
	return db.FromContext(ctx).Model(&model.UserSession{}).
		Where("id = ?", sessionID).
		UpdateColumn("refresh_token_digest", digest).Error
}

// Touch 节流更新最后活跃时间；不满足阈值时不产生写入。
func (SessionRepository) Touch(
	ctx context.Context,
	sessionID uint64,
	now time.Time,
	threshold time.Duration,
) error {
	return db.FromContext(ctx).Model(&model.UserSession{}).
		Where("id = ? AND revoked_at IS NULL AND expires_at > ? AND last_seen_at <= ?",
			sessionID, now, now.Add(-threshold)).
		UpdateColumn("last_seen_at", now).Error
}

// Revoke 撤销当前用户的一条有效会话。
func (SessionRepository) Revoke(
	ctx context.Context,
	userID uint64,
	sessionID uint64,
	now time.Time,
) error {
	result := db.FromContext(ctx).Model(&model.UserSession{}).
		Where("id = ? AND user_id = ? AND revoked_at IS NULL AND expires_at > ?", sessionID, userID, now).
		UpdateColumn("revoked_at", gorm.Expr("GREATEST(?, last_seen_at)", now))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeAll 撤销用户的全部有效会话，已经撤销的审计行保持不变。
func (SessionRepository) RevokeAll(ctx context.Context, userID uint64, now time.Time) error {
	return db.FromContext(ctx).Model(&model.UserSession{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		UpdateColumn("revoked_at", gorm.Expr("GREATEST(?, last_seen_at)", now)).Error
}

// ListActive 返回尚未撤销且未过期的设备会话。
func (SessionRepository) ListActive(
	ctx context.Context,
	userID uint64,
	now time.Time,
) ([]model.UserSession, error) {
	var sessions []model.UserSession
	err := db.FromContext(ctx).
		Where("user_id = ? AND revoked_at IS NULL AND expires_at > ?", userID, now).
		Order("last_seen_at DESC, id DESC").
		Find(&sessions).Error
	return sessions, err
}

func (row identityRow) identity() *Identity {
	return &Identity{
		Session: model.UserSession{
			ID: row.SessionID, UserID: row.SessionUserID, LastSeenAt: row.SessionLastSeen,
			ExpiresAt: row.SessionExpiresAt, RevokedAt: row.SessionRevokedAt,
		},
		User: model.User{
			ID: row.UserID, Email: row.Email, PasswordHash: row.PasswordHash, Name: row.Name,
			Gender: row.Gender, Bio: row.Bio, AvatarImageAssetID: row.AvatarID,
			Roles:          roleValues(row.Roles),
			BanIsPermanent: row.BanIsPermanent, BannedUntil: row.BannedUntil,
			BanReason: row.BanReason, BannedBy: row.BannedBy, DeletedAt: row.DeletedAt,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		},
	}
}

func roleValues(values []string) []model.UserRole {
	roles := make([]model.UserRole, 0, len(values))
	for _, value := range values {
		roles = append(roles, model.UserRole(value))
	}
	return roles
}

const zeroDigest = "0000000000000000000000000000000000000000000000000000000000000000"
