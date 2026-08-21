package model

import "time"

// ImageAsset 是上传到对象存储的图片及其引用、审核状态。
type ImageAsset struct {
	ID          uint64 `gorm:"primaryKey"`
	UploaderID  *uint64
	Purpose     ImagePurpose
	ObjectKey   string
	PublicURL   string
	ContentType string
	Size        *int64
	Status      ImageStatus
	Moderation  ModerationStatus
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TableName 返回图片资产表名。
func (ImageAsset) TableName() string { return "image_assets" }
