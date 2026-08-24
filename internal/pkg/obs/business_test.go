package obs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

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
		`danshi_moderation_review_queue_items 7`,
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

func TestReviewQueueCollectorPropagatesCollectionFailure(t *testing.T) {
	m, err := NewMetrics(nil, WithReviewQueueCounter(func(context.Context) (int64, error) {
		return 0, errors.New("queue unavailable")
	}))
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	if _, err := m.registry.Gather(); err == nil || !strings.Contains(err.Error(), "queue unavailable") {
		t.Fatalf("队列 collector 应显式暴露失败，实际 err=%v", err)
	}
}

func gatherBusinessMetrics(t *testing.T, m *Metrics) string {
	t.Helper()
	families, err := m.registry.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var output bytes.Buffer
	encoder := expfmt.NewEncoder(&output, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, family := range families {
		if err := encoder.Encode(family); err != nil {
			t.Fatalf("Encode: %v", err)
		}
	}
	return output.String()
}
