package router

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/config"
	dbinfra "github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/infra/localstorage"
	"github.com/Foodan-Dev/danshi-backend/internal/infra/tencentcloud"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

type captureModerationAlerter struct {
	alerts []service.ModerationAlert
}

func (a *captureModerationAlerter) Alert(_ context.Context, alert service.ModerationAlert) {
	a.alerts = append(a.alerts, alert)
}

func TestDefaultModerationAndStorageDependencies(t *testing.T) {
	t.Run("production fails closed when providers are missing", func(t *testing.T) {
		deps := withDefaultDomainDeps(Deps{Config: config.Config{Profile: config.ProfileProd}})
		require.IsType(t, service.UnavailableContentModerator{}, deps.ContentModerator)
		require.IsType(t, service.UnavailableImageStorage{}, deps.ImageStorage)
		require.IsType(t, service.UnavailableImageModerator{}, deps.ImageModerator)
		require.NotNil(t, deps.ImageCallbackDecoder)
	})

	t.Run("development uses replaceable local adapters", func(t *testing.T) {
		deps := withDefaultDomainDeps(Deps{Config: config.Config{Profile: config.ProfileDev}})
		require.IsType(t, service.DirectPassContentModerator{}, deps.ContentModerator)
		require.IsType(t, &localstorage.Memory{}, deps.ImageStorage)
		require.IsType(t, service.DirectPassImageModerator{}, deps.ImageModerator)
		require.NotNil(t, deps.ModerationAlerter)
	})
}

func TestModerationAlertsRunAfterCommit(t *testing.T) {
	capture := &captureModerationAlerter{}
	deps := withDefaultDomainDeps(Deps{
		Config: config.Config{Profile: config.ProfileDev}, ModerationAlerter: capture,
	})
	ctx, queue := dbinfra.WithAfterCommitQueue(context.Background())
	jobID := "job-1"
	alert := service.ModerationAlert{
		Target: service.ModerationTargetPost, TargetID: 42,
		Provider: model.ModerationProvider("test"), ProviderJobID: &jobID,
		Verdict: model.ModerationVerdictReview, Labels: []string{"review"},
	}
	deps.ModerationAlerter.Alert(ctx, alert)
	alert.Labels[0] = "mutated"
	jobID = "mutated"
	require.Empty(t, capture.alerts, "事务提交前不得发送审核告警")

	require.Empty(t, queue.Run(ctx))
	require.Len(t, capture.alerts, 1)
	require.Equal(t, []string{"review"}, capture.alerts[0].Labels)
	require.Equal(t, "job-1", *capture.alerts[0].ProviderJobID)

	deps.ModerationAlerter.Alert(context.Background(), alert)
	require.Len(t, capture.alerts, 2, "非事务调用仍须发送告警")
}

func TestVerificationEmailSenderSelection(t *testing.T) {
	t.Run("development complete config uses real adapter", func(t *testing.T) {
		assertVerificationSenderChoice(
			t,
			completeSESConfig(config.ProfileDev),
			&tencentcloud.SESVerificationEmailSender{},
			"tencent_ses",
		)
	})

	t.Run("development missing config logs", func(t *testing.T) {
		assertVerificationSenderChoice(
			t,
			config.Config{Profile: config.ProfileDev},
			&service.LogVerificationEmailSender{},
			"log",
		)
	})

	t.Run("production missing config fails closed", func(t *testing.T) {
		assertVerificationSenderChoice(
			t,
			config.Config{Profile: config.ProfileProd},
			service.UnavailableVerificationEmailSender{},
			"none",
		)
	})

	t.Run("production complete config uses real adapter", func(t *testing.T) {
		assertVerificationSenderChoice(
			t,
			completeSESConfig(config.ProfileProd),
			&tencentcloud.SESVerificationEmailSender{},
			"tencent_ses",
		)
	})

	t.Run("injected fake wins", func(t *testing.T) {
		injected := service.NewLogVerificationEmailSender(nil)
		sender := verificationEmailSender(Deps{
			Config: config.Config{Profile: config.ProfileProd}, EmailSender: injected,
		})
		require.Same(t, injected, sender)
	})
}

func completeSESConfig(profile config.Profile) config.Config {
	return config.Config{
		Profile:         profile,
		TencentSecretID: "secret-id", TencentSecretKey: "secret-key",
		TencentRegion: "ap-guangzhou", TencentSESFromEmail: "sender@example.com",
		TencentSESFromName: "旦食", TencentSESSubject: "旦食注册验证码",
		TencentSESTemplateID: 123,
	}
}

func assertVerificationSenderChoice(
	t *testing.T,
	cfg config.Config,
	wantSender service.VerificationEmailSender,
	wantProvider string,
) {
	t.Helper()
	sender := verificationEmailSender(Deps{
		Config: cfg, BusinessMetrics: &businessMetricCapture{},
	})
	observed, ok := sender.(observedVerificationSender)
	require.True(t, ok)
	require.IsType(t, wantSender, observed.next)
	require.Equal(t, wantProvider, observed.provider)
}
