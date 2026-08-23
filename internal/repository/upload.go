package repository

import (
	"context"
	"time"

	"gorm.io/gorm/clause"

	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
)

// UploadRepository 是上传资产生命周期的持久化边界。
type UploadRepository struct{}

// FindByID 返回单张图片资产，不锁行。
func (UploadRepository) FindByID(ctx context.Context, imageAssetID uint64) (*model.ImageAsset, error) {
	var asset model.ImageAsset
	err := db.FromContext(ctx).Where("id = ?", imageAssetID).First(&asset).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &asset, nil
}

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

// MarkComplete 写入首次对外暴露的公开 URL；资产在建立真实引用前保持 pending。
func (UploadRepository) MarkComplete(
	ctx context.Context,
	imageAssetID uint64,
	publicURL string,
	size int64,
) error {
	result := db.FromContext(ctx).Model(&model.ImageAsset{}).Where("id = ?", imageAssetID).
		Updates(map[string]any{
			"public_url": publicURL, "size": size, "updated_at": time.Now().UTC(),
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
		SELECT a.* FROM image_assets AS a
		WHERE a.status = ? AND a.created_at < ?
		  AND NOT EXISTS (
		    SELECT 1 FROM post_images AS pi WHERE pi.image_asset_id = a.id
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM users AS u WHERE u.avatar_image_asset_id = a.id
		  )
		ORDER BY a.created_at, a.id
		FOR UPDATE SKIP LOCKED
		LIMIT ?
	`, model.ImageStatusPending, before, limit).Scan(&assets).Error
	return assets, err
}

// RetirePurged 把对象已删除的无引用资产标为 retired，并用唯一墓碑替换失效公开 URL。
func (UploadRepository) RetirePurged(ctx context.Context, imageAssetID uint64) error {
	result := db.FromContext(ctx).Model(&model.ImageAsset{}).
		Where(`id = ? AND status = ?
			AND NOT EXISTS (
			  SELECT 1 FROM post_images AS pi WHERE pi.image_asset_id = image_assets.id
			)
			AND NOT EXISTS (
			  SELECT 1 FROM users AS u WHERE u.avatar_image_asset_id = image_assets.id
			)`, imageAssetID, model.ImageStatusPending).
		Updates(map[string]any{
			"public_url": model.PurgedImageURL(imageAssetID),
			"status":     model.ImageStatusRetired,
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
