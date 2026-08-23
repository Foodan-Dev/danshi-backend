package router_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

func TestConfigDomainAgainstPostgres(t *testing.T) {
	gdb, database := openAuthPostgres(t)
	engine := authTestEngine(authTestConfig(), database, newCaptureEmailSender())
	activeCanteen, activeWindow := createConfigFixtures(t, gdb)

	t.Run("config route inventory", func(t *testing.T) {
		testConfigRouteInventory(t, engine)
	})

	t.Run("only active dictionaries are exposed", func(t *testing.T) {
		status, response, _ := performJSON(t, engine, http.MethodGet, "/api/v2/config", nil, "")
		require.Equal(t, http.StatusOK, status, "配置端点应允许匿名读取")
		var result service.ExploreConfig
		decodeData(t, response, &result)
		require.Len(t, result.PostTypes, 2)
		require.Equal(t, model.PostTypeShare, result.PostTypes[0].Type)
		require.Equal(t, model.PostTypeSeeking, result.PostTypes[1].Type)
		require.NotEmpty(t, result.Cuisines)
		require.NotEmpty(t, result.Flavors)
		require.GreaterOrEqual(t, len(result.Canteens), 2)
		require.Equal(t, "config-tie-a", result.Canteens[0].ID)
		require.Equal(t, "config-tie-b", result.Canteens[1].ID,
			"相同 sort_order 必须以 id 稳定排序")
		require.Equal(t, []string{"配置排序菜系 A", "配置排序菜系 B"}, result.Cuisines[:2])
		require.Equal(t, []string{"配置排序口味 A", "配置排序口味 B"}, result.Flavors[:2])
		require.Contains(t, result.Cuisines, "配置启用菜系")
		require.NotContains(t, result.Cuisines, "配置停用菜系")
		require.Contains(t, result.Flavors, "配置启用口味")
		require.NotContains(t, result.Flavors, "配置停用口味")

		var found *service.CanteenConfig
		for index := range result.Canteens {
			item := &result.Canteens[index]
			require.NotEqual(t, "config-inactive", item.ID)
			for _, window := range item.Windows {
				require.NotEqual(t, "父餐厅停用窗口", window.Name,
					"父餐厅停用时不能泄漏仍为 active 的子窗口")
			}
			if item.ID == activeCanteen.Code {
				found = item
			}
		}
		require.NotNil(t, found)
		require.True(t, found.IsActive)
		require.Len(t, found.Windows, 1)
		require.Equal(t, activeWindow.ID, found.Windows[0].ID)
		require.True(t, found.Windows[0].IsActive)
	})
}

func testConfigRouteInventory(t *testing.T, engine *server.Hertz) {
	t.Helper()
	operations := make([]string, 0)
	for _, route := range engine.Routes() {
		if strings.HasPrefix(route.Path, "/api/v2/config") {
			operations = append(operations, route.Method+" "+route.Path)
		}
	}
	require.ElementsMatch(t, []string{"GET /api/v2/config"}, operations)
}

func createConfigFixtures(t *testing.T, gdb *gorm.DB) (model.Canteen, model.CanteenWindow) {
	t.Helper()
	activeCanteen := model.Canteen{
		Code: "config-active", Name: "配置启用餐厅", Campus: "测试校区",
		SortOrder: -100, IsActive: true,
	}
	inactiveCanteen := model.Canteen{
		Code: "config-inactive", Name: "配置停用餐厅", Campus: "测试校区",
		SortOrder: -99, IsActive: false,
	}
	require.NoError(t, gdb.Create(&activeCanteen).Error)
	require.NoError(t, gdb.Create(&inactiveCanteen).Error)
	for _, canteen := range []model.Canteen{
		{Code: "config-tie-a", Name: "配置排序餐厅 A", Campus: "测试校区", SortOrder: -200, IsActive: true},
		{Code: "config-tie-b", Name: "配置排序餐厅 B", Campus: "测试校区", SortOrder: -200, IsActive: true},
	} {
		require.NoError(t, gdb.Create(&canteen).Error)
	}
	floor := "1F"
	activeWindow := model.CanteenWindow{
		CanteenID: activeCanteen.ID, Name: "配置启用窗口", Floor: &floor,
		SortOrder: -100, IsActive: true,
	}
	inactiveWindow := model.CanteenWindow{
		CanteenID: activeCanteen.ID, Name: "配置停用窗口", Floor: &floor,
		SortOrder: -99, IsActive: false,
	}
	orphanedByInactiveParent := model.CanteenWindow{
		CanteenID: inactiveCanteen.ID, Name: "父餐厅停用窗口", Floor: &floor,
		SortOrder: -98, IsActive: true,
	}
	require.NoError(t, gdb.Create(&activeWindow).Error)
	require.NoError(t, gdb.Create(&inactiveWindow).Error)
	require.NoError(t, gdb.Create(&orphanedByInactiveParent).Error)
	require.NoError(t, gdb.Create(&model.Cuisine{
		Name: "配置启用菜系", SortOrder: -100, IsActive: true,
	}).Error)
	require.NoError(t, gdb.Create(&model.Cuisine{
		Name: "配置停用菜系", SortOrder: -99, IsActive: false,
	}).Error)
	require.NoError(t, gdb.Create(&model.Cuisine{
		Name: "配置排序菜系 A", SortOrder: -200, IsActive: true,
	}).Error)
	require.NoError(t, gdb.Create(&model.Cuisine{
		Name: "配置排序菜系 B", SortOrder: -200, IsActive: true,
	}).Error)
	require.NoError(t, gdb.Create(&model.Flavor{
		Name: "配置启用口味", SortOrder: -100, IsActive: true,
	}).Error)
	require.NoError(t, gdb.Create(&model.Flavor{
		Name: "配置停用口味", SortOrder: -99, IsActive: false,
	}).Error)
	require.NoError(t, gdb.Create(&model.Flavor{
		Name: "配置排序口味 A", SortOrder: -200, IsActive: true,
	}).Error)
	require.NoError(t, gdb.Create(&model.Flavor{
		Name: "配置排序口味 B", SortOrder: -200, IsActive: true,
	}).Error)
	return activeCanteen, activeWindow
}
