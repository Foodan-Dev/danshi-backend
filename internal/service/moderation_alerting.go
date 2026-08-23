package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

const (
	defaultCallbackAuthFailureThreshold = 5
	defaultCallbackAuthFailureWindow    = time.Minute
)

// CallbackAuthFailureMonitor 在单进程短窗口内聚合错误回调令牌，避免单次探测制造噪音。
type CallbackAuthFailureMonitor struct {
	mu          sync.Mutex
	threshold   int
	window      time.Duration
	now         func() time.Time
	windowStart time.Time
	failures    int
	alerted     bool
}

// NewCallbackAuthFailureMonitor 创建固定窗口聚合器。无效参数只供绕过配置加载的测试依赖兜底；
// 正常启动仍由 config.Validate 拒绝无效值。
func NewCallbackAuthFailureMonitor(threshold int, window time.Duration) *CallbackAuthFailureMonitor {
	if threshold < 2 {
		threshold = defaultCallbackAuthFailureThreshold
	}
	if window <= 0 {
		window = defaultCallbackAuthFailureWindow
	}
	return &CallbackAuthFailureMonitor{
		threshold: threshold,
		window:    window,
		now:       time.Now,
	}
}

// RecordFailure 记录一次失败；同一窗口只在首次达到阈值时返回 alert=true。
func (m *CallbackAuthFailureMonitor) RecordFailure() (occurrences int, alert bool) {
	if m == nil {
		return 0, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	if m.windowStart.IsZero() || !now.Before(m.windowStart.Add(m.window)) {
		m.windowStart = now
		m.failures = 0
		m.alerted = false
	}
	m.failures++
	if m.failures >= m.threshold && !m.alerted {
		m.alerted = true
		return m.failures, true
	}
	return m.failures, false
}

// WindowSeconds 返回告警载荷使用的安全窗口摘要。
func (m *CallbackAuthFailureMonitor) WindowSeconds() int {
	if m == nil {
		return 0
	}
	return int(m.window / time.Second)
}

const reviewBacklogAlertKey = "review_backlog"

// ReviewBacklogCheckOptions 是一次帖子粒度积压检查的启动期已校验参数。
type ReviewBacklogCheckOptions struct {
	Threshold int
	Cooldown  time.Duration
	Now       time.Time
}

// ReviewBacklogCheckResult 描述本次队列深度与是否安排了提交后告警。
type ReviewBacklogCheckResult struct {
	QueueDepth     int64
	Threshold      int64
	AlertScheduled bool
}

// ModerationAlertService 编排待复核队列计数、跨任务抑制状态与提交后告警。
type ModerationAlertService struct {
	alerter ModerationAlerter
	alerts  repository.ModerationAlertRepository
}

// NewModerationAlertService 创建审核异常检查服务。
func NewModerationAlertService(alerter ModerationAlerter) *ModerationAlertService {
	if alerter == nil {
		alerter = DiscardModerationAlerter{}
	}
	return &ModerationAlertService{alerter: alerter}
}

// CheckReviewBacklog 对管理端真实 queue_items 计数，并以“回落重置 + 冷却重报”抑制刷屏。
func (s *ModerationAlertService) CheckReviewBacklog(
	ctx context.Context,
	options ReviewBacklogCheckOptions,
) (ReviewBacklogCheckResult, error) {
	if options.Threshold <= 0 {
		return ReviewBacklogCheckResult{}, fmt.Errorf("待复核积压阈值必须为正数")
	}
	if options.Cooldown <= 0 {
		return ReviewBacklogCheckResult{}, fmt.Errorf("待复核积压告警冷却必须为正时长")
	}
	if options.Now.IsZero() {
		options.Now = time.Now()
	}
	now := options.Now.UTC()
	queueDepth, err := s.alerts.CountPendingReviewQueue(ctx)
	if err != nil {
		return ReviewBacklogCheckResult{}, fmt.Errorf("统计待复核队列: %w", err)
	}
	state, err := s.alerts.EnsureAndLockState(ctx, reviewBacklogAlertKey, now)
	if err != nil {
		return ReviewBacklogCheckResult{}, fmt.Errorf("锁定积压告警状态: %w", err)
	}

	threshold := int64(options.Threshold)
	overThreshold := queueDepth >= threshold
	shouldAlert := overThreshold && (!state.Active || state.LastAlertedAt == nil ||
		!now.Before(state.LastAlertedAt.Add(options.Cooldown)))
	state.Active = overThreshold
	state.LastObservedCount = queueDepth
	state.UpdatedAt = now
	if shouldAlert {
		state.LastAlertedAt = &now
	}
	if err := s.alerts.SaveState(ctx, state); err != nil {
		return ReviewBacklogCheckResult{}, fmt.Errorf("保存积压告警状态: %w", err)
	}
	if shouldAlert {
		s.alerter.Alert(ctx, ModerationAlert{
			Kind:       ModerationAlertKindReviewBacklog,
			QueueDepth: queueDepth,
			Threshold:  threshold,
		})
	}
	return ReviewBacklogCheckResult{
		QueueDepth: queueDepth, Threshold: threshold, AlertScheduled: shouldAlert,
	}, nil
}
