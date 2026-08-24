package router

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

func TestObservedModeratorsUseBoundedBusinessSemantics(t *testing.T) {
	recorder := &businessMetricCapture{}
	content := observeContentModerator(fixedContentModerator{
		result: service.ModerationResult{
			Provider: model.ModerationProviderTencentCI,
			Verdict:  model.ModerationVerdictReview,
			Labels:   []string{"provider_failed"},
		},
	}, recorder)
	if _, err := content.Review(context.Background(), service.ModerationRequest{
		Target: service.ModerationTargetPost, Text: "sensitive body not forwarded to metrics",
	}); err != nil {
		t.Fatalf("Review: %v", err)
	}
	recorder.requireEvent(t, "submission", "tencent_ci", "post", "")
	recorder.requireEvent(t, "terminal", "tencent_ci", "post", "provider_failed")

	image := observeImageModerator(failingImageModerator{}, recorder)
	if _, err := image.SubmitImage(context.Background(), service.ImageModerationRequest{
		ImageAssetID: 919, ObjectKey: "private/object/key.jpg",
	}); err == nil {
		t.Fatal("SubmitImage 应返回供应商错误")
	}
	recorder.requireEvent(t, "submission", "unknown", "image_asset", "")
	recorder.requireEvent(t, "provider_failure", "unknown", "image_asset", "provider_error")
}

func TestObservedVerificationSenderClassifiesSuccessAndFailure(t *testing.T) {
	recorder := &businessMetricCapture{}
	success := observeVerificationSender(fixedVerificationSender{}, "tencent_ses", recorder)
	if err := success.SendRegistrationCode(
		context.Background(), "private@fdueat.com", "123456",
	); err != nil {
		t.Fatalf("成功 sender: %v", err)
	}
	recorder.requireEvent(t, "verification", "tencent_ses", "send", "none")

	failure := observeVerificationSender(
		fixedVerificationSender{err: errors.New("provider response with private text")},
		"tencent_ses",
		recorder,
	)
	if err := failure.SendRegistrationCode(
		context.Background(), "private@fdueat.com", "123456",
	); err == nil {
		t.Fatal("失败 sender 应返回错误")
	}
	recorder.requireEvent(t, "verification", "tencent_ses", "provider_failure", "provider_error")
}

type fixedContentModerator struct {
	result service.ModerationResult
	err    error
}

func (m fixedContentModerator) Review(
	context.Context,
	service.ModerationRequest,
) (service.ModerationResult, error) {
	return m.result, m.err
}

type failingImageModerator struct{}

func (failingImageModerator) SubmitImage(
	context.Context,
	service.ImageModerationRequest,
) (service.ImageModerationSubmission, error) {
	return service.ImageModerationSubmission{}, errors.New("provider failure")
}

type fixedVerificationSender struct{ err error }

func (s fixedVerificationSender) SendRegistrationCode(context.Context, string, string) error {
	return s.err
}

type businessMetricEvent struct {
	kind     string
	provider string
	scene    string
	value    string
}

type businessMetricCapture struct {
	mu     sync.Mutex
	events []businessMetricEvent
}

func (c *businessMetricCapture) add(event businessMetricEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
}

func (c *businessMetricCapture) RecordModerationSubmission(provider, scene string) {
	c.add(businessMetricEvent{kind: "submission", provider: provider, scene: scene})
}

func (c *businessMetricCapture) RecordModerationProviderFailure(provider, scene, reason string) {
	c.add(businessMetricEvent{
		kind: "provider_failure", provider: provider, scene: scene, value: reason,
	})
}

func (c *businessMetricCapture) RecordModerationTerminal(
	_ context.Context,
	provider string,
	scene string,
	outcome string,
) {
	c.add(businessMetricEvent{kind: "terminal", provider: provider, scene: scene, value: outcome})
}

func (c *businessMetricCapture) RecordModerationCallback(
	_ context.Context,
	provider string,
	outcome string,
	reason string,
) {
	c.add(businessMetricEvent{
		kind: "callback", provider: provider, scene: outcome, value: reason,
	})
}

func (c *businessMetricCapture) RecordVerification(
	_ context.Context,
	provider string,
	outcome string,
	reason string,
) {
	c.add(businessMetricEvent{
		kind: "verification", provider: provider, scene: outcome, value: reason,
	})
}

func (c *businessMetricCapture) requireEvent(
	t *testing.T,
	kind string,
	provider string,
	scene string,
	value string,
) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, event := range c.events {
		if event.kind == kind && event.provider == provider && event.scene == scene && event.value == value {
			return
		}
	}
	t.Fatalf("缺少事件 kind=%s provider=%s scene=%s value=%s；实际=%+v",
		kind, provider, scene, value, c.events)
}
