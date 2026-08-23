// Package localstorage 提供开发环境可替换的内存对象存储。
package localstorage

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/jingyijun/danshi_backend_go/internal/service"
)

// Memory 是不发网络请求的开发存储；签发后即模拟对象按声明大小存在。
type Memory struct {
	mu             sync.RWMutex
	objects        map[string]int64
	privateObjects map[string]bool
}

// NewMemory 创建空的开发存储。
func NewMemory() *Memory {
	return &Memory{
		objects: make(map[string]int64), privateObjects: make(map[string]bool),
	}
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
