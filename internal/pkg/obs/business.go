package obs

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
)

const businessMetricCollectionTimeout = 3 * time.Second

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
	desc    *prometheus.Desc
}

func newReviewQueueCollector(counter ReviewQueueCounter) prometheus.Collector {
	return &reviewQueueCollector{
		counter: counter,
		desc: prometheus.NewDesc(
			"danshi_moderation_review_queue_items",
			"Current review queue items using the same grouping semantics as the admin queue.",
			nil,
			nil,
		),
	}
}

func (c *reviewQueueCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *reviewQueueCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), businessMetricCollectionTimeout)
	defer cancel()
	count, err := c.counter(ctx)
	if err != nil {
		ch <- prometheus.NewInvalidMetric(c.desc, err)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(count))
}

var _ BusinessRecorder = (*Metrics)(nil)
