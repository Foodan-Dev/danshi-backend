// Package localstorage 提供开发环境可替换的内存对象存储。
package localstorage

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

var (
	errObjectNotFound = errors.New("对象不存在")
	errAccessDenied   = errors.New("对象禁止匿名读取")
	errSignedExpired  = errors.New("签名读取地址已过期")
)

type signedRead struct {
	objectKey string
	expiresAt time.Time
}

// Memory 是不发网络请求的开发存储；签发后即模拟对象按声明大小存在。
type Memory struct {
	mu             sync.RWMutex
	objects        map[string]int64
	privateObjects map[string]bool
	signedReads    map[string]signedRead
	nextSignature  uint64
	now            func() time.Time
}

// NewMemory 创建空的开发存储。
func NewMemory() *Memory {
	return &Memory{
		objects: make(map[string]int64), privateObjects: make(map[string]bool),
		signedReads: make(map[string]signedRead), now: time.Now,
	}
}

// PresignGet 为开发对象生成只能在有效期内读取的 memory URL。
func (m *Memory) PresignGet(_ context.Context, objectKey string, ttl time.Duration) (string, error) {
	if strings.TrimSpace(objectKey) == "" || ttl <= 0 {
		return "", fmt.Errorf("签名读取参数无效")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextSignature++
	expiresAt := m.now().UTC().Add(ttl)
	query := url.Values{
		"expires":   {strconv.FormatInt(expiresAt.Unix(), 10)},
		"signature": {strconv.FormatUint(m.nextSignature, 10)},
	}
	signedURL := "memory://signed/" + url.PathEscape(objectKey) + "?" + query.Encode()
	m.signedReads[signedURL] = signedRead{objectKey: objectKey, expiresAt: expiresAt}
	return signedURL, nil
}

// PresignPut 记录声明大小并返回 memory scheme 的开发凭证。
func (m *Memory) PresignPut(
	_ context.Context,
	request service.StoragePresignRequest,
) (service.StorageUploadTicket, error) {
	m.mu.Lock()
	m.objects[request.ObjectKey] = request.ContentLength
	m.mu.Unlock()
	return service.StorageUploadTicket{
		UploadURL: "memory://upload/" + url.PathEscape(request.ObjectKey),
		ExpiresAt: time.Now().UTC().Add(request.TTL),
	}, nil
}

// HeadObject 返回内存对象元信息。
func (m *Memory) HeadObject(_ context.Context, objectKey string) (service.StorageObjectMeta, error) {
	m.mu.RLock()
	size, exists := m.objects[objectKey]
	m.mu.RUnlock()
	return service.StorageObjectMeta{Exists: exists, ContentLength: size}, nil
}

// DeleteObject 幂等删除内存对象。
func (m *Memory) DeleteObject(_ context.Context, objectKey string) error {
	m.mu.Lock()
	delete(m.objects, objectKey)
	delete(m.privateObjects, objectKey)
	m.mu.Unlock()
	return nil
}

// SetObjectPublicAccess 幂等切换开发对象的公开读状态。
func (m *Memory) SetObjectPublicAccess(_ context.Context, objectKey string, public bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.objects[objectKey]; !exists {
		return fmt.Errorf("对象不存在: %s", objectKey)
	}
	m.privateObjects[objectKey] = !public
	return nil
}

// PublicURL 返回不可出网的开发 URL。
func (*Memory) PublicURL(objectKey string) (string, error) {
	if objectKey == "" {
		return "", fmt.Errorf("对象键不能为空")
	}
	return "memory://public/" + url.PathEscape(objectKey), nil
}

// ReadURL 模拟客户端读取裸公开 URL 或服务端签发的短期 URL。
func (m *Memory) ReadURL(rawURL string) (service.StorageObjectMeta, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "memory" {
		return service.StorageObjectMeta{}, errObjectNotFound
	}
	var objectKey string
	switch parsed.Host {
	case "public":
		objectKey, err = url.PathUnescape(strings.TrimPrefix(parsed.EscapedPath(), "/"))
		if err != nil {
			return service.StorageObjectMeta{}, errObjectNotFound
		}
		if m.privateObjects[objectKey] {
			return service.StorageObjectMeta{}, errAccessDenied
		}
	case "signed":
		signed, exists := m.signedReads[rawURL]
		if !exists {
			return service.StorageObjectMeta{}, errAccessDenied
		}
		if !m.now().UTC().Before(signed.expiresAt) {
			return service.StorageObjectMeta{}, errSignedExpired
		}
		objectKey = signed.objectKey
	default:
		return service.StorageObjectMeta{}, errObjectNotFound
	}
	size, exists := m.objects[objectKey]
	if !exists {
		return service.StorageObjectMeta{}, errObjectNotFound
	}
	return service.StorageObjectMeta{Exists: true, ContentLength: size}, nil
}
