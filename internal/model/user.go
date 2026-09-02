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
	Roles              []UserRole `gorm:"-"`
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

// UserNameClaim 是一个不可篡改的 name 占用记录。users.name 保存当前公开身份，
// 本表保留当前与历史 name，避免改名或注销后被其他账号冒用。
type UserNameClaim struct {
	ID        uint64 `gorm:"primaryKey"`
	UserID    uint64
	Name      string
	CreatedAt time.Time
}

// TableName 返回用户 name 占用记录表名。
func (UserNameClaim) TableName() string { return "user_name_claims" }

// UserRoleBinding 是用户当前生效的一项管理角色绑定。
type UserRoleBinding struct {
	UserID    uint64   `gorm:"primaryKey;autoIncrement:false"`
	Role      UserRole `gorm:"primaryKey"`
	GrantedBy *uint64
	GrantedAt time.Time
}

// TableName 返回用户角色绑定表名。
func (UserRoleBinding) TableName() string { return "user_roles" }

// UserBanRecord 是一次追加不可篡改的封禁或解封事实。
type UserBanRecord struct {
	ID             uint64 `gorm:"primaryKey"`
	UserID         uint64
	Action         UserBanAction
	BanIsPermanent bool
	BannedUntil    *time.Time
	Reason         *string
	ActorID        *uint64
	CreatedAt      time.Time
}

// TableName 返回用户封禁记录表名。
func (UserBanRecord) TableName() string { return "user_ban_records" }

// UserRoleRecord 是一次追加不可篡改的角色授予或撤销事实。
type UserRoleRecord struct {
	ID        uint64 `gorm:"primaryKey"`
	UserID    uint64
	Role      UserRole
	Action    UserRoleAction
	ActorID   *uint64
	CreatedAt time.Time
}

// TableName 返回用户角色记录表名。
func (UserRoleRecord) TableName() string { return "user_role_records" }

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
