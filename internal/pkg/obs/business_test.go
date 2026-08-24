package obs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	hertzconfig "github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"

	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
)

func TestBusinessMetricsCommitBoundaryAndQueueGauge(t *testing.T) {
	m, err := NewMetrics(nil, WithReviewQueueCounter(func(context.Context) (int64, error) {
		return 7, nil
	}))
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}

	ctx, afterCommit := db.WithAfterCommitQueue(context.Background())
	m.RecordModerationSubmission("tencent_ci", "post")
	m.RecordModerationProviderFailure("tencent_ci", "image_asset", "provider_error")
	m.RecordModerationTerminal(ctx, "tencent_ci", "post", "review")
	m.RecordModerationCallback(ctx, "tencent_ci", "processed", "applied")
	m.RecordVerification(ctx, "tencent_ses", "send", "none")
	m.RecordVerification(ctx, "none", "rate_limited", "rate_limited")
	m.reviewQueue.Refresh(context.Background())

	before := gatherBusinessMetrics(t, m)
	for _, absent := range []string{
		`danshi_moderation_terminal_outcomes_total{outcome="review",provider="tencent_ci",scene="post"}`,
		`danshi_moderation_callbacks_total{outcome="processed",provider="tencent_ci",reason="applied"}`,
		`danshi_verification_events_total{outcome="send",provider="tencent_ses",reason="none"}`,
	} {
		if strings.Contains(before, absent) {
			t.Fatalf("事务提交前不应导出 %q\n%s", absent, before)
		}
	}
	for _, present := range []string{
		`danshi_moderation_submissions_total{provider="tencent_ci",scene="post"} 1`,
		`danshi_moderation_provider_failures_total{provider="tencent_ci",reason="provider_error",scene="image_asset"} 1`,
		`danshi_verification_events_total{outcome="rate_limited",provider="none",reason="rate_limited"} 1`,
		`danshi_moderation_review_queue_cached_items 7`,
		`danshi_moderation_review_queue_cache_ready 1`,
		`danshi_moderation_review_queue_last_refresh_success 1`,
	} {
		if !strings.Contains(before, present) {
			t.Errorf("提交前指标缺少 %q\n%s", present, before)
		}
	}

	if panics := afterCommit.Run(context.Background()); len(panics) != 0 {
		t.Fatalf("提交后指标回调 panic: %v", panics)
	}
	after := gatherBusinessMetrics(t, m)
	for _, present := range []string{
		`danshi_moderation_terminal_outcomes_total{outcome="review",provider="tencent_ci",scene="post"} 1`,
		`danshi_moderation_callbacks_total{outcome="processed",provider="tencent_ci",reason="applied"} 1`,
		`danshi_verification_events_total{outcome="send",provider="tencent_ses",reason="none"} 1`,
	} {
		if !strings.Contains(after, present) {
			t.Errorf("提交后指标缺少 %q\n%s", present, after)
		}
	}
}

func TestBusinessMetricsConcurrentRecordingAndSentinelRedaction(t *testing.T) {
	m := mustMetrics(t, nil)
	const workers = 32
	const iterations = 40
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for range iterations {
				m.RecordModerationSubmission("tencent_ci", "comment")
				m.RecordVerification(
					context.Background(), "tencent_ses", "provider_failure", "provider_error",
				)
			}
		}()
	}
	group.Wait()

	sentinel := "sentinel-user-object-job-email-body-error-9b7f"
	m.RecordModerationSubmission(sentinel, sentinel)
	m.RecordModerationProviderFailure(sentinel, sentinel, sentinel)
	m.RecordModerationTerminal(context.Background(), sentinel, sentinel, sentinel)
	m.RecordModerationCallback(context.Background(), sentinel, sentinel, sentinel)
	m.RecordVerification(context.Background(), sentinel, sentinel, sentinel)

	output := gatherBusinessMetrics(t, m)
	if strings.Contains(output, sentinel) {
		t.Fatalf("业务指标泄露了未受信任 label 哨兵：\n%s", output)
	}
	expected := workers * iterations
	for _, sample := range []string{
		fmt.Sprintf(
			`danshi_moderation_submissions_total{provider="tencent_ci",scene="comment"} %d`,
			expected,
		),
		fmt.Sprintf(
			`danshi_verification_events_total{outcome="provider_failure",provider="tencent_ses",reason="provider_error"} %d`,
			expected,
		),
		`danshi_moderation_submissions_total{provider="unknown",scene="unknown"} 1`,
	} {
		if !strings.Contains(output, sample) {
			t.Errorf("并发指标缺少 %q\n%s", sample, output)
		}
	}
}

func TestReviewQueueCollectorConcurrentRefreshUsesOneQuery(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	collector := newReviewQueueCollector(func(context.Context) (int64, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		return 11, nil
	})
	collector.refreshInterval = 0

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		collector.Refresh(context.Background())
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("首个队列刷新没有进入查询")
	}

	const concurrentScrapes = 32
	var group sync.WaitGroup
	for range concurrentScrapes {
		group.Add(1)
		go func() {
			defer group.Done()
			collector.Refresh(context.Background())
		}()
	}
	nonBlocking := make(chan struct{})
	go func() {
		group.Wait()
		close(nonBlocking)
	}()
	select {
	case <-nonBlocking:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("并发 scrape 等待了正在执行的队列查询")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("并发 scrape 触发 %d 次查询，期望 1", got)
	}
	during := gatherCollector(t, collector)
	for _, sample := range []string{
		`danshi_moderation_review_queue_cache_ready 0`,
		`danshi_moderation_review_queue_refresh_in_progress 1`,
	} {
		if !strings.Contains(during, sample) {
			t.Errorf("刷新中指标缺少 %q\n%s", sample, during)
		}
	}

	close(release)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("队列刷新没有结束")
	}
	after := gatherCollector(t, collector)
	for _, sample := range []string{
		`danshi_moderation_review_queue_cached_items 11`,
		`danshi_moderation_review_queue_cache_ready 1`,
		`danshi_moderation_review_queue_refresh_in_progress 0`,
	} {
		if !strings.Contains(after, sample) {
			t.Errorf("刷新后指标缺少 %q\n%s", sample, after)
		}
	}
}

func TestReviewQueueCollectorRefreshIntervalUsesCache(t *testing.T) {
	var calls atomic.Int32
	now := time.Unix(1_800_000_000, 0).UTC()
	collector := newReviewQueueCollector(func(context.Context) (int64, error) {
		return int64(calls.Add(1)), nil
	})
	collector.now = func() time.Time { return now }

	collector.Refresh(context.Background())
	collector.Refresh(context.Background())
	if got := calls.Load(); got != 1 {
		t.Fatalf("缓存窗口内连续 scrape 触发 %d 次查询，期望 1", got)
	}
	if output := gatherCollector(t, collector); !strings.Contains(
		output, `danshi_moderation_review_queue_cached_items 1`,
	) {
		t.Fatalf("缓存窗口内没有导出最近成功值\n%s", output)
	}

	now = now.Add(reviewQueueRefreshInterval)
	collector.Refresh(context.Background())
	if got := calls.Load(); got != 2 {
		t.Fatalf("缓存窗口结束后查询次数 = %d，期望 2", got)
	}
	if output := gatherCollector(t, collector); !strings.Contains(
		output, `danshi_moderation_review_queue_cached_items 2`,
	) {
		t.Fatalf("刷新窗口结束后没有导出新值\n%s", output)
	}
}

func TestReviewQueueCollectorFailureRecoveryAndStaleCache(t *testing.T) {
	var calls atomic.Int32
	collector := newReviewQueueCollector(func(context.Context) (int64, error) {
		switch calls.Add(1) {
		case 1:
			return 0, errors.New("initial failure")
		case 2:
			return 17, nil
		default:
			return 0, errors.New("refresh failure")
		}
	})
	collector.refreshInterval = 0

	startup := gatherCollector(t, collector)
	if strings.Contains(startup, "review_queue_cached_items") {
		t.Fatalf("启动尚无成功值时不应导出缓存队列深度\n%s", startup)
	}
	if !strings.Contains(startup, `danshi_moderation_review_queue_cache_ready 0`) {
		t.Fatalf("启动状态没有明确报告 cache_ready=0\n%s", startup)
	}

	collector.Refresh(context.Background())
	failed := gatherCollector(t, collector)
	for _, sample := range []string{
		`danshi_moderation_review_queue_cache_ready 0`,
		`danshi_moderation_review_queue_last_refresh_success 0`,
		`danshi_moderation_review_queue_refresh_errors_total 1`,
	} {
		if !strings.Contains(failed, sample) {
			t.Errorf("首次失败指标缺少 %q\n%s", sample, failed)
		}
	}
	if strings.Contains(failed, "review_queue_cached_items") {
		t.Fatalf("首次失败不能伪造队列深度\n%s", failed)
	}

	collector.Refresh(context.Background())
	recovered := gatherCollector(t, collector)
	for _, sample := range []string{
		`danshi_moderation_review_queue_cached_items 17`,
		`danshi_moderation_review_queue_cache_ready 1`,
		`danshi_moderation_review_queue_last_refresh_success 1`,
	} {
		if !strings.Contains(recovered, sample) {
			t.Errorf("恢复后指标缺少 %q\n%s", sample, recovered)
		}
	}
	if !strings.Contains(recovered, "danshi_moderation_review_queue_last_success_timestamp_seconds") {
		t.Fatalf("恢复后缺少最后成功时间，无法判断缓存新鲜度\n%s", recovered)
	}

	collector.Refresh(context.Background())
	stale := gatherCollector(t, collector)
	for _, sample := range []string{
		`danshi_moderation_review_queue_cached_items 17`,
		`danshi_moderation_review_queue_cache_ready 1`,
		`danshi_moderation_review_queue_last_refresh_success 0`,
		`danshi_moderation_review_queue_refresh_errors_total 2`,
	} {
		if !strings.Contains(stale, sample) {
			t.Errorf("失败后缓存语义缺少 %q\n%s", sample, stale)
		}
	}
}

func TestReviewQueueCollectorTimeoutReturnsWithoutBackgroundLeak(t *testing.T) {
	var active atomic.Int32
	var calls atomic.Int32
	collector := newReviewQueueCollector(func(ctx context.Context) (int64, error) {
		calls.Add(1)
		active.Add(1)
		defer active.Add(-1)
		<-ctx.Done()
		return 0, ctx.Err()
	})
	collector.refreshInterval = 0
	collector.refreshTimeout = 10 * time.Millisecond

	for range 5 {
		started := time.Now()
		collector.Refresh(context.Background())
		if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
			t.Fatalf("超时刷新耗时 %s，未被硬超时约束", elapsed)
		}
		if got := active.Load(); got != 0 {
			t.Fatalf("刷新返回后仍有 %d 个活跃查询", got)
		}
	}
	if got := calls.Load(); got != 5 {
		t.Fatalf("刷新调用次数 = %d，期望 5", got)
	}
	output := gatherCollector(t, collector)
	if !strings.Contains(output, `danshi_moderation_review_queue_refresh_errors_total 5`) {
		t.Fatalf("超时错误没有被累计\n%s", output)
	}
}

func TestMetricsEndpointKeepsServingWhenReviewQueueRefreshFails(t *testing.T) {
	m, err := NewMetrics(nil, WithReviewQueueCounter(func(context.Context) (int64, error) {
		return 0, errors.New("queue unavailable")
	}))
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	h := server.New(hertzconfig.Option{F: func(*hertzconfig.Options) {}})
	m.Register(h)
	response := ut.PerformRequest(h.Engine, http.MethodGet, "/metrics", nil).Result()
	if response.StatusCode() != http.StatusOK {
		t.Fatalf("队列刷新失败不应拖垮 /metrics，状态码=%d，body=%s",
			response.StatusCode(), response.Body())
	}
	body := string(response.Body())
	for _, sample := range []string{
		`danshi_moderation_review_queue_cache_ready 0`,
		`danshi_moderation_review_queue_last_refresh_success 0`,
		`danshi_moderation_review_queue_refresh_errors_total 1`,
	} {
		if !strings.Contains(body, sample) {
			t.Errorf("失败时 /metrics 缺少 %q\n%s", sample, body)
		}
	}
	if strings.Contains(body, "review_queue_cached_items") {
		t.Fatalf("无成功缓存时不应暴露虚假队列深度\n%s", body)
	}
}

func gatherCollector(t *testing.T, collector prometheus.Collector) string {
	t.Helper()
	registry := prometheus.NewRegistry()
	if err := registry.Register(collector); err != nil {
		t.Fatalf("Register: %v", err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return encodeMetricFamilies(t, families)
}

func gatherBusinessMetrics(t *testing.T, m *Metrics) string {
	t.Helper()
	families, err := m.registry.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return encodeMetricFamilies(t, families)
}

func encodeMetricFamilies(t *testing.T, families []*dto.MetricFamily) string {
	t.Helper()
	var output bytes.Buffer
	encoder := expfmt.NewEncoder(&output, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, family := range families {
		if err := encoder.Encode(family); err != nil {
			t.Fatalf("Encode: %v", err)
		}
	}
	return output.String()
}
