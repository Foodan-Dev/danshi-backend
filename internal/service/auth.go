// Package service 实现不依赖 HTTP 框架的领域业务逻辑。
package service

import (
	"context"
	"crypto/hmac"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/config"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/jwtx"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/passwordx"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/ptime"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

const (
	verificationCodeTTL      = 10 * time.Minute
	verificationSendCooldown = time.Minute
	verificationSendWindow   = time.Hour
	maxSendsPerWindow        = int32(5)
	maxFailedAttempts        = int32(5)
	sessionTouchThreshold    = 5 * time.Minute
)

var (
	errInvalidCredentials = errors.New("auth: invalid credentials")
	errInvalidSession     = errors.New("auth: invalid session")
)

// RegisterInput 是注册领域输入。注册不接受头像字段。
type RegisterInput struct {
	Email            string
	Password         string
	VerificationCode *string
	Name             *string
	Gender           *string
}

// LoginInput 是登录领域输入。
type LoginInput struct {
	Identifier string
	Password   string
}

// ClientInfo 描述一次登录所在设备。
type ClientInfo struct {
	DeviceLabel string
	UserAgent   string
	IP          string
}

// UserView 是只对用户本人返回的账号信息。
type UserView struct {
	ID        uint64           `json:"id"`
	Email     string           `json:"email"`
	Name      string           `json:"name"`
	Gender    *model.Gender    `json:"gender"`
	Bio       *string          `json:"bio"`
	Roles     []model.UserRole `json:"roles"`
	AvatarURL *string          `json:"avatar_url"`
}

// AuthResult 是注册和登录的 token 对与当前用户。
type AuthResult struct {
	Token        string   `json:"token"`
	RefreshToken string   `json:"refresh_token"`
	User         UserView `json:"user"`
}

// TokenResult 是 refresh 返回的 token 对。refresh token 不轮转，原样返回。
type TokenResult struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token"`
}

// Principal 是鉴权通过后在请求内传递的最小身份。
type Principal struct {
	User      model.User
	SessionID uint64
}

// SessionView 是“我的登录设备”列表项。
type SessionView struct {
	ID          uint64     `json:"id"`
	DeviceLabel *string    `json:"device_label"`
	UserAgent   *string    `json:"user_agent"`
	IP          *string    `json:"ip"`
	CreatedAt   ptime.Time `json:"created_at"`
	LastSeenAt  ptime.Time `json:"last_seen_at"`
	ExpiresAt   ptime.Time `json:"expires_at"`
	IsCurrent   bool       `json:"is_current"`
}

// RateLimitError 携带 HTTP Retry-After 所需的秒数。
type RateLimitError struct {
	RetryAfterSeconds int
	err               error
}

func (e *RateLimitError) Error() string { return e.err.Error() }
func (e *RateLimitError) Unwrap() error { return e.err }

type persistedError struct{ err error }

func (e *persistedError) Error() string { return e.err.Error() }
func (e *persistedError) Unwrap() error { return e.err }

// ShouldCommitError 报告该错误是否已经写入必须保留的安全状态。
func ShouldCommitError(err error) bool {
	var target *persistedError
	return errors.As(err, &target)
}

// AuthService 实现 auth 域完整业务闭环。
type AuthService struct {
	cfg      config.Config
	tokens   *jwtx.Codec
	sender   VerificationEmailSender
	users    repository.UserRepository
	codes    repository.VerificationCodeRepository
	sessions repository.SessionRepository
}

// NewAuthService 创建 auth 服务。
func NewAuthService(cfg config.Config, sender VerificationEmailSender) *AuthService {
	if sender == nil {
		sender = UnavailableVerificationEmailSender{}
	}
	return &AuthService{cfg: cfg, tokens: jwtx.NewCodec(cfg.JWTSecretKey), sender: sender}
}

// SendVerificationCode 为允许注册的域名生成并记录一次验证码发送状态。
func (s *AuthService) SendVerificationCode(ctx context.Context, rawEmail string) error {
	email, err := normalizeEmail(rawEmail)
	if err != nil {
		return err
	}
	if !s.cfg.EmailVerificationRequired {
		return nil
	}
	if !s.domainAllowed(email) {
		return nil
	}
	registered := false
	if _, err = s.users.FindByEmail(ctx, email, repository.QueryOptions{IncludeDeleted: true}); err == nil {
		registered = true
	} else if !errors.Is(err, repository.ErrNotFound) {
		return apierr.Internal(err)
	}

	now := time.Now().UTC()
	challenge, err := s.codes.LockOrCreate(ctx, email, model.VerificationPurposeRegistration, now)
	if err != nil {
		return apierr.Internal(err)
	}
	if err := enforceSendRate(challenge, now); err != nil {
		return err
	}
	var code string
	if registered {
		challenge.CodeDigest, err = randomDigest()
	} else {
		code, err = generateVerificationCode()
		if err == nil {
			challenge.CodeDigest = s.codeDigest(email, code)
		}
	}
	if err != nil {
		return apierr.Internal(err)
	}

	challenge.ExpiresAt = now.Add(verificationCodeTTL)
	challenge.LastSentAt = &now
	challenge.SendCount++
	challenge.FailedAttempts = 0
	challenge.ConsumedAt = nil
	if err := s.codes.SaveState(ctx, challenge, now); err != nil {
		return apierr.Internal(err)
	}
	if registered {
		return nil
	}
	if err := s.sender.SendRegistrationCode(ctx, email, code); err != nil {
		return apierr.ServiceUnavailable("验证码暂时无法发送，请稍后再试").WithCause(err)
	}
	return nil
}

// Register 校验一次性验证码，并在同一个 UoW 中创建用户与首个设备会话。
func (s *AuthService) Register(
	ctx context.Context,
	input RegisterInput,
	client ClientInfo,
) (*AuthResult, error) {
	input, err := normalizeRegister(input)
	if err != nil {
		return nil, err
	}
	if !s.domainAllowed(input.Email) {
		return nil, apierr.InvalidField("email", apierr.FieldInvalidDomain, "该邮箱域名不允许注册")
	}
	if s.cfg.EmailVerificationRequired {
		if err := validateVerificationCode(input.VerificationCode); err != nil {
			return nil, err
		}
	}
	if _, err := s.users.FindByEmail(
		ctx, input.Email, repository.QueryOptions{IncludeDeleted: true},
	); err == nil {
		return nil, apierr.Conflict(apierr.BizEmailTaken, "邮箱已被注册")
	} else if !errors.Is(err, repository.ErrNotFound) {
		return nil, apierr.Internal(err)
	}

	passwordHash, err := passwordx.Hash(input.Password)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if s.cfg.EmailVerificationRequired {
		if err := s.consumeVerification(ctx, input.Email, *input.VerificationCode); err != nil {
			return nil, err
		}
	}

	user := &model.User{
		Email: input.Email, PasswordHash: passwordHash, Name: valueOrEmpty(input.Name),
		Gender: genderValue(input.Gender),
	}
	if err := s.users.Create(ctx, user); err != nil {
		if repository.IsUniqueViolation(err, "uq_users_email_lower") {
			return nil, apierr.Conflict(apierr.BizEmailTaken, "邮箱已被注册")
		}
		if repository.IsUniqueViolation(err, "uq_users_name_lower") ||
			repository.IsUniqueViolation(err, "uq_user_name_claims_name_lower") {
			return nil, apierr.Conflict(apierr.BizNameTaken, "name 已被占用")
		}
		return nil, apierr.Internal(err)
	}
	if err := s.users.ClaimName(ctx, user.ID, user.Name, time.Now().UTC()); err != nil {
		if repository.IsUniqueViolation(err, "uq_user_name_claims_name_lower") ||
			repository.IsUniqueViolation(err, "uq_users_name_lower") ||
			errors.Is(err, repository.ErrAlreadyExists) {
			return nil, apierr.Conflict(apierr.BizNameTaken, "name 已被占用")
		}
		return nil, apierr.Internal(err)
	}
	return s.issueSession(ctx, user, client, time.Now().UTC())
}

// Login 校验邮箱或 name 与密码；邮箱登录还必须满足邮箱域名白名单。
func (s *AuthService) Login(
	ctx context.Context,
	input LoginInput,
	client ClientInfo,
) (*AuthResult, error) {
	if err := validatePassword(input.Password, false); err != nil {
		return nil, err
	}
	user, err := s.findLoginUser(ctx, input.Identifier)
	if err != nil {
		var inputError *apierr.Error
		if errors.As(err, &inputError) {
			return nil, err
		}
		if !errors.Is(err, repository.ErrNotFound) {
			return nil, apierr.Internal(err)
		}
		return nil, apierr.Unauthorized().WithCause(errInvalidCredentials)
	}
	if !passwordx.Verify(input.Password, passwordHash(user)) {
		return nil, apierr.Unauthorized().WithCause(errInvalidCredentials)
	}
	if banned(user, time.Now().UTC()) {
		return nil, bannedError(user)
	}
	return s.issueSession(ctx, user, client, time.Now().UTC())
}

func (s *AuthService) findLoginUser(ctx context.Context, rawIdentifier string) (*model.User, error) {
	identifier := strings.TrimSpace(rawIdentifier)
	if identifier == "" {
		return nil, apierr.InvalidField("identifier", apierr.FieldRequired, "登录标识不能为空")
	}
	if strings.Contains(identifier, "@") {
		email, err := normalizeEmail(identifier)
		if err != nil {
			return nil, apierr.InvalidField("identifier", apierr.FieldInvalidFormat, "登录标识格式不正确")
		}
		if !s.domainAllowed(email) {
			return nil, apierr.Unauthorized().WithCause(errInvalidCredentials)
		}
		return s.users.FindByEmail(ctx, email)
	}
	name, err := normalizeName(identifier)
	if err != nil {
		return nil, apierr.InvalidField("identifier", apierr.FieldInvalidFormat, "登录标识格式不正确")
	}
	return s.users.FindByName(ctx, name)
}

// Refresh 校验 refresh token、摘要和会话状态，只换发 access token。
func (s *AuthService) Refresh(ctx context.Context, refreshToken string) (*TokenResult, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, apierr.Unauthorized().WithCause(jwtx.ErrInvalid)
	}
	claims, err := s.tokens.Parse(refreshToken, jwtx.TypeRefresh)
	if err != nil {
		return nil, apierr.Unauthorized().WithCause(err)
	}
	userID, sessionID, err := claimIDs(claims)
	if err != nil {
		return nil, apierr.Unauthorized().WithCause(err)
	}
	now := time.Now().UTC()
	identity, err := s.sessions.FindRefreshIdentity(
		ctx, sessionID, userID, jwtx.Digest(refreshToken), now,
	)
	if err != nil {
		return nil, sessionLookupError(err)
	}
	access, err := s.tokens.SignAt(
		int64(identity.User.ID), int64(identity.Session.ID), jwtx.TypeAccess, now, s.cfg.AccessTokenTTL(),
	)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if err := s.sessions.Touch(ctx, sessionID, now, sessionTouchThreshold); err != nil {
		return nil, apierr.Internal(err)
	}
	return &TokenResult{Token: access, RefreshToken: refreshToken}, nil
}

// Authenticate 验证 access token 及其绑定会话，并节流更新最后活跃时间。
func (s *AuthService) Authenticate(ctx context.Context, accessToken string) (*Principal, error) {
	claims, err := s.tokens.Parse(accessToken, jwtx.TypeAccess)
	if err != nil {
		return nil, apierr.Unauthorized().WithCause(err)
	}
	userID, sessionID, err := claimIDs(claims)
	if err != nil {
		return nil, apierr.Unauthorized().WithCause(err)
	}
	now := time.Now().UTC()
	identity, err := s.sessions.FindActiveIdentity(ctx, sessionID, userID, now)
	if err != nil {
		return nil, sessionLookupError(err)
	}
	if err := s.sessions.Touch(ctx, sessionID, now, sessionTouchThreshold); err != nil {
		return nil, apierr.Internal(err)
	}
	return &Principal{User: identity.User, SessionID: identity.Session.ID}, nil
}

// CurrentUser 返回当前用户自己的账号信息。
func (s *AuthService) CurrentUser(ctx context.Context, principal *Principal) (UserView, error) {
	return s.userView(ctx, &principal.User)
}

// Logout 撤销当前设备会话。
func (s *AuthService) Logout(ctx context.Context, principal *Principal) error {
	err := s.sessions.Revoke(ctx, principal.User.ID, principal.SessionID, time.Now().UTC())
	if errors.Is(err, repository.ErrNotFound) {
		return apierr.Unauthorized().WithCause(errInvalidSession)
	}
	if err != nil {
		return apierr.Internal(err)
	}
	return nil
}

// LogoutAll 撤销当前用户全部设备会话。
func (s *AuthService) LogoutAll(ctx context.Context, principal *Principal) error {
	if err := s.sessions.RevokeAll(ctx, principal.User.ID, time.Now().UTC()); err != nil {
		return apierr.Internal(err)
	}
	return nil
}

// Sessions 列出当前有效会话。
func (s *AuthService) Sessions(ctx context.Context, principal *Principal) ([]SessionView, error) {
	rows, err := s.sessions.ListActive(ctx, principal.User.ID, time.Now().UTC())
	if err != nil {
		return nil, apierr.Internal(err)
	}
	views := make([]SessionView, 0, len(rows))
	for _, row := range rows {
		views = append(views, SessionView{
			ID: row.ID, DeviceLabel: row.DeviceLabel, UserAgent: row.UserAgent, IP: row.IP,
			CreatedAt: ptime.Time(row.CreatedAt), LastSeenAt: ptime.Time(row.LastSeenAt),
			ExpiresAt: ptime.Time(row.ExpiresAt), IsCurrent: row.ID == principal.SessionID,
		})
	}
	return views, nil
}

// KickSession 撤销属于当前用户的指定设备。
func (s *AuthService) KickSession(ctx context.Context, principal *Principal, sessionID uint64) error {
	err := s.sessions.Revoke(ctx, principal.User.ID, sessionID, time.Now().UTC())
	if errors.Is(err, repository.ErrNotFound) {
		return apierr.NotFound(apierr.BizSessionNotFound, "会话")
	}
	if err != nil {
		return apierr.Internal(err)
	}
	return nil
}

func (s *AuthService) issueSession(
	ctx context.Context,
	user *model.User,
	client ClientInfo,
	now time.Time,
) (*AuthResult, error) {
	placeholder, err := randomDigest()
	if err != nil {
		return nil, apierr.Internal(err)
	}
	session := newSession(user.ID, client, placeholder, now, s.cfg.RefreshTokenTTL())
	if err := s.sessions.Create(ctx, session); err != nil {
		return nil, apierr.Internal(err)
	}
	if user.ID > math.MaxInt64 || session.ID > math.MaxInt64 {
		return nil, apierr.Internal(errors.New("用户或会话 ID 超出 JWT 可表示范围"))
	}
	refresh, err := s.tokens.SignAt(
		int64(user.ID), int64(session.ID), jwtx.TypeRefresh, now, s.cfg.RefreshTokenTTL(),
	)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	access, err := s.tokens.SignAt(
		int64(user.ID), int64(session.ID), jwtx.TypeAccess, now, s.cfg.AccessTokenTTL(),
	)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if err := s.sessions.SetRefreshDigest(ctx, session.ID, jwtx.Digest(refresh)); err != nil {
		return nil, apierr.Internal(err)
	}
	userView, err := s.userView(ctx, user)
	if err != nil {
		return nil, err
	}
	return &AuthResult{Token: access, RefreshToken: refresh, User: userView}, nil
}

func (s *AuthService) userView(ctx context.Context, user *model.User) (UserView, error) {
	avatarURL, err := s.users.FindAvatarURL(ctx, user.ID)
	if err != nil {
		return UserView{}, apierr.Internal(err)
	}
	roles := user.Roles
	if roles == nil {
		roles, err = s.users.FindRoles(ctx, user.ID)
		if err != nil {
			return UserView{}, apierr.Internal(err)
		}
	}
	return UserView{
		ID: user.ID, Email: user.Email, Name: user.Name, Gender: user.Gender,
		Bio: user.Bio, Roles: append([]model.UserRole{}, roles...), AvatarURL: avatarURL,
	}, nil
}

func (s *AuthService) consumeVerification(ctx context.Context, email, code string) error {
	now := time.Now().UTC()
	challenge, err := s.codes.LockExisting(ctx, email, model.VerificationPurposeRegistration)
	if errors.Is(err, repository.ErrNotFound) {
		return apierr.BadRequest(apierr.BizVerifyCodeInvalid, "验证码无效或已过期")
	}
	if err != nil {
		return apierr.Internal(err)
	}
	if challenge.ConsumedAt != nil || !challenge.ExpiresAt.After(now) ||
		challenge.FailedAttempts >= maxFailedAttempts {
		return apierr.BadRequest(apierr.BizVerifyCodeInvalid, "验证码无效或已过期")
	}
	if !hmac.Equal([]byte(challenge.CodeDigest), []byte(s.codeDigest(email, code))) {
		challenge.FailedAttempts++
		if err := s.codes.SaveState(ctx, challenge, now); err != nil {
			return apierr.Internal(err)
		}
		return &persistedError{err: apierr.BadRequest(
			apierr.BizVerifyCodeInvalid, "验证码无效或已过期",
		)}
	}
	challenge.ConsumedAt = &now
	if err := s.codes.SaveState(ctx, challenge, now); err != nil {
		return apierr.Internal(err)
	}
	return nil
}

func (s *AuthService) domainAllowed(email string) bool {
	domain := emailDomain(email)
	for _, allowed := range s.cfg.EmailDomains() {
		if domain == allowed {
			return true
		}
	}
	return false
}

func (s *AuthService) codeDigest(email, code string) string {
	payload := fmt.Sprintf("%s:%s:%s", model.VerificationPurposeRegistration, email, code)
	digest := hmac.New(sha256.New, []byte(s.cfg.EmailVerificationSecret))
	_, _ = digest.Write([]byte(payload))
	return fmt.Sprintf("%x", digest.Sum(nil))
}

func enforceSendRate(challenge *model.EmailVerificationCode, now time.Time) error {
	if challenge.LastSentAt != nil {
		remaining := verificationSendCooldown - now.Sub(*challenge.LastSentAt)
		if remaining > 0 {
			return rateLimitError(remaining)
		}
	}
	if now.Sub(challenge.SendWindowStartedAt) >= verificationSendWindow {
		challenge.SendWindowStartedAt = now
		challenge.SendCount = 0
	}
	if challenge.SendCount >= maxSendsPerWindow {
		return rateLimitError(verificationSendWindow - now.Sub(challenge.SendWindowStartedAt))
	}
	return nil
}

func rateLimitError(remaining time.Duration) error {
	retry := max(1, int(math.Ceil(remaining.Seconds())))
	return &RateLimitError{
		RetryAfterSeconds: retry,
		err: apierr.TooManyRequests(
			apierr.BizVerifyCodeTooMany, "验证码发送过于频繁，请稍后再试",
		),
	}
}

func generateVerificationCode() (string, error) {
	n, err := cryptorand.Int(cryptorand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func randomDigest() (string, error) {
	random := make([]byte, 32)
	if _, err := cryptorand.Read(random); err != nil {
		return "", err
	}
	return jwtx.Digest(string(random)), nil
}

func newSession(
	userID uint64,
	client ClientInfo,
	digest string,
	now time.Time,
	ttl time.Duration,
) *model.UserSession {
	userAgent := optionalString(client.UserAgent, 512)
	deviceLabel := optionalString(client.DeviceLabel, 100)
	if deviceLabel == nil && userAgent != nil {
		deviceLabel = optionalString(*userAgent, 100)
	}
	if deviceLabel == nil {
		unknown := "未知设备"
		deviceLabel = &unknown
	}
	return &model.UserSession{
		UserID: userID, RefreshTokenDigest: digest, DeviceLabel: deviceLabel,
		UserAgent: userAgent, IP: optionalString(client.IP, 45), CreatedAt: now,
		LastSeenAt: now, ExpiresAt: now.Add(ttl),
	}
}

func claimIDs(claims *jwtx.Claims) (uint64, uint64, error) {
	userID, err := claims.UserID()
	if err != nil || userID <= 0 || claims.SessionID <= 0 {
		return 0, 0, jwtx.ErrInvalid
	}
	return uint64(userID), uint64(claims.SessionID), nil
}

func sessionLookupError(err error) error {
	if errors.Is(err, repository.ErrNotFound) {
		return apierr.Unauthorized().WithCause(errInvalidSession)
	}
	return apierr.Internal(err)
}

func banned(user *model.User, now time.Time) bool {
	return user.BanIsPermanent || (user.BannedUntil != nil && user.BannedUntil.After(now))
}

func bannedError(user *model.User) error {
	reason := "账号违反平台规则"
	if user.BanReason != nil && strings.TrimSpace(*user.BanReason) != "" {
		reason = *user.BanReason
	}
	if user.BanIsPermanent {
		return apierr.Forbidden(apierr.BizAccountBanned, "账号已被永久封禁："+reason)
	}
	return apierr.Forbidden(
		apierr.BizAccountBanned,
		fmt.Sprintf("账号已被封禁至 %s：%s", ptime.Format(*user.BannedUntil), reason),
	)
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func genderValue(value *string) *model.Gender {
	if value == nil {
		return nil
	}
	gender := model.Gender(*value)
	return &gender
}

func passwordHash(user *model.User) string {
	if user == nil {
		return ""
	}
	return user.PasswordHash
}
