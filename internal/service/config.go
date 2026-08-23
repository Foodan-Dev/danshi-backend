package service

import (
	"context"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

// PostTypeSubTypeConfig 描述分享帖的细分类。
type PostTypeSubTypeConfig struct {
	Value model.ShareType `json:"value"`
	Label string          `json:"label"`
}

// PostTypeConfig 描述帖子大类及前端字段提示。
type PostTypeConfig struct {
	Type              model.PostType          `json:"type"`
	Name              string                  `json:"name"`
	Description       string                  `json:"description"`
	SubTypes          []PostTypeSubTypeConfig `json:"sub_types"`
	RequiredFields    []string                `json:"required_fields"`
	RecommendedFields []string                `json:"recommended_fields"`
}

// CanteenWindowConfig 是餐厅下可选择的启用窗口。
type CanteenWindowConfig struct {
	ID       uint64  `json:"id"`
	Name     string  `json:"name"`
	Floor    *string `json:"floor"`
	IsActive bool    `json:"is_active"`
}

// CanteenConfig 是以稳定 code 为 id 的餐厅配置。
type CanteenConfig struct {
	ID       string                `json:"id"`
	Name     string                `json:"name"`
	Campus   string                `json:"campus"`
	IsActive bool                  `json:"is_active"`
	Windows  []CanteenWindowConfig `json:"windows"`
}

// ExploreConfig 是前端筛选与发帖选择器的唯一配置真源。
type ExploreConfig struct {
	PostTypes []PostTypeConfig `json:"post_types"`
	Canteens  []CanteenConfig  `json:"canteens"`
	Cuisines  []string         `json:"cuisines"`
	Flavors   []string         `json:"flavors"`
}

// ConfigService 返回不含停用项的公共配置。
type ConfigService struct {
	config repository.ConfigRepository
}

// NewConfigService 创建配置服务。
func NewConfigService() *ConfigService { return &ConfigService{} }

// Get 返回帖子类型常量与数据库启用词表。
func (s *ConfigService) Get(ctx context.Context) (*ExploreConfig, error) {
	dictionaries, err := s.config.FindActiveDictionaries(ctx)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	windows := make(map[uint64][]CanteenWindowConfig)
	for _, row := range dictionaries.Windows {
		windows[row.CanteenID] = append(windows[row.CanteenID], CanteenWindowConfig{
			ID: row.ID, Name: row.Name, Floor: row.Floor, IsActive: true,
		})
	}
	canteens := make([]CanteenConfig, 0, len(dictionaries.Canteens))
	for _, row := range dictionaries.Canteens {
		items := windows[row.ID]
		if items == nil {
			items = []CanteenWindowConfig{}
		}
		canteens = append(canteens, CanteenConfig{
			ID: row.Code, Name: row.Name, Campus: row.Campus, IsActive: true, Windows: items,
		})
	}
	cuisines := make([]string, 0, len(dictionaries.Cuisines))
	for _, row := range dictionaries.Cuisines {
		cuisines = append(cuisines, row.Name)
	}
	flavors := make([]string, 0, len(dictionaries.Flavors))
	for _, row := range dictionaries.Flavors {
		flavors = append(flavors, row.Name)
	}
	return &ExploreConfig{
		PostTypes: postTypeConfigs(), Canteens: canteens, Cuisines: cuisines, Flavors: flavors,
	}, nil
}

func postTypeConfigs() []PostTypeConfig {
	return []PostTypeConfig{
		{
			Type: model.PostTypeShare, Name: "美食分享/避雷",
			Description: "分享你的美食体验，为他人提供参考",
			SubTypes: []PostTypeSubTypeConfig{
				{Value: model.ShareTypeRecommend, Label: "推荐"},
				{Value: model.ShareTypeWarning, Label: "避雷"},
			},
			RequiredFields: []string{"title", "content", "images", "category"},
			RecommendedFields: []string{
				"canteen", "cuisine", "flavors", "price", "tags",
			},
		},
		{
			Type: model.PostTypeSeeking, Name: "求美食推荐",
			Description:    "寻求他人的美食推荐和建议",
			SubTypes:       []PostTypeSubTypeConfig{},
			RequiredFields: []string{"title", "content", "category"},
			RecommendedFields: []string{
				"canteen", "cuisine", "budget_range", "preferences", "tags",
			},
		},
	}
}
