package router

import (
	"context"
	"errors"
	"time"

	"github.com/Foodan-Dev/danshi-backend/internal/repository"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

type transactionalImageAccessController struct {
	outbox repository.ImageAccessOutboxRepository
	now    func() time.Time
}

func (a transactionalImageAccessController) Apply(
	ctx context.Context,
	change service.ImageAccessChange,
) error {
	if change.ImageAssetID == 0 || change.SourceModerationRecordID == 0 {
		return errors.New("image access intent requires asset and moderation record")
	}
	now := time.Now().UTC()
	if a.now != nil {
		now = a.now().UTC()
	}
	_, err := a.outbox.Enqueue(
		ctx, change.ImageAssetID, change.SourceModerationRecordID, change.Public,
		change.PurgeRequired, now,
	)
	return err
}

func newModerationService(deps Deps) *service.ModerationService {
	return service.NewModerationService(deps.ModerationAlerter, transactionalImageAccessController{})
}

var _ service.ImageAccessController = transactionalImageAccessController{}
