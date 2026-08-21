package model

import "time"

// Canteen 对应餐厅/食堂字典。
type Canteen struct {
	ID        uint64 `gorm:"primaryKey"`
	Code      string
	Name      string
	Campus    string
	SortOrder int32
	IsActive  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName 返回餐厅表名。
func (Canteen) TableName() string { return "canteens" }

// CanteenWindow 对应餐厅下的窗口字典。
type CanteenWindow struct {
	ID        uint64 `gorm:"primaryKey"`
	CanteenID uint64
	Name      string
	Floor     *string
	SortOrder int32
	IsActive  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName 返回餐厅窗口表名。
func (CanteenWindow) TableName() string { return "canteen_windows" }

// Cuisine 对应菜系字典。
type Cuisine struct {
	ID        uint64 `gorm:"primaryKey"`
	Name      string
	SortOrder int32
	IsActive  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName 返回菜系表名。
func (Cuisine) TableName() string { return "cuisines" }

// Flavor 对应口味字典。
type Flavor struct {
	ID        uint64 `gorm:"primaryKey"`
	Name      string
	SortOrder int32
	IsActive  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName 返回口味表名。
func (Flavor) TableName() string { return "flavors" }

// Tag 是用户自由创建、先发后审的话题标签。
type Tag struct {
	ID         uint64 `gorm:"primaryKey"`
	Name       string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Moderation ModerationStatus
	DeletedAt  *time.Time
}

// TableName 返回话题标签表名。
func (Tag) TableName() string { return "tags" }

// DictionarySuggestion 是用户提交并由管理员审核的封闭词表提议。
type DictionarySuggestion struct {
	ID                   uint64 `gorm:"primaryKey"`
	Kind                 SuggestionKind
	ProposedName         string
	ProposerID           uint64
	PostID               *uint64
	FlavorStance         *FlavorStance
	ParentCanteenID      *uint64
	ParentSuggestionID   *uint64
	ParentSuggestionKind *SuggestionKind `gorm:"->"`
	Status               SuggestionStatus
	ReviewerID           *uint64
	ReviewedAt           *time.Time
	ReviewNote           *string
	ResultingFlavorID    *uint64
	ResultingCuisineID   *uint64
	ResultingCanteenID   *uint64
	ResultingWindowID    *uint64
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// TableName 返回词条提议表名。
func (DictionarySuggestion) TableName() string { return "dictionary_suggestions" }
