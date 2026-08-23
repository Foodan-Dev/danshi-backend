package router

import (
	"context"
	"log/slog"

	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

type afterCommitImageAccessController struct {
	storage service.ImageStorage
	purger  service.ImageCachePurger
	log     *slog.Logger
}

func (a afterCommitImageAccessController) Apply(
	ctx context.Context,
	change service.ImageAccessChange,
) {
	callback := func(callbackCtx context.Context) {
		if err := a.storage.SetObjectPublicAccess(callbackCtx, change.ObjectKey, change.Public); err != nil {
			a.logFailure(callbackCtx, "更新审核图片对象 ACL 失败", change, err)
		}
		if err := a.purger.PurgeURL(callbackCtx, change.PublicURL); err != nil {
			a.logFailure(callbackCtx, "刷新审核图片 CDN 缓存失败", change, err)
		}
	}
	if db.AfterCommit(ctx, callback) {
		return
	}
	callback(ctx)
}

func (a afterCommitImageAccessController) logFailure(
	ctx context.Context,
	message string,
	change service.ImageAccessChange,
	err error,
) {
	if a.log == nil {
		return
	}
	a.log.ErrorContext(ctx, message,
		slog.Uint64("image_asset_id", change.ImageAssetID),
		slog.String("object_key", change.ObjectKey),
		slog.Bool("public", change.Public),
		slog.Any("err", err),
	)
}

func newModerationService(deps Deps) *service.ModerationService {
	return service.NewModerationService(deps.ModerationAlerter, afterCommitImageAccessController{
		storage: deps.ImageStorage,
		purger:  deps.ImageCachePurger,
		log:     deps.Log,
	})
}

var _ service.ImageAccessController = afterCommitImageAccessController{}
