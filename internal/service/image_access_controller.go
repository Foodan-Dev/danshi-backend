package service

import (
	"context"
	"errors"
	"time"

	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

// DurableImageAccessController 把审核终态事务性写入图片访问 outbox。
type DurableImageAccessController struct {
	outbox repository.ImageAccessOutboxRepository
	now    func() time.Time
}

// NewDurableImageAccessController 创建使用真实持久 outbox 的访问控制器。
func NewDurableImageAccessController() DurableImageAccessController {
	return DurableImageAccessController{}
}

// Apply 在当前事务内追加访问意图。
func (a DurableImageAccessController) Apply(ctx context.Context, change ImageAccessChange) error {
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

var _ ImageAccessController = DurableImageAccessController{}
