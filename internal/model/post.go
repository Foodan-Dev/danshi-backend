package model

import (
	"encoding/json"
	"time"

	"github.com/shopspring/decimal"
)

// Post 是分享帖或提问帖主体。
type Post struct {
	ID              uint64 `gorm:"primaryKey"`
	AuthorID        uint64
	PostType        PostType
	ShareType       *ShareType
	Status          PostStatus
	Category        PostCategory
	Title           string
	Content         string
	CurrentRevision int32 `gorm:"default:1"`
	CanteenID       *uint64
	CanteenWindowID *uint64
	CuisineID       *uint64
	Price           *decimal.Decimal `gorm:"type:numeric(10,2)"`
	BudgetMin       *int32
	BudgetMax       *int32
	LikeCount       int32 `gorm:"->"`
	FavoriteCount   int32 `gorm:"->"`
	CommentCount    int32 `gorm:"->"`
	ViewCount       int32 `gorm:"->"`
	DeletedAt       *time.Time
	DeletedReason   *DeleteReason
	DeletedBy       *uint64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// TableName 返回帖子表名。
func (Post) TableName() string { return "posts" }

// PostTag 是帖子与自由标签的多对多关联行。
type PostTag struct {
	PostID uint64 `gorm:"primaryKey;autoIncrement:false"`
	TagID  uint64 `gorm:"primaryKey;autoIncrement:false"`
}

// TableName 返回帖子标签关联表名。
func (PostTag) TableName() string { return "post_tags" }

// PostFlavor 是帖子与口味的关联及其语义立场。
type PostFlavor struct {
	PostID   uint64 `gorm:"primaryKey;autoIncrement:false"`
	FlavorID uint64 `gorm:"primaryKey;autoIncrement:false"`
	Stance   FlavorStance
	PostType PostType
}

// TableName 返回帖子口味关联表名。
func (PostFlavor) TableName() string { return "post_flavors" }

// PostImage 是帖子图片及其展示位置。
type PostImage struct {
	PostID       uint64 `gorm:"primaryKey;autoIncrement:false"`
	Position     int16  `gorm:"primaryKey;autoIncrement:false"`
	ImageAssetID uint64
	CreatedAt    time.Time
}

// TableName 返回帖子图片关联表名。
func (PostImage) TableName() string { return "post_images" }

// Favorite 是用户的私有帖子收藏。
type Favorite struct {
	UserID    uint64 `gorm:"primaryKey;autoIncrement:false"`
	PostID    uint64 `gorm:"primaryKey;autoIncrement:false"`
	CreatedAt time.Time
}

// TableName 返回帖子收藏表名。
func (Favorite) TableName() string { return "favorites" }

// PostLike 是用户对帖子的点赞动作。
type PostLike struct {
	UserID    uint64 `gorm:"primaryKey;autoIncrement:false"`
	PostID    uint64 `gorm:"primaryKey;autoIncrement:false"`
	CreatedAt time.Time
}

// TableName 返回帖子点赞表名。
func (PostLike) TableName() string { return "post_likes" }

// PostHistory 是帖子的一版不可变完整内容快照，包括当前版本。
type PostHistory struct {
	ID         uint64 `gorm:"primaryKey"`
	PostID     uint64
	Revision   int32
	EditedBy   uint64
	EditedAt   time.Time
	Snapshot   json.RawMessage `gorm:"type:jsonb"`
	EditReason *string
}

// TableName 返回帖子历史表名。
func (PostHistory) TableName() string { return "post_histories" }
