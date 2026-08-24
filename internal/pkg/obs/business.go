package obs

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
)

const (
	reviewQueueRefreshTimeout  = 3 * time.Second
	reviewQueueRefreshInterval = 15 * time.Second
)

// BusinessRecorder 是业务层只写的低基数指标端口。实现必须把所有入参收敛为固定枚举，
// 不能把用户、对象、任务、邮箱、正文或错误文本写入 label。
type BusinessRecorder interface {
	RecordModerationSubmission(provider, scene string)
	RecordModerationProviderFailure(provider, scene, reason string)
	RecordModerationTerminal(ctx context.Context, provider, scene, outcome string)
	RecordModerationCallback(ctx context.Context, provider, outcome, reason string)
	RecordVerification(ctx context.Context, provider, outcome, reason string)
}

// ReviewQueueCounter 返回管理端真实 queue_items 的当前条目数。
type ReviewQueueCounter func(ctx context.Context) (int64, error)

type moderationMetrics struct {
	submissions      *prometheus.CounterVec
	providerFailures *prometheus.CounterVec
	terminalOutcomes *prometheus.CounterVec
	callbacks        *prometheus.CounterVec
}

func newModerationMetrics() moderationMetrics {
	return moderationMetrics{
		submissions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "danshi", Subsystem: "moderation", Name: "submissions_total",
			Help: "Total moderation provider submissions by bounded provider and scene.",
		}, []string{"provider", "scene"}),
		providerFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "danshi", Subsystem: "moderation", Name: "provider_failures_total",
			Help: "Total moderation provider invocation failures by bounded reason.",
		}, []string{"provider", "scene", "reason"}),
		terminalOutcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "danshi", Subsystem: "moderation", Name: "terminal_outcomes_total",
			Help: "Total committed moderation terminal outcomes.",
		}, []string{"provider", "scene", "outcome"}),
		callbacks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "danshi", Subsystem: "moderation", Name: "callbacks_total",
			Help: "Total moderation callbacks by bounded outcome and reason.",
		}, []string{"provider", "outcome", "reason"}),
	}
}

type verificationMetrics struct {
	events *prometheus.CounterVec
}

func newVerificationMetrics() verificationMetrics {
	return verificationMetrics{events: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "danshi", Subsystem: "verification", Name: "events_total",
		Help: "Total email verification events by bounded outcome and reason.",
	}, []string{"provider", "outcome", "reason"})}
}

// RecordModerationSubmission 记录一次真正进入审核端口的调用。
func (m *Metrics) RecordModerationSubmission(provider, scene string) {
	if m == nil {
		return
	}
	m.moderation.submissions.WithLabelValues(
		boundedProvider(provider), boundedScene(scene),
	).Inc()
}

// RecordModerationProviderFailure 记录审核供应商调用失败；错误正文不会成为 label。
func (m *Metrics) RecordModerationProviderFailure(provider, scene, reason string) {
	if m == nil {
		return
	}
	m.moderation.providerFailures.WithLabelValues(
		boundedProvider(provider), boundedScene(scene), boundedReason(reason),
	).Inc()
}

// RecordModerationTerminal 只在当前事务成功提交后记录终态；非事务调用立即记录。
func (m *Metrics) RecordModerationTerminal(
	ctx context.Context,
	provider string,
	scene string,
	outcome string,
) {
	if m == nil {
		return
	}
	provider = boundedProvider(provider)
	scene = boundedScene(scene)
	outcome = boundedModerationOutcome(outcome)
	recordAfterCommit(ctx, func() {
		m.moderation.terminalOutcomes.WithLabelValues(provider, scene, outcome).Inc()
	})
}

// RecordModerationCallback 对已处理回调等待事务提交；鉴权和载荷失败即时计数。
func (m *Metrics) RecordModerationCallback(
	ctx context.Context,
	provider string,
	outcome string,
	reason string,
) {
	if m == nil {
		return
	}
	provider = boundedProvider(provider)
	outcome = boundedCallbackOutcome(outcome)
	reason = boundedReason(reason)
	record := func() {
		m.moderation.callbacks.WithLabelValues(provider, outcome, reason).Inc()
	}
	if outcome == "processed" {
		recordAfterCommit(ctx, record)
		return
	}
	record()
}

// RecordVerification 对成功发信等待事务提交；失败、限流和在途拒绝即时计数。
func (m *Metrics) RecordVerification(
	ctx context.Context,
	provider string,
	outcome string,
	reason string,
) {
	if m == nil {
		return
	}
	provider = boundedProvider(provider)
	outcome = boundedVerificationOutcome(outcome)
	reason = boundedReason(reason)
	record := func() {
		m.verification.events.WithLabelValues(provider, outcome, reason).Inc()
	}
	if outcome == "send" {
		recordAfterCommit(ctx, record)
		return
	}
	record()
}

func recordAfterCommit(ctx context.Context, record func()) {
	callback := func(context.Context) { record() }
	if db.AfterCommit(ctx, callback) {
		return
	}
	record()
}

func boundedProvider(value string) string {
	switch value {
	case "tencent_ci", "tencent_ses", "dev_allow", "log", "manual", "none":
		return value
	default:
		return "unknown"
	}
}

func boundedScene(value string) string {
	switch value {
	case "post", "tag", "comment", "user", "image_asset":
		return value
	default:
		return "unknown"
	}
}

func boundedModerationOutcome(value string) string {
	switch value {
	case "pass", "review", "block", "provider_failed":
		return value
	default:
		return "unknown"
	}
}

func boundedCallbackOutcome(value string) string {
	switch value {
	case "auth_failed", "invalid", "processed":
		return value
	default:
		return "invalid"
	}
}

func boundedVerificationOutcome(value string) string {
	switch value {
	case "send", "provider_failure", "rate_limited", "inflight_rejected":
		return value
	default:
		return "provider_failure"
	}
}

func boundedReason(value string) string {
	switch value {
	case "none", "provider_error", "auth", "payload", "target", "processing",
		"applied", "duplicate", "rate_limited", "inflight_rejected", "unavailable":
		return value
	default:
		return "unknown"
	}
}

type reviewQueueCollector struct {
	counter ReviewQueueCounter

	mu                 sync.RWMutex
	refreshing         bool
	hasValue           bool
	value              int64
	lastSuccess        time.Time
	lastAttempt        time.Time
	lastRefreshSuccess bool
	refreshErrors      uint64

	refreshTimeout  time.Duration
	refreshInterval time.Duration
	now             func() time.Time

	itemsDesc              *prometheus.Desc
	cacheReadyDesc         *prometheus.Desc
	lastSuccessDesc        *prometheus.Desc
	lastRefreshSuccessDesc *prometheus.Desc
	refreshingDesc         *prometheus.Desc
	refreshErrorsDesc      *prometheus.Desc
}

func newReviewQueueCollector(counter ReviewQueueCounter) *reviewQueueCollector {
	return &reviewQueueCollector{
		counter:        counter,
		refreshTimeout: reviewQueueRefreshTimeout, refreshInterval: reviewQueueRefreshInterval,
		now: time.Now,
		itemsDesc: prometheus.NewDesc(
			"danshi_moderation_review_queue_cached_items",
			"Last successfully observed review queue items; inspect cache status metrics for freshness.",
			nil,
			nil,
		),
		cacheReadyDesc: prometheus.NewDesc(
			"danshi_moderation_review_queue_cache_ready",
			"Whether a successful review queue observation is available (1) or not (0).",
			nil, nil,
		),
		lastSuccessDesc: prometheus.NewDesc(
			"danshi_moderation_review_queue_last_success_timestamp_seconds",
			"Unix timestamp of the last successful review queue observation.",
			nil, nil,
		),
		lastRefreshSuccessDesc: prometheus.NewDesc(
			"danshi_moderation_review_queue_last_refresh_success",
			"Whether the last completed review queue refresh succeeded (1) or failed/not run (0).",
			nil, nil,
		),
		refreshingDesc: prometheus.NewDesc(
			"danshi_moderation_review_queue_refresh_in_progress",
			"Whether one bounded review queue refresh is currently in progress.",
			nil, nil,
		),
		refreshErrorsDesc: prometheus.NewDesc(
			"danshi_moderation_review_queue_refresh_errors_total",
			"Total failed or invalid review queue refresh attempts.",
			nil, nil,
		),
	}
}

func (c *reviewQueueCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.itemsDesc
	ch <- c.cacheReadyDesc
	ch <- c.lastSuccessDesc
	ch <- c.lastRefreshSuccessDesc
	ch <- c.refreshingDesc
	ch <- c.refreshErrorsDesc
}

// Refresh 尝试刷新队列缓存。同一时刻只有首个调用方执行数据库查询；其余调用方
// 立即返回并由 Collect 导出既有缓存。查询与调用方 context 绑定且有硬超时，不启动
// 后台 goroutine，也不建立独立连接池。
func (c *reviewQueueCollector) Refresh(ctx context.Context) {
	if c == nil || c.counter == nil {
		return
	}
	now := c.now().UTC()
	c.mu.Lock()
	if c.refreshing || (!c.lastAttempt.IsZero() && c.refreshInterval > 0 &&
		now.Before(c.lastAttempt.Add(c.refreshInterval))) {
		c.mu.Unlock()
		return
	}
	c.refreshing = true
	c.lastAttempt = now
	c.mu.Unlock()

	queryCtx, cancel := context.WithTimeout(ctx, c.refreshTimeout)
	count, err := c.counter(queryCtx)
	cancel()
	if err == nil && count < 0 {
		err = errors.New("review queue counter returned a negative value")
	}
	finishedAt := c.now().UTC()

	c.mu.Lock()
	c.refreshing = false
	c.lastRefreshSuccess = err == nil
	if err != nil {
		c.refreshErrors++
	} else {
		c.hasValue = true
		c.value = count
		c.lastSuccess = finishedAt
	}
	c.mu.Unlock()
}

func (c *reviewQueueCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	refreshing := c.refreshing
	hasValue := c.hasValue
	value := c.value
	lastSuccess := c.lastSuccess
	lastRefreshSuccess := c.lastRefreshSuccess
	refreshErrors := c.refreshErrors
	c.mu.RUnlock()

	if hasValue {
		ch <- prometheus.MustNewConstMetric(c.itemsDesc, prometheus.GaugeValue, float64(value))
		ch <- prometheus.MustNewConstMetric(
			c.lastSuccessDesc, prometheus.GaugeValue, float64(lastSuccess.Unix()),
		)
	}
	ch <- prometheus.MustNewConstMetric(c.cacheReadyDesc, prometheus.GaugeValue, boolFloat(hasValue))
	ch <- prometheus.MustNewConstMetric(
		c.lastRefreshSuccessDesc, prometheus.GaugeValue, boolFloat(lastRefreshSuccess),
	)
	ch <- prometheus.MustNewConstMetric(c.refreshingDesc, prometheus.GaugeValue, boolFloat(refreshing))
	ch <- prometheus.MustNewConstMetric(
		c.refreshErrorsDesc, prometheus.CounterValue, float64(refreshErrors),
	)
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

var _ BusinessRecorder = (*Metrics)(nil)
