package router

import "github.com/Foodan-Dev/danshi-backend/internal/service"

func newModerationService(deps Deps) *service.ModerationService {
	return service.NewModerationService(
		deps.ModerationAlerter,
		service.NewDurableImageAccessController(),
	)
}
