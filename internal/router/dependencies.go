package router

import (
	"time"

	"github.com/Foodan-Dev/danshi-backend/internal/infra/alerting"
	"github.com/Foodan-Dev/danshi-backend/internal/infra/localstorage"
	"github.com/Foodan-Dev/danshi-backend/internal/infra/tencentcloud"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

const (
	defaultMaxImageBytes = int64(10 * 1024 * 1024)
	defaultPresignTTL    = 10 * time.Minute
	defaultPresignGetTTL = time.Hour
)

func withDefaultDomainDeps(deps Deps) Deps {
	provider := configuredTencentProvider(deps)
	if deps.ContentModerator == nil {
		switch {
		case provider != nil && deps.Config.TencentCIConfigured():
			deps.ContentModerator = provider
		case deps.Config.IsProd():
			deps.ContentModerator = service.UnavailableContentModerator{}
		default:
			deps.ContentModerator = service.DirectPassContentModerator{}
		}
	}
	if deps.ImageStorage == nil {
		switch {
		case provider != nil && deps.Config.COSConfigured():
			deps.ImageStorage = provider
		case deps.Config.IsProd():
			deps.ImageStorage = service.UnavailableImageStorage{}
		default:
			deps.ImageStorage = localstorage.NewMemory()
		}
	}
	if deps.ImageModerator == nil {
		switch {
		case provider != nil && deps.Config.TencentCIConfigured():
			deps.ImageModerator = provider
		case deps.Config.IsProd():
			deps.ImageModerator = service.UnavailableImageModerator{}
		default:
			deps.ImageModerator = service.DirectPassImageModerator{}
		}
	}
	if deps.ImageCallbackDecoder == nil {
		deps.ImageCallbackDecoder = tencentcloud.CallbackDecoder{}
	}
	if deps.ModerationAlerter == nil {
		if deps.Config.FeishuModerationWebhook != "" {
			deps.ModerationAlerter = alerting.NewFeishuWebhook(
				deps.Config.FeishuModerationWebhook, nil, deps.Log,
			)
		} else {
			deps.ModerationAlerter = service.NewLogModerationAlerter(deps.Log)
		}
	}
	deps.ModerationAlerter = alerting.AfterCommit(deps.ModerationAlerter)
	deps.ContentModerator = observeContentModerator(deps.ContentModerator, deps.BusinessMetrics)
	deps.ImageModerator = observeImageModerator(deps.ImageModerator, deps.BusinessMetrics)
	return deps
}

func configuredTencentProvider(deps Deps) *tencentcloud.Provider {
	if !deps.Config.COSConfigured() {
		return nil
	}
	provider, err := tencentcloud.NewProvider(deps.Config, nil)
	if err != nil {
		if deps.Log != nil {
			deps.Log.Error("腾讯云供应商适配器初始化失败", "err", err)
		}
		return nil
	}
	return provider
}

func uploadLimits(deps Deps) (int64, time.Duration) {
	maxImageBytes := deps.Config.COSMaxImageBytes
	if maxImageBytes <= 0 {
		maxImageBytes = defaultMaxImageBytes
	}
	presignTTL := deps.Config.COSPresignTTL()
	if presignTTL <= 0 {
		presignTTL = defaultPresignTTL
	}
	return maxImageBytes, presignTTL
}

func signedImageTTL(deps Deps) time.Duration {
	ttl := deps.Config.COSPresignGetTTL()
	if ttl <= 0 {
		ttl = defaultPresignGetTTL
	}
	return ttl
}
