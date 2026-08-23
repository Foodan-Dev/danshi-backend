package model

import "time"

// Comment 保存真实回复链，并通过 root_id 支撑按楼拍扁展示。
type Comment struct {
	ID                       uint64 `gorm:"primaryKey"`
	PostID                   uint64
	AuthorID                 uint64
	ParentID                 *uint64
	RootID                   *uint64
	ReplyToUserID            uint64
	Content                  string
	LikeCount                int32 `gorm:"->"`
	ReplyCount               int32 `gorm:"->"`
	DeletedAt                *time.Time
	DeletedReason            *DeleteReason
	DeletedBy                *uint64
	CreatedAt                time.Time
	UpdatedAt                time.Time
	EffectiveRootID          uint64  `gorm:"->"`
	RootPostIDForAuthorCheck *uint64 `gorm:"->"`
}

// TableName 返回评论表名。
func (Comment) TableName() string { return "comments" }

// CommentMention 是评论正文中被提及的用户。
type CommentMention struct {
	CommentID uint64 `gorm:"primaryKey;autoIncrement:false"`
	UserID    uint64 `gorm:"primaryKey;autoIncrement:false"`
}

// TableName 返回评论提及关联表名。
func (CommentMention) TableName() string { return "comment_mentions" }

// CommentLike 是用户对评论的点赞动作。
type CommentLike struct {
	UserID    uint64 `gorm:"primaryKey;autoIncrement:false"`
	CommentID uint64 `gorm:"primaryKey;autoIncrement:false"`
	CreatedAt time.Time
}

// TableName 返回评论点赞表名。
func (CommentLike) TableName() string { return "comment_likes" }

// CommentHistory 是评论被编辑替换掉的一版正文快照。
type CommentHistory struct {
	ID        uint64 `gorm:"primaryKey"`
	CommentID uint64
	Revision  int32
	EditedBy  uint64
	EditedAt  time.Time
	Content   string
}

// TableName 返回评论历史表名。
func (CommentHistory) TableName() string { return "comment_histories" }
