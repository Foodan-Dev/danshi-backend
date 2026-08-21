// Package jwtx 签发与校验令牌。
//
// 与 Python 侧的关键差异（docs/go-rewrite-plan.md §5.2.6）：
// **access 与 refresh 都绑定会话，不再是无状态的**。两者都带 sid，
// 每次鉴权按 sid 查 user_sessions，命中且未撤销未过期才放行。
// 因此所有撤销（本设备登出、登出所有设备、踢设备）都立即生效。
//
// 存量 Python 令牌一律失效：它们没有 sid，且 sub 是 uuid 而不是整数。
// 这是有意为之，切换时全体用户重新登录。
package jwtx

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TokenType 区分访问令牌与刷新令牌，防止二者混用。
type TokenType string

// 令牌类型枚举值。
const (
	TypeAccess  TokenType = "access"
	TypeRefresh TokenType = "refresh"
)

// 令牌解析的稳定错误值。
var (
	ErrInvalid = errors.New("令牌无效")
	ErrExpired = errors.New("令牌已过期")
)

// Claims 是令牌载荷。刻意保持最小：任何放进来的字段都会随令牌四处流转，
// 且在令牌过期前无法更新，所以除了定位会话必需的信息外不放业务数据。
type Claims struct {
	jwt.RegisteredClaims
	Type      TokenType `json:"type"`
	SessionID int64     `json:"sid"`
}

// UserID 把 JWT subject 解析为用户整数主键。
func (c Claims) UserID() (int64, error) {
	return strconv.ParseInt(c.Subject, 10, 64)
}

// Codec 使用项目密钥签发并验证会话绑定的 JWT。
type Codec struct {
	secret []byte
}

// NewCodec 创建 JWT 编解码器。
func NewCodec(secret string) *Codec { return &Codec{secret: []byte(secret)} }

// Sign 签发带用户、会话和令牌类型的 JWT。
func (c *Codec) Sign(userID, sessionID int64, typ TokenType, ttl time.Duration) (string, error) {
	return c.SignAt(userID, sessionID, typ, time.Now().UTC(), ttl)
}

// SignAt 在指定时刻签发令牌，供需要让会话绝对过期时间与 JWT exp 精确一致的业务使用。
func (c *Codec) SignAt(
	userID, sessionID int64,
	typ TokenType,
	now time.Time,
	ttl time.Duration,
) (string, error) {
	now = now.UTC()
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(userID, 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
		Type:      typ,
		SessionID: sessionID,
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(c.secret)
}

// Parse 校验签名与过期，并断言令牌类型。
// 断言类型是必须的——否则 refresh token 可以直接当 access token 用。
func (c *Codec) Parse(token string, want TokenType) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("签名算法不匹配: %v", t.Header["alg"])
		}
		return c.secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpired
		}
		return nil, ErrInvalid
	}
	if claims.Type != want {
		return nil, ErrInvalid
	}
	if claims.SessionID <= 0 {
		// 没有 sid 说明是存量 Python 令牌，直接判无效
		return nil, ErrInvalid
	}
	if _, err := claims.UserID(); err != nil {
		return nil, ErrInvalid
	}
	return claims, nil
}
