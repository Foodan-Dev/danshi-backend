package model

import "time"

// User 是账号主体；注销与封禁都保留数据行。
type User struct {
	ID                 uint64 `gorm:"primaryKey"`
	Email              string
	PasswordHash       string
	Name               string
	Gender             *Gender
	Bio                *string
	AvatarImageAssetID *uint64
	Role               UserRole
	BanIsPermanent     bool
	BannedUntil        *time.Time
	BanReason          *string
	BannedBy           *uint64
	DeletedAt          *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// TableName 返回用户表名。
func (User) TableName() string { return "users" }

// Follow 是用户之间的单向关注关系。
type Follow struct {
	FollowerID  uint64 `gorm:"primaryKey;autoIncrement:false"`
	FollowingID uint64 `gorm:"primaryKey;autoIncrement:false"`
	CreatedAt   time.Time
}

// TableName 返回关注关系表名。
func (Follow) TableName() string { return "follows" }

// EmailVerificationCode 保存邮箱验证码摘要及频率控制状态。
type EmailVerificationCode struct {
	ID                  uint64 `gorm:"primaryKey"`
	Email               string
	Purpose             VerificationPurpose
	CodeDigest          string
	ExpiresAt           time.Time
	LastSentAt          *time.Time
	SendWindowStartedAt time.Time
	SendCount           int32
	FailedAttempts      int32
	ConsumedAt          *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// TableName 返回邮箱验证码表名。
func (EmailVerificationCode) TableName() string { return "email_verification_codes" }

// UserSession 是一台设备的一次可撤销登录会话。
type UserSession struct {
	ID                 uint64 `gorm:"primaryKey"`
	UserID             uint64
	RefreshTokenDigest string
	DeviceLabel        *string
	UserAgent          *string
	IP                 *string
	CreatedAt          time.Time
	LastSeenAt         time.Time
	ExpiresAt          time.Time
	RevokedAt          *time.Time
}

// TableName 返回用户会话表名。
func (UserSession) TableName() string { return "user_sessions" }
