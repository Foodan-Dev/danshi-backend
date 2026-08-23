package testutil

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

// ErrMockContentMD5Mismatch 表示测试对象内容与 presign 声明的 MD5 不一致。
// 现有 ImageStorage.HeadObject 端口不返回 MD5，所以该错误会沿存储故障路径返回。
var ErrMockContentMD5Mismatch = errors.New("mock object Content-MD5 mismatch")

// ErrMockPublicAccessDenied 表示对象存在但对象级 ACL 禁止公开读取。
var ErrMockPublicAccessDenied = errors.New("mock object public access denied")

// ErrMockObjectNotFound 表示 Mock 存储中没有目标对象或公开 URL。
var ErrMockObjectNotFound = errors.New("mock object not found")

// StoredObject 是 Mock 存储中一个可由测试控制的对象。
type StoredObject struct {
	ContentLength int64
	ContentMD5    string
}

// StoragePresignBehavior 覆写下一次 presign 的返回、错误或阻塞。
type StoragePresignBehavior struct {
	Ticket  *service.StorageUploadTicket
	Err     error
	Delay   time.Duration
	Release <-chan struct{}
}

// StorageHeadBehavior 覆写下一次 HEAD；Meta 为 nil 时读取 Mock 对象表。
type StorageHeadBehavior struct {
	Meta    *service.StorageObjectMeta
	Err     error
	Delay   time.Duration
	Release <-chan struct{}
}

// StorageDeleteBehavior 覆写下一次删除的错误或阻塞。
type StorageDeleteBehavior struct {
	Err     error
	Delay   time.Duration
	Release <-chan struct{}
}

// StorageAccessBehavior 覆写下一次对象公开读切换的错误或阻塞。
type StorageAccessBehavior struct {
	Err     error
	Delay   time.Duration
	Release <-chan struct{}
}

// StorageAccessCall 是一次对象公开读切换调用。
type StorageAccessCall struct {
	ObjectKey string
	Public    bool
}

// MockImageStorage 是并发安全、可编程的 service.ImageStorage 实现。
type MockImageStorage struct {
	mu sync.Mutex

	autoMaterialize bool
	uploadURLBase   string
	publicURLBase   string
	now             func() time.Time

	objects        map[string]StoredObject
	privateObjects map[string]bool
	expectedMD5    map[string]string
	publicURLs     map[string]string
	publicURLErrs  map[string]error

	presignBehaviors []StoragePresignBehavior
	headBehaviors    []StorageHeadBehavior
	deleteBehaviors  []StorageDeleteBehavior
	accessBehaviors  []StorageAccessBehavior

	presignCalls   []service.StoragePresignRequest
	headCalls      []string
	deleteCalls    []string
	accessCalls    []StorageAccessCall
	publicURLCalls []string
	signal         callSignal
}

// NewMockImageStorage 创建默认不自动生成对象的存储 Mock。
// 调用 MaterializeLastPresign 或 PutObject 后，complete 才能看到对象存在。
func NewMockImageStorage() *MockImageStorage {
	return &MockImageStorage{
		uploadURLBase:  "https://upload.example.test/",
		publicURLBase:  "https://image.example.test/",
		now:            func() time.Time { return time.Now().UTC() },
		objects:        make(map[string]StoredObject),
		privateObjects: make(map[string]bool),
		expectedMD5:    make(map[string]string),
		publicURLs:     make(map[string]string),
		publicURLErrs:  make(map[string]error),
		signal:         newCallSignal(),
	}
}

// SetAutoMaterialize 控制 presign 是否立即模拟一个完全匹配的已上传对象。
func (s *MockImageStorage) SetAutoMaterialize(enabled bool) {
	s.mu.Lock()
	s.autoMaterialize = enabled
	s.mu.Unlock()
}

// SetUploadURLBase 设置默认 upload URL 前缀。
func (s *MockImageStorage) SetUploadURLBase(base string) {
	s.mu.Lock()
	s.uploadURLBase = base
	s.mu.Unlock()
}

// SetPublicURLBase 设置默认 public URL 前缀。
func (s *MockImageStorage) SetPublicURLBase(base string) {
	s.mu.Lock()
	s.publicURLBase = base
	s.mu.Unlock()
}

// SetPublicURL 为指定 object key 设置精确 public URL。
func (s *MockImageStorage) SetPublicURL(objectKey string, publicURL string) {
	s.mu.Lock()
	s.publicURLs[objectKey] = publicURL
	delete(s.publicURLErrs, objectKey)
	s.mu.Unlock()
}

// SetPublicURLError 为指定 object key 设置 URL 构造失败。
func (s *MockImageStorage) SetPublicURLError(objectKey string, err error) {
	s.mu.Lock()
	s.publicURLErrs[objectKey] = err
	s.mu.Unlock()
}

// QueuePresign 按调用顺序编排 presign 行为。
func (s *MockImageStorage) QueuePresign(behaviors ...StoragePresignBehavior) {
	s.mu.Lock()
	s.presignBehaviors = append(s.presignBehaviors, behaviors...)
	s.mu.Unlock()
}

// QueueHead 按调用顺序编排 HEAD 行为。
func (s *MockImageStorage) QueueHead(behaviors ...StorageHeadBehavior) {
	s.mu.Lock()
	s.headBehaviors = append(s.headBehaviors, behaviors...)
	s.mu.Unlock()
}

// QueueDelete 按调用顺序编排删除行为，适合精确构造清理/complete 竞态。
func (s *MockImageStorage) QueueDelete(behaviors ...StorageDeleteBehavior) {
	s.mu.Lock()
	s.deleteBehaviors = append(s.deleteBehaviors, behaviors...)
	s.mu.Unlock()
}

// QueueAccess 按调用顺序编排对象公开读切换行为。
func (s *MockImageStorage) QueueAccess(behaviors ...StorageAccessBehavior) {
	s.mu.Lock()
	s.accessBehaviors = append(s.accessBehaviors, behaviors...)
	s.mu.Unlock()
}

// PutObject 写入或覆盖一个测试对象。
func (s *MockImageStorage) PutObject(objectKey string, object StoredObject) {
	s.mu.Lock()
	s.objects[objectKey] = object
	delete(s.privateObjects, objectKey)
	s.mu.Unlock()
}

// RemoveObject 让后续 HEAD 返回对象不存在。
func (s *MockImageStorage) RemoveObject(objectKey string) {
	s.mu.Lock()
	delete(s.objects, objectKey)
	delete(s.privateObjects, objectKey)
	s.mu.Unlock()
}

// MaterializeLastPresign 按最近一次声明的大小和 MD5 模拟上传完成。
func (s *MockImageStorage) MaterializeLastPresign() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.presignCalls) == 0 {
		return "", errors.New("还没有 presign 请求")
	}
	request := s.presignCalls[len(s.presignCalls)-1]
	s.objects[request.ObjectKey] = StoredObject{
		ContentLength: request.ContentLength,
		ContentMD5:    request.ContentMD5,
	}
	delete(s.privateObjects, request.ObjectKey)
	return request.ObjectKey, nil
}

// PresignPut 记录全部签名字段并返回可控 ticket。
func (s *MockImageStorage) PresignPut(
	ctx context.Context,
	request service.StoragePresignRequest,
) (service.StorageUploadTicket, error) {
	s.mu.Lock()
	s.presignCalls = append(s.presignCalls, request)
	s.expectedMD5[request.ObjectKey] = request.ContentMD5
	autoMaterialize := s.autoMaterialize
	if autoMaterialize {
		s.objects[request.ObjectKey] = StoredObject{
			ContentLength: request.ContentLength, ContentMD5: request.ContentMD5,
		}
		delete(s.privateObjects, request.ObjectKey)
	}
	behavior, hasBehavior := popFirst(&s.presignBehaviors)
	uploadURLBase := s.uploadURLBase
	now := s.now
	s.signal.notify()
	s.mu.Unlock()

	if hasBehavior {
		if err := runStorageTiming(ctx, behavior.Delay, behavior.Release); err != nil {
			return service.StorageUploadTicket{}, err
		}
		if behavior.Err != nil {
			return service.StorageUploadTicket{}, behavior.Err
		}
		if behavior.Ticket != nil {
			return *behavior.Ticket, nil
		}
	}
	return service.StorageUploadTicket{
		UploadURL: joinObjectURL(uploadURLBase, request.ObjectKey),
		ExpiresAt: now().UTC().Add(request.TTL),
	}, nil
}

// HeadObject 返回对象存在性/大小，并在已知实际 MD5 不一致时显式失败。
func (s *MockImageStorage) HeadObject(
	ctx context.Context,
	objectKey string,
) (service.StorageObjectMeta, error) {
	s.mu.Lock()
	s.headCalls = append(s.headCalls, objectKey)
	behavior, hasBehavior := popFirst(&s.headBehaviors)
	object, exists := s.objects[objectKey]
	expectedMD5 := s.expectedMD5[objectKey]
	s.signal.notify()
	s.mu.Unlock()

	if hasBehavior {
		if err := runStorageTiming(ctx, behavior.Delay, behavior.Release); err != nil {
			return service.StorageObjectMeta{}, err
		}
		if behavior.Err != nil {
			return service.StorageObjectMeta{}, behavior.Err
		}
		if behavior.Meta != nil {
			return *behavior.Meta, nil
		}
	}
	if exists && object.ContentMD5 != "" && expectedMD5 != "" && object.ContentMD5 != expectedMD5 {
		return service.StorageObjectMeta{}, ErrMockContentMD5Mismatch
	}
	return service.StorageObjectMeta{
		Exists: exists, ContentLength: object.ContentLength,
	}, nil
}

// DeleteObject 记录调用，在可选阻塞释放后幂等删除对象。
func (s *MockImageStorage) DeleteObject(ctx context.Context, objectKey string) error {
	s.mu.Lock()
	s.deleteCalls = append(s.deleteCalls, objectKey)
	behavior, hasBehavior := popFirst(&s.deleteBehaviors)
	s.signal.notify()
	s.mu.Unlock()

	if hasBehavior {
		if err := runStorageTiming(ctx, behavior.Delay, behavior.Release); err != nil {
			return err
		}
		if behavior.Err != nil {
			return behavior.Err
		}
	}
	s.mu.Lock()
	delete(s.objects, objectKey)
	delete(s.privateObjects, objectKey)
	s.mu.Unlock()
	return nil
}

// SetObjectPublicAccess 记录调用并幂等切换对象公开读状态。
func (s *MockImageStorage) SetObjectPublicAccess(
	ctx context.Context,
	objectKey string,
	public bool,
) error {
	s.mu.Lock()
	s.accessCalls = append(s.accessCalls, StorageAccessCall{ObjectKey: objectKey, Public: public})
	behavior, hasBehavior := popFirst(&s.accessBehaviors)
	s.signal.notify()
	s.mu.Unlock()

	if hasBehavior {
		if err := runStorageTiming(ctx, behavior.Delay, behavior.Release); err != nil {
			return err
		}
		if behavior.Err != nil {
			return behavior.Err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.objects[objectKey]; !exists {
		return ErrMockObjectNotFound
	}
	s.privateObjects[objectKey] = !public
	return nil
}

// PublicURL 返回指定精确 URL 或基于默认前缀构造的 URL。
func (s *MockImageStorage) PublicURL(objectKey string) (string, error) {
	s.mu.Lock()
	s.publicURLCalls = append(s.publicURLCalls, objectKey)
	publicURL, exact := s.publicURLs[objectKey]
	err := s.publicURLErrs[objectKey]
	base := s.publicURLBase
	s.signal.notify()
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	if exact {
		return publicURL, nil
	}
	if strings.TrimSpace(objectKey) == "" {
		return "", errors.New("object key 不能为空")
	}
	return joinObjectURL(base, objectKey), nil
}

// ReadPublicURL 模拟匿名客户端通过 PublicURL 读取对象。
func (s *MockImageStorage) ReadPublicURL(publicURL string) (StoredObject, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for objectKey, object := range s.objects {
		candidate, exact := s.publicURLs[objectKey]
		if !exact {
			candidate = joinObjectURL(s.publicURLBase, objectKey)
		}
		if candidate != publicURL {
			continue
		}
		if s.privateObjects[objectKey] {
			return StoredObject{}, ErrMockPublicAccessDenied
		}
		return object, nil
	}
	return StoredObject{}, ErrMockObjectNotFound
}

// PresignCalls 返回不可变请求快照。
func (s *MockImageStorage) PresignCalls() []service.StoragePresignRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]service.StoragePresignRequest{}, s.presignCalls...)
}

// HeadCalls 返回 HEAD object key 调用顺序。
func (s *MockImageStorage) HeadCalls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.headCalls...)
}

// DeleteCalls 返回删除 object key 调用顺序。
func (s *MockImageStorage) DeleteCalls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.deleteCalls...)
}

// AccessCalls 返回对象公开读切换调用顺序。
func (s *MockImageStorage) AccessCalls() []StorageAccessCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]StorageAccessCall{}, s.accessCalls...)
}

// PublicURLCalls 返回公开 URL 构造调用顺序。
func (s *MockImageStorage) PublicURLCalls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.publicURLCalls...)
}

// LastPresign 返回最近一次 presign 请求。
func (s *MockImageStorage) LastPresign() (service.StoragePresignRequest, bool) {
	calls := s.PresignCalls()
	if len(calls) == 0 {
		return service.StoragePresignRequest{}, false
	}
	return calls[len(calls)-1], true
}

// WaitForDeleteCalls 等待至少 n 次删除进入 Mock，不使用 sleep 轮询。
func (s *MockImageStorage) WaitForDeleteCalls(ctx context.Context, n int) bool {
	return waitForCalls(ctx, func() (int, <-chan struct{}) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.deleteCalls), s.signal.changed
	}, n)
}

// WaitForHeadCalls 等待至少 n 次 HEAD 进入 Mock。
func (s *MockImageStorage) WaitForHeadCalls(ctx context.Context, n int) bool {
	return waitForCalls(ctx, func() (int, <-chan struct{}) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.headCalls), s.signal.changed
	}, n)
}

// RequirePresignCalls 断言 presign 调用次数。
func (s *MockImageStorage) RequirePresignCalls(t testing.TB, want int) {
	t.Helper()
	if got := len(s.PresignCalls()); got != want {
		t.Fatalf("presign 调用次数不符：want=%d got=%d", want, got)
	}
}

// RequireDeleteCalls 断言删除调用次数。
func (s *MockImageStorage) RequireDeleteCalls(t testing.TB, want int) {
	t.Helper()
	if got := len(s.DeleteCalls()); got != want {
		t.Fatalf("删除调用次数不符：want=%d got=%d", want, got)
	}
}

// RequireAccessCalls 断言对象公开读切换调用次数。
func (s *MockImageStorage) RequireAccessCalls(t testing.TB, want int) {
	t.Helper()
	if got := len(s.AccessCalls()); got != want {
		t.Fatalf("对象公开读切换调用次数不符：want=%d got=%d", want, got)
	}
}

func runStorageTiming(
	ctx context.Context,
	delay time.Duration,
	release <-chan struct{},
) error {
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return waitForRelease(ctx, release)
}

func joinObjectURL(base string, objectKey string) string {
	return strings.TrimRight(base, "/") + "/" + url.PathEscape(objectKey)
}

func popFirst[T any](values *[]T) (T, bool) {
	var zero T
	if len(*values) == 0 {
		return zero, false
	}
	value := (*values)[0]
	*values = (*values)[1:]
	return value, true
}

var _ service.ImageStorage = (*MockImageStorage)(nil)

// MockImageCachePurger 是并发安全的 CDN 刷新端口 Mock。
type MockImageCachePurger struct {
	mu     sync.Mutex
	calls  []string
	errors []error
}

// NewMockImageCachePurger 创建默认成功的 CDN 刷新 Mock。
func NewMockImageCachePurger() *MockImageCachePurger { return &MockImageCachePurger{} }

// QueueErrors 按调用顺序编排刷新错误；nil 表示该次成功。
func (p *MockImageCachePurger) QueueErrors(errors ...error) {
	p.mu.Lock()
	p.errors = append(p.errors, errors...)
	p.mu.Unlock()
}

// PurgeURL 记录 URL 并返回下一项预设错误。
func (p *MockImageCachePurger) PurgeURL(_ context.Context, publicURL string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, publicURL)
	if len(p.errors) == 0 {
		return nil
	}
	err := p.errors[0]
	p.errors = p.errors[1:]
	return err
}

// Calls 返回全部 CDN 刷新 URL。
func (p *MockImageCachePurger) Calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string{}, p.calls...)
}

var _ service.ImageCachePurger = (*MockImageCachePurger)(nil)
