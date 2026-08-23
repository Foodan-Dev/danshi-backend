package testutil

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

// ErrMockEmailDelivery 是测试发信失败的稳定错误。
var ErrMockEmailDelivery = errors.New("mock email delivery failed")

// EmailBehavior 描述匹配到的一次投递如何失败、变慢或等待 context 超时。
type EmailBehavior struct {
	Err     error
	Delay   time.Duration
	Release <-chan struct{}
	Timeout bool
}

// EmailRule 按规范化邮箱和全局调用序号匹配投递行为。
// 零值字段不参与匹配；多项同时设置时必须全部命中。
type EmailRule struct {
	Email    string
	Call     int
	Behavior EmailBehavior
}

// EmailAttempt 记录所有尝试，包括失败和超时。
type EmailAttempt struct {
	Sequence int
	Email    string
	Code     string
}

// EmailDelivery 只记录实际成功返回的投递。
type EmailDelivery struct {
	Sequence int
	Email    string
	Code     string
}

// MockEmailSender 是可编程、并发安全的验证码投递 Mock。
type MockEmailSender struct {
	mu sync.Mutex

	rules           []EmailRule
	defaultBehavior EmailBehavior
	attempts        []EmailAttempt
	deliveries      []EmailDelivery
	signal          callSignal
}

// NewMockEmailSender 创建默认成功投递的验证码 Mock。
func NewMockEmailSender() *MockEmailSender {
	return &MockEmailSender{signal: newCallSignal()}
}

// EmailFailure 创建失败行为；失败尝试不会出现在 Deliveries/Codes 中。
func EmailFailure(err error) EmailBehavior {
	if err == nil {
		err = ErrMockEmailDelivery
	}
	return EmailBehavior{Err: err}
}

// EmailTimeout 一直等待调用方 context 到期并返回 context 错误。
func EmailTimeout() EmailBehavior {
	return EmailBehavior{Timeout: true}
}

// EmailBlocked 创建由测试 channel 精确释放的慢投递行为。
func EmailBlocked(release <-chan struct{}) EmailBehavior {
	return EmailBehavior{Release: release}
}

// SetDefault 设置没有规则命中时的投递行为。
func (s *MockEmailSender) SetDefault(behavior EmailBehavior) {
	s.mu.Lock()
	s.defaultBehavior = behavior
	s.mu.Unlock()
}

// Program 按传入顺序追加规则；最先命中的规则生效。
func (s *MockEmailSender) Program(rules ...EmailRule) {
	s.mu.Lock()
	s.rules = append(s.rules, rules...)
	s.mu.Unlock()
}

// SendRegistrationCode 实现 service.VerificationEmailSender。
func (s *MockEmailSender) SendRegistrationCode(
	ctx context.Context,
	email string,
	code string,
) error {
	normalized := strings.ToLower(strings.TrimSpace(email))
	s.mu.Lock()
	sequence := len(s.attempts) + 1
	s.attempts = append(s.attempts, EmailAttempt{
		Sequence: sequence, Email: normalized, Code: code,
	})
	behavior := s.defaultBehavior
	for _, rule := range s.rules {
		if emailRuleMatches(rule, normalized, sequence) {
			behavior = rule.Behavior
			break
		}
	}
	s.signal.notify()
	s.mu.Unlock()

	if err := runEmailTiming(ctx, behavior); err != nil {
		return err
	}
	if behavior.Err != nil {
		return behavior.Err
	}
	s.mu.Lock()
	s.deliveries = append(s.deliveries, EmailDelivery{
		Sequence: sequence, Email: normalized, Code: code,
	})
	s.signal.notify()
	s.mu.Unlock()
	return nil
}

// Attempts 返回包含失败/超时尝试的不可变快照；email 为空时返回全部。
func (s *MockEmailSender) Attempts(email string) []EmailAttempt {
	normalized := strings.ToLower(strings.TrimSpace(email))
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]EmailAttempt, 0, len(s.attempts))
	for _, attempt := range s.attempts {
		if normalized == "" || attempt.Email == normalized {
			result = append(result, attempt)
		}
	}
	return result
}

// Deliveries 返回成功投递的不可变快照；email 为空时返回全部。
func (s *MockEmailSender) Deliveries(email string) []EmailDelivery {
	normalized := strings.ToLower(strings.TrimSpace(email))
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]EmailDelivery, 0, len(s.deliveries))
	for _, delivery := range s.deliveries {
		if normalized == "" || delivery.Email == normalized {
			result = append(result, delivery)
		}
	}
	return result
}

// Codes 返回某邮箱全部成功投递的验证码，按时间正序排列。
func (s *MockEmailSender) Codes(email string) []string {
	deliveries := s.Deliveries(email)
	codes := make([]string, len(deliveries))
	for index := range deliveries {
		codes[index] = deliveries[index].Code
	}
	return codes
}

// LastCode 返回某邮箱最近一次成功投递的验证码。
func (s *MockEmailSender) LastCode(email string) (string, bool) {
	codes := s.Codes(email)
	if len(codes) == 0 {
		return "", false
	}
	return codes[len(codes)-1], true
}

// DeliveryCount 返回某邮箱成功投递次数。
func (s *MockEmailSender) DeliveryCount(email string) int {
	return len(s.Deliveries(email))
}

// WaitForAttempts 等待至少 n 次调用进入 sender，适合无 sleep 的并发测试。
func (s *MockEmailSender) WaitForAttempts(ctx context.Context, n int) bool {
	return waitForCalls(ctx, func() (int, <-chan struct{}) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.attempts), s.signal.changed
	}, n)
}

// WaitForDeliveries 等待至少 n 次调用成功返回。
func (s *MockEmailSender) WaitForDeliveries(ctx context.Context, n int) bool {
	return waitForCalls(ctx, func() (int, <-chan struct{}) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.deliveries), s.signal.changed
	}, n)
}

// RequireNoDelivery 断言目标邮箱没有成功投递，防止防枚举路径误发邮件。
func (s *MockEmailSender) RequireNoDelivery(t testing.TB, email string) {
	t.Helper()
	if deliveries := s.Deliveries(email); len(deliveries) != 0 {
		t.Fatalf("邮箱 %q 不应收到投递，实际 %d 次", email, len(deliveries))
	}
}

// RequireDeliveryCount 断言某邮箱成功投递次数。
func (s *MockEmailSender) RequireDeliveryCount(t testing.TB, email string, want int) {
	t.Helper()
	if got := s.DeliveryCount(email); got != want {
		t.Fatalf("邮箱 %q 投递次数不符：want=%d got=%d", email, want, got)
	}
}

func emailRuleMatches(rule EmailRule, email string, call int) bool {
	if rule.Email != "" && strings.ToLower(strings.TrimSpace(rule.Email)) != email {
		return false
	}
	return rule.Call == 0 || rule.Call == call
}

func runEmailTiming(ctx context.Context, behavior EmailBehavior) error {
	if behavior.Delay > 0 {
		timer := time.NewTimer(behavior.Delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := waitForRelease(ctx, behavior.Release); err != nil {
		return err
	}
	if behavior.Timeout {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

var _ service.VerificationEmailSender = (*MockEmailSender)(nil)
