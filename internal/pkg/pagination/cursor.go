package pagination

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
)

const (
	cursorVersion     = byte(1)
	cursorPayloadSize = 1 + 8 + 8
)

// ErrInvalidCursor 是无法解密、被篡改或载荷不合法的游标。
var ErrInvalidCursor = errors.New("游标无效")

// Cursor 是数据库复合排序键，只在服务端内部流转。
type Cursor struct {
	CreatedAt time.Time
	ID        uint64
}

// CursorRequest 是已校验 limit、尚待按端点作用域解码的请求。
type CursorRequest struct {
	Token string
	Limit int
}

// CursorParams 是仓储可直接使用的游标查询参数。
type CursorParams struct {
	After *Cursor
	Limit int
}

// CursorCodec 使用 AES-GCM 隐藏并认证复合排序键。
// scope 作为附加认证数据，使帖子和通知游标不能交叉使用。
type CursorCodec struct {
	aead  cipher.AEAD
	scope []byte
}

// NewCursorCodec 从应用密钥派生独立的游标密钥。
func NewCursorCodec(secret, scope string) *CursorCodec {
	key := sha256.Sum256([]byte("danshi-cursor-v1\x00" + secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		panic(fmt.Sprintf("初始化游标 AES: %v", err))
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(fmt.Sprintf("初始化游标 GCM: %v", err))
	}
	return &CursorCodec{aead: aead, scope: []byte(scope)}
}

// NewEphemeralCursorCodec 为未注入应用配置的直接服务构造生成进程内随机密钥。
// 正式路由始终使用 NewCursorCodec 与运行时密钥，以便同一部署的各副本互认游标。
func NewEphemeralCursorCodec(scope string) *CursorCodec {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		panic(fmt.Sprintf("生成临时游标密钥: %v", err))
	}
	return NewCursorCodec(string(secret), scope)
}

// ParseCursorRequest 严格校验游标分页 limit；令牌由对应服务的 codec 解码。
func ParseCursorRequest(rawCursor, rawLimit string) (CursorRequest, error) {
	limit, err := parseOne(rawLimit, "limit", DefaultLimit, 1, MaxLimit)
	if err != nil {
		return CursorRequest{}, err
	}
	return CursorRequest{Token: rawCursor, Limit: limit}, nil
}

// DecodeRequest 将客户端请求转换为仓储参数，任何游标问题均稳定映射为 422。
func (c *CursorCodec) DecodeRequest(request CursorRequest) (CursorParams, error) {
	after, err := c.Decode(request.Token)
	if err != nil {
		return CursorParams{}, apierr.InvalidField(
			"cursor", apierr.FieldInvalidFormat, "cursor 无效或已被篡改",
		)
	}
	return CursorParams{After: after, Limit: request.Limit}, nil
}

// Encode 加密一个完整的 (created_at, id) 排序键。
func (c *CursorCodec) Encode(value Cursor) (string, error) {
	if value.CreatedAt.IsZero() || value.ID == 0 {
		return "", fmt.Errorf("编码游标: %w", ErrInvalidCursor)
	}
	payload := make([]byte, cursorPayloadSize)
	payload[0] = cursorVersion
	binary.BigEndian.PutUint64(payload[1:9], uint64(value.CreatedAt.UTC().UnixMicro()))
	binary.BigEndian.PutUint64(payload[9:17], value.ID)
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("生成游标 nonce: %w", err)
	}
	sealed := c.aead.Seal(nonce, nonce, payload, c.scope)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Decode 解密并认证游标。空串表示从最新一条开始。
func (c *CursorCodec) Decode(raw string) (*Cursor, error) {
	if raw == "" {
		return nil, nil
	}
	sealed, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(sealed) != c.aead.NonceSize()+cursorPayloadSize+c.aead.Overhead() {
		return nil, ErrInvalidCursor
	}
	nonceSize := c.aead.NonceSize()
	payload, err := c.aead.Open(nil, sealed[:nonceSize], sealed[nonceSize:], c.scope)
	if err != nil || len(payload) != cursorPayloadSize || payload[0] != cursorVersion {
		return nil, ErrInvalidCursor
	}
	id := binary.BigEndian.Uint64(payload[9:17])
	if id == 0 {
		return nil, ErrInvalidCursor
	}
	micros := int64(binary.BigEndian.Uint64(payload[1:9]))
	return &Cursor{CreatedAt: time.UnixMicro(micros).UTC(), ID: id}, nil
}
