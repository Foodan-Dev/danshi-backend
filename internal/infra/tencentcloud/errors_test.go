package tencentcloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

func TestProviderFailureRedactsKnownSecretsAndPreservesChain(t *testing.T) {
	cfg := providerTestConfig()
	original := fmt.Errorf(
		"GET https://img.example.test/private.jpg?callback=token=%s&secret_key=%s",
		cfg.ModerationCallbackToken,
		cfg.TencentSecretKey,
	)
	provider, err := NewProvider(cfg, &http.Client{
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, original
		}),
	})
	require.NoError(t, err)

	_, err = provider.SubmitImage(context.Background(), service.ImageModerationRequest{
		ImageAssetID: 1,
		ObjectKey:    "posts/1/private.jpg",
	})

	require.Error(t, err)
	require.NotContains(t, err.Error(), cfg.ModerationCallbackToken)
	require.NotContains(t, err.Error(), cfg.TencentSecretKey)
	require.Contains(t, err.Error(), redactedSecret)
	require.ErrorIs(t, err, original)
	failure := apierr.As(err)
	require.NotContains(t, failure.Error(), cfg.ModerationCallbackToken)
	require.NotContains(t, failure.Error(), cfg.TencentSecretKey)
	require.NotContains(t, failure.Cause().Error(), cfg.ModerationCallbackToken)
	require.NotContains(t, failure.Cause().Error(), cfg.TencentSecretKey)
}

func TestKnownSecretRedactorSkipsEmptySecrets(t *testing.T) {
	original := errors.New("provider unavailable")

	redacted := newSecretRedactor("").redact(original)

	require.Same(t, original, redacted)
}
