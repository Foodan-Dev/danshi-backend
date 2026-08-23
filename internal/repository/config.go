package repository

import (
	"context"

	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
)

// ActiveConfigDictionaries 是 /config 所需的全部启用词表行。
type ActiveConfigDictionaries struct {
	Canteens []model.Canteen
	Windows  []model.CanteenWindow
	Cuisines []model.Cuisine
	Flavors  []model.Flavor
}

// ConfigRepository 是公开配置的只读仓储。
type ConfigRepository struct{}

// FindActiveDictionaries 按稳定排序读取全部启用词表。
func (ConfigRepository) FindActiveDictionaries(ctx context.Context) (ActiveConfigDictionaries, error) {
	result := ActiveConfigDictionaries{
		Canteens: []model.Canteen{}, Windows: []model.CanteenWindow{},
		Cuisines: []model.Cuisine{}, Flavors: []model.Flavor{},
	}
	tx := db.FromContext(ctx)
	if err := tx.Where("is_active").Order("sort_order, id").Find(&result.Canteens).Error; err != nil {
		return result, err
	}
	if err := tx.Where("is_active").Order("canteen_id, sort_order, id").Find(&result.Windows).Error; err != nil {
		return result, err
	}
	if err := tx.Where("is_active").Order("sort_order, id").Find(&result.Cuisines).Error; err != nil {
		return result, err
	}
	if err := tx.Where("is_active").Order("sort_order, id").Find(&result.Flavors).Error; err != nil {
		return result, err
	}
	return result, nil
}
