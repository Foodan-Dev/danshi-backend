package model

import (
	"fmt"
	"strings"
	"time"
)

const purgedImageURLPrefix = "urn:danshi:image-asset:"

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

// PurgedImageURL 为对象已删除的资产生成唯一内部墓碑，防止失效公开 URL 再次被引用。
func PurgedImageURL(imageAssetID uint64) string {
	return fmt.Sprintf("%s%d:retired", purgedImageURLPrefix, imageAssetID)
}

// IsPurgedImageURL 报告 URL 是否为对象已经删除的内部墓碑。
func IsPurgedImageURL(value string) bool {
	return strings.HasPrefix(value, purgedImageURLPrefix) && strings.HasSuffix(value, ":retired")
}
