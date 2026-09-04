package router

import (
	"context"

	"github.com/Foodan-Dev/danshi-backend/internal/infra/tencentcloud"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/obs"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

type observedContentModerator struct {
	next     service.ContentModerator
	metrics  obs.BusinessRecorder
	provider string
}

func observeContentModerator(
	next service.ContentModerator,
	metrics obs.BusinessRecorder,
) service.ContentModerator {
	if metrics == nil {
		return next
	}
	return observedContentModerator{
		next: next, metrics: metrics, provider: moderationProvider(next),
	}
}

func (m observedContentModerator) Review(
	ctx context.Context,
	request service.ModerationRequest,
) (service.ModerationResult, error) {
	result, err := m.next.Review(ctx, request)
	provider := m.provider
	if result.Provider != "" {
		provider = string(result.Provider)
	}
	m.metrics.RecordModerationSubmission(provider, string(request.Target))
	if err != nil {
		m.metrics.RecordModerationProviderFailure(
			provider, string(request.Target), "provider_error",
		)
		return result, err
	}
	m.metrics.RecordModerationTerminal(
		ctx, provider, string(request.Target), moderationOutcome(result.Verdict, result.Labels),
	)
	return result, nil
}

type observedImageModerator struct {
	next     service.ImageModerator
	metrics  obs.BusinessRecorder
	provider string
}

func observeImageModerator(
	next service.ImageModerator,
	metrics obs.BusinessRecorder,
) service.ImageModerator {
	if metrics == nil {
		return next
	}
	return observedImageModerator{
		next: next, metrics: metrics, provider: moderationProvider(next),
	}
}

func (m observedImageModerator) SubmitImage(
	ctx context.Context,
	request service.ImageModerationRequest,
) (service.ImageModerationSubmission, error) {
	result, err := m.next.SubmitImage(ctx, request)
	provider := m.provider
	if result.Provider != "" {
		provider = string(result.Provider)
	}
	m.metrics.RecordModerationSubmission(provider, string(service.ModerationTargetImage))
	if err != nil {
		m.metrics.RecordModerationProviderFailure(
			provider, string(service.ModerationTargetImage), "provider_error",
		)
		return result, err
	}
	if result.Immediate != nil {
		m.metrics.RecordModerationTerminal(
			ctx,
			provider,
			string(service.ModerationTargetImage),
			moderationOutcome(result.Immediate.Verdict, result.Immediate.Labels),
		)
	}
	return result, nil
}

type observedVerificationSender struct {
	next     service.VerificationEmailSender
	metrics  obs.BusinessRecorder
	provider string
}

func observeVerificationSender(
	next service.VerificationEmailSender,
	provider string,
	metrics obs.BusinessRecorder,
) service.VerificationEmailSender {
	if metrics == nil {
		return next
	}
	return observedVerificationSender{next: next, metrics: metrics, provider: provider}
}

func (s observedVerificationSender) SendRegistrationCode(
	ctx context.Context,
	email string,
	code string,
) error {
	err := s.next.SendRegistrationCode(ctx, email, code)
	if err != nil {
		s.metrics.RecordVerification(ctx, s.provider, "provider_failure", "provider_error")
		return err
	}
	s.metrics.RecordVerification(ctx, s.provider, "send", "none")
	return nil
}

func (s observedVerificationSender) SendPasswordResetCode(ctx context.Context, email, code string) error {
	err := s.next.SendPasswordResetCode(ctx, email, code)
	if err != nil {
		s.metrics.RecordVerification(ctx, s.provider, "provider_failure", "provider_error")
		return err
	}
	s.metrics.RecordVerification(ctx, s.provider, "send", "none")
	return nil
}

// Configured 透传底层投递器的可用性，避免 fail-closed 适配器被观测包装层遮蔽。
func (s observedVerificationSender) Configured() bool {
	available, ok := s.next.(interface{ Configured() bool })
	return !ok || available.Configured()
}

func moderationProvider(value any) string {
	switch value.(type) {
	case *tencentcloud.Provider:
		return "tencent_ci"
	case service.DirectPassContentModerator, service.DirectPassImageModerator:
		return "dev_allow"
	default:
		return "unknown"
	}
}

func moderationOutcome(verdict model.ModerationVerdict, labels []string) string {
	for _, label := range labels {
		if label == "provider_failed" {
			return "provider_failed"
		}
	}
	return string(verdict)
}

var (
	_ service.ContentModerator        = observedContentModerator{}
	_ service.ImageModerator          = observedImageModerator{}
	_ service.VerificationEmailSender = observedVerificationSender{}
)
