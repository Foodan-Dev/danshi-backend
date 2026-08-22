package repository

import (
	"context"
	"time"

	"gorm.io/gorm/clause"

	"github.com/jingyijun/danshi_backend_go/internal/infra/db"
	"github.com/jingyijun/danshi_backend_go/internal/model"
)

// UploadRepository 是上传资产生命周期的持久化边界。
type UploadRepository struct{}

// Create 创建尚未完成直传的资产行。
func (UploadRepository) Create(ctx context.Context, asset *model.ImageAsset) error {
	return db.FromContext(ctx).Create(asset).Error
}

// LockByID 为 complete 加行锁，与过期回收互斥。
func (UploadRepository) LockByID(ctx context.Context, imageAssetID uint64) (*model.ImageAsset, error) {
	var asset model.ImageAsset
	err := db.FromContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", imageAssetID).First(&asset).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &asset, nil
}

// MarkComplete 写入首次对外暴露的公开 URL，并把资产置为 ready。
func (UploadRepository) MarkComplete(
	ctx context.Context,
	imageAssetID uint64,
	publicURL string,
	size int64,
) error {
	result := db.FromContext(ctx).Model(&model.ImageAsset{}).Where("id = ?", imageAssetID).
		Updates(map[string]any{
			"public_url": publicURL, "size": size, "status": model.ImageStatusReady,
			"updated_at": time.Now().UTC(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// LockExpiredPending 用 SKIP LOCKED 领取一批过期上传，不等待正在 complete 的行。
func (UploadRepository) LockExpiredPending(
	ctx context.Context,
	before time.Time,
	limit int,
) ([]model.ImageAsset, error) {
	assets := make([]model.ImageAsset, 0, limit)
	err := db.FromContext(ctx).Raw(`
		SELECT * FROM image_assets
		WHERE status = ? AND created_at < ?
		ORDER BY created_at, id
		FOR UPDATE SKIP LOCKED
		LIMIT ?
	`, model.ImageStatusPending, before, limit).Scan(&assets).Error
	return assets, err
}

// Retire 把已回收对象的资产标为 retired。
func (UploadRepository) Retire(ctx context.Context, imageAssetID uint64) error {
	return db.FromContext(ctx).Model(&model.ImageAsset{}).Where("id = ?", imageAssetID).
		Updates(map[string]any{"status": model.ImageStatusRetired, "updated_at": time.Now().UTC()}).Error
}
