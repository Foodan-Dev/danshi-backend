package service

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/ptime"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

// CreateDictionaryItemInput 是口味与菜系共享的新建字段。
type CreateDictionaryItemInput struct {
	Name      string
	SortOrder int32
	IsActive  *bool
}

// UpdateDictionaryItemInput 是口味与菜系共享的局部更新字段。
type UpdateDictionaryItemInput struct {
	Name      *string
	SortOrder *int32
	IsActive  *bool
}

// CreateCanteenInput 是餐厅新建字段。
type CreateCanteenInput struct {
	Code      string
	Name      string
	Campus    string
	SortOrder int32
	IsActive  *bool
}

// UpdateCanteenInput 是餐厅局部更新字段；CodeSet 用于明确拒绝稳定 code 修改。
type UpdateCanteenInput struct {
	CodeSet   bool
	Name      *string
	Campus    *string
	SortOrder *int32
	IsActive  *bool
}

// CreateCanteenWindowInput 是窗口新建字段。
type CreateCanteenWindowInput struct {
	Name      string
	Floor     *string
	SortOrder int32
	IsActive  *bool
}

// UpdateCanteenWindowInput 是窗口局部更新字段。
type UpdateCanteenWindowInput struct {
	CanteenIDSet bool
	Name         *string
	Floor        *string
	FloorSet     bool
	SortOrder    *int32
	IsActive     *bool
}

// DictionaryItemView 是口味与菜系共享的响应形态。
type DictionaryItemView struct {
	ID        uint64     `json:"id"`
	Name      string     `json:"name"`
	SortOrder int32      `json:"sort_order"`
	IsActive  bool       `json:"is_active"`
	CreatedAt ptime.Time `json:"created_at"`
	UpdatedAt ptime.Time `json:"updated_at"`
}

// DictionaryCanteenView 是管理端餐厅响应。
type DictionaryCanteenView struct {
	ID        uint64     `json:"id"`
	Code      string     `json:"code"`
	Name      string     `json:"name"`
	Campus    string     `json:"campus"`
	SortOrder int32      `json:"sort_order"`
	IsActive  bool       `json:"is_active"`
	CreatedAt ptime.Time `json:"created_at"`
	UpdatedAt ptime.Time `json:"updated_at"`
}

// DictionaryWindowView 是管理端窗口响应。
type DictionaryWindowView struct {
	ID        uint64     `json:"id"`
	CanteenID uint64     `json:"canteen_id"`
	Name      string     `json:"name"`
	Floor     *string    `json:"floor"`
	SortOrder int32      `json:"sort_order"`
	IsActive  bool       `json:"is_active"`
	CreatedAt ptime.Time `json:"created_at"`
	UpdatedAt ptime.Time `json:"updated_at"`
}

// DictionaryDeleteResult 是物理删除成功回执。
type DictionaryDeleteResult struct {
	ID uint64 `json:"id"`
}

// CreateFlavor 新建口味词条。
func (s *DictionaryService) CreateFlavor(
	ctx context.Context,
	input CreateDictionaryItemInput,
) (*DictionaryItemView, error) {
	name, err := requiredDictionaryText(input.Name, "name", 50)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	item := &model.Flavor{
		Name: name, SortOrder: input.SortOrder, IsActive: defaultActive(input.IsActive),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateDictionary(ctx, item); err != nil {
		return nil, dictionaryMutationError(err, "口味")
	}
	view := dictionaryItemView(item.ID, item.Name, item.SortOrder, item.IsActive, item.CreatedAt, item.UpdatedAt)
	return &view, nil
}

// UpdateFlavor 局部更新口味。
func (s *DictionaryService) UpdateFlavor(
	ctx context.Context,
	itemID uint64,
	input UpdateDictionaryItemInput,
) (*DictionaryItemView, error) {
	fields, err := dictionaryItemFields(input, 50)
	if err != nil {
		return nil, err
	}
	if err := repository.UpdateDictionary[model.Flavor](ctx, itemID, fields); err != nil {
		return nil, dictionaryMutationError(err, "口味")
	}
	item, err := repository.DictionaryByID[model.Flavor](ctx, itemID)
	if err != nil {
		return nil, dictionaryMutationError(err, "口味")
	}
	view := dictionaryItemView(item.ID, item.Name, item.SortOrder, item.IsActive, item.CreatedAt, item.UpdatedAt)
	return &view, nil
}

// DeleteFlavor 删除无引用且无审核历史的口味。
func (s *DictionaryService) DeleteFlavor(ctx context.Context, itemID uint64) (*DictionaryDeleteResult, error) {
	if err := repository.DeleteDictionary[model.Flavor](ctx, itemID); err != nil {
		return nil, dictionaryDeleteError(err, "口味")
	}
	return &DictionaryDeleteResult{ID: itemID}, nil
}

// CreateCuisine 新建菜系词条。
func (s *DictionaryService) CreateCuisine(
	ctx context.Context,
	input CreateDictionaryItemInput,
) (*DictionaryItemView, error) {
	name, err := requiredDictionaryText(input.Name, "name", 50)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	item := &model.Cuisine{
		Name: name, SortOrder: input.SortOrder, IsActive: defaultActive(input.IsActive),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateDictionary(ctx, item); err != nil {
		return nil, dictionaryMutationError(err, "菜系")
	}
	view := dictionaryItemView(item.ID, item.Name, item.SortOrder, item.IsActive, item.CreatedAt, item.UpdatedAt)
	return &view, nil
}

// UpdateCuisine 局部更新菜系。
func (s *DictionaryService) UpdateCuisine(
	ctx context.Context,
	itemID uint64,
	input UpdateDictionaryItemInput,
) (*DictionaryItemView, error) {
	fields, err := dictionaryItemFields(input, 50)
	if err != nil {
		return nil, err
	}
	if err := repository.UpdateDictionary[model.Cuisine](ctx, itemID, fields); err != nil {
		return nil, dictionaryMutationError(err, "菜系")
	}
	item, err := repository.DictionaryByID[model.Cuisine](ctx, itemID)
	if err != nil {
		return nil, dictionaryMutationError(err, "菜系")
	}
	view := dictionaryItemView(item.ID, item.Name, item.SortOrder, item.IsActive, item.CreatedAt, item.UpdatedAt)
	return &view, nil
}

// DeleteCuisine 删除无引用且无审核历史的菜系。
func (s *DictionaryService) DeleteCuisine(ctx context.Context, itemID uint64) (*DictionaryDeleteResult, error) {
	if err := repository.DeleteDictionary[model.Cuisine](ctx, itemID); err != nil {
		return nil, dictionaryDeleteError(err, "菜系")
	}
	return &DictionaryDeleteResult{ID: itemID}, nil
}

// CreateCanteen 新建餐厅。
func (s *DictionaryService) CreateCanteen(
	ctx context.Context,
	input CreateCanteenInput,
) (*DictionaryCanteenView, error) {
	code, err := requiredDictionaryText(input.Code, "code", 64)
	if err != nil {
		return nil, err
	}
	name, err := requiredDictionaryText(input.Name, "name", 100)
	if err != nil {
		return nil, err
	}
	campus, err := requiredDictionaryText(input.Campus, "campus", 50)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	item := &model.Canteen{
		Code: code, Name: name, Campus: campus, SortOrder: input.SortOrder,
		IsActive: defaultActive(input.IsActive), CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateDictionary(ctx, item); err != nil {
		return nil, dictionaryMutationError(err, "餐厅")
	}
	view := dictionaryCanteenView(*item)
	return &view, nil
}

// UpdateCanteen 局部更新餐厅；稳定 code 永远不可修改。
func (s *DictionaryService) UpdateCanteen(
	ctx context.Context,
	itemID uint64,
	input UpdateCanteenInput,
) (*DictionaryCanteenView, error) {
	if input.CodeSet {
		return nil, apierr.InvalidField("code", apierr.FieldConflict, "餐厅 code 是稳定标识，不可修改")
	}
	fields := make(map[string]any)
	if input.Name != nil {
		name, err := requiredDictionaryText(*input.Name, "name", 100)
		if err != nil {
			return nil, err
		}
		fields["name"] = name
	}
	if input.Campus != nil {
		campus, err := requiredDictionaryText(*input.Campus, "campus", 50)
		if err != nil {
			return nil, err
		}
		fields["campus"] = campus
	}
	addDictionaryPatchFields(fields, input.SortOrder, input.IsActive)
	if len(fields) == 0 {
		return nil, apierr.InvalidField("body", apierr.FieldRequired, "至少提供一个可更新字段")
	}
	if err := repository.UpdateDictionary[model.Canteen](ctx, itemID, fields); err != nil {
		return nil, dictionaryMutationError(err, "餐厅")
	}
	item, err := repository.DictionaryByID[model.Canteen](ctx, itemID)
	if err != nil {
		return nil, dictionaryMutationError(err, "餐厅")
	}
	view := dictionaryCanteenView(*item)
	return &view, nil
}

// DeleteCanteen 删除无引用且无审核历史的餐厅。
func (s *DictionaryService) DeleteCanteen(ctx context.Context, itemID uint64) (*DictionaryDeleteResult, error) {
	if err := repository.DeleteDictionary[model.Canteen](ctx, itemID); err != nil {
		return nil, dictionaryDeleteError(err, "餐厅")
	}
	return &DictionaryDeleteResult{ID: itemID}, nil
}

// CreateWindow 在指定餐厅下新建窗口。
func (s *DictionaryService) CreateWindow(
	ctx context.Context,
	canteenID uint64,
	input CreateCanteenWindowInput,
) (*DictionaryWindowView, error) {
	if _, err := repository.DictionaryByID[model.Canteen](ctx, canteenID); err != nil {
		return nil, dictionaryMutationError(err, "餐厅")
	}
	name, err := requiredDictionaryText(input.Name, "name", 100)
	if err != nil {
		return nil, err
	}
	floor, err := normalizeFloor(input.Floor)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	item := &model.CanteenWindow{
		CanteenID: canteenID, Name: name, Floor: floor, SortOrder: input.SortOrder,
		IsActive: defaultActive(input.IsActive), CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateDictionary(ctx, item); err != nil {
		return nil, dictionaryMutationError(err, "窗口")
	}
	view := dictionaryWindowView(*item)
	return &view, nil
}

// UpdateWindow 局部更新窗口；所属餐厅不可移动。
func (s *DictionaryService) UpdateWindow(
	ctx context.Context,
	itemID uint64,
	input UpdateCanteenWindowInput,
) (*DictionaryWindowView, error) {
	if input.CanteenIDSet {
		return nil, apierr.InvalidField("canteen_id", apierr.FieldConflict, "窗口所属餐厅不可修改")
	}
	fields := make(map[string]any)
	if input.Name != nil {
		name, err := requiredDictionaryText(*input.Name, "name", 100)
		if err != nil {
			return nil, err
		}
		fields["name"] = name
	}
	if input.FloorSet {
		floor, err := normalizeFloor(input.Floor)
		if err != nil {
			return nil, err
		}
		fields["floor"] = floor
	}
	addDictionaryPatchFields(fields, input.SortOrder, input.IsActive)
	if len(fields) == 0 {
		return nil, apierr.InvalidField("body", apierr.FieldRequired, "至少提供一个可更新字段")
	}
	if err := repository.UpdateDictionary[model.CanteenWindow](ctx, itemID, fields); err != nil {
		return nil, dictionaryMutationError(err, "窗口")
	}
	item, err := repository.DictionaryByID[model.CanteenWindow](ctx, itemID)
	if err != nil {
		return nil, dictionaryMutationError(err, "窗口")
	}
	view := dictionaryWindowView(*item)
	return &view, nil
}

// DeleteWindow 删除无引用且无审核历史的窗口。
func (s *DictionaryService) DeleteWindow(ctx context.Context, itemID uint64) (*DictionaryDeleteResult, error) {
	if err := repository.DeleteDictionary[model.CanteenWindow](ctx, itemID); err != nil {
		return nil, dictionaryDeleteError(err, "窗口")
	}
	return &DictionaryDeleteResult{ID: itemID}, nil
}

func dictionaryItemFields(input UpdateDictionaryItemInput, nameLimit int) (map[string]any, error) {
	fields := make(map[string]any)
	if input.Name != nil {
		name, err := requiredDictionaryText(*input.Name, "name", nameLimit)
		if err != nil {
			return nil, err
		}
		fields["name"] = name
	}
	addDictionaryPatchFields(fields, input.SortOrder, input.IsActive)
	if len(fields) == 0 {
		return nil, apierr.InvalidField("body", apierr.FieldRequired, "至少提供一个可更新字段")
	}
	return fields, nil
}

func addDictionaryPatchFields(fields map[string]any, sortOrder *int32, isActive *bool) {
	if sortOrder != nil {
		fields["sort_order"] = *sortOrder
	}
	if isActive != nil {
		fields["is_active"] = *isActive
	}
}

func defaultActive(value *bool) bool {
	if value == nil {
		return true
	}
	return *value
}

func normalizeFloor(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	floor := strings.TrimSpace(*value)
	if floor == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(floor) > 20 {
		return nil, apierr.InvalidField("floor", apierr.FieldTooLong, "floor 不能超过 20 个字符")
	}
	return &floor, nil
}

func dictionaryItemView(
	id uint64,
	name string,
	sortOrder int32,
	isActive bool,
	createdAt time.Time,
	updatedAt time.Time,
) DictionaryItemView {
	return DictionaryItemView{
		ID: id, Name: name, SortOrder: sortOrder, IsActive: isActive,
		CreatedAt: ptime.Time(createdAt), UpdatedAt: ptime.Time(updatedAt),
	}
}

func dictionaryCanteenView(item model.Canteen) DictionaryCanteenView {
	return DictionaryCanteenView{
		ID: item.ID, Code: item.Code, Name: item.Name, Campus: item.Campus,
		SortOrder: item.SortOrder, IsActive: item.IsActive,
		CreatedAt: ptime.Time(item.CreatedAt), UpdatedAt: ptime.Time(item.UpdatedAt),
	}
}

func dictionaryWindowView(item model.CanteenWindow) DictionaryWindowView {
	return DictionaryWindowView{
		ID: item.ID, CanteenID: item.CanteenID, Name: item.Name, Floor: item.Floor,
		SortOrder: item.SortOrder, IsActive: item.IsActive,
		CreatedAt: ptime.Time(item.CreatedAt), UpdatedAt: ptime.Time(item.UpdatedAt),
	}
}

func dictionaryMutationError(err error, resource string) error {
	if errors.Is(err, repository.ErrNotFound) {
		return apierr.NotFound(apierr.BizDictItemNotFound, resource)
	}
	if errors.Is(err, repository.ErrAlreadyExists) || repository.IsSQLState(err, "23505") {
		return apierr.Conflict(apierr.BizAlreadyExists, "同名或同标识词表项已存在")
	}
	return apierr.Internal(err)
}

func dictionaryDeleteError(err error, resource string) error {
	if errors.Is(err, repository.ErrNotFound) {
		return apierr.NotFound(apierr.BizDictItemNotFound, resource)
	}
	if repository.IsSQLState(err, "23503", "23001") {
		return apierr.Conflict(apierr.BizDictItemInUse, "该词条已被引用，只能停用")
	}
	return apierr.Internal(err)
}
