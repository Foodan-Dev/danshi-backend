package router

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jingyijun/danshi_backend_go/internal/config"
	"github.com/jingyijun/danshi_backend_go/internal/infra/localstorage"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

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
