package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/jingyijun/danshi_backend_go/internal/infra/db"
	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/pagination"
)

// SuggestionRecord 是提议主体与提议人展示信息的一次查询结果。
type SuggestionRecord struct {
	model.DictionarySuggestion
	ProposerName      string     `gorm:"column:proposer_name"`
	ProposerDeletedAt *time.Time `gorm:"column:proposer_deleted_at"`
}

// DictionaryRepository 实现词条提议与管理端词表维护的数据访问。
type DictionaryRepository struct{}

// CreateSuggestion 创建一条待人工审核提议。
func (DictionaryRepository) CreateSuggestion(ctx context.Context, value *model.DictionarySuggestion) error {
	return db.FromContext(ctx).Create(value).Error
}

// FindSuggestionByID 查询提议主体。
func (DictionaryRepository) FindSuggestionByID(
	ctx context.Context,
	suggestionID uint64,
) (*model.DictionarySuggestion, error) {
	var suggestion model.DictionarySuggestion
	err := db.FromContext(ctx).Where("id = ?", suggestionID).First(&suggestion).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &suggestion, nil
}

// FindSuggestionRecordByID 查询提议及可能已注销的提议人展示信息。
func (DictionaryRepository) FindSuggestionRecordByID(
	ctx context.Context,
	suggestionID uint64,
) (*SuggestionRecord, error) {
	var record SuggestionRecord
	result := db.FromContext(ctx).Table("dictionary_suggestions AS d").
		Select(`d.*, proposer.name AS proposer_name,
			proposer.deleted_at AS proposer_deleted_at`).
		Joins("JOIN users AS proposer ON proposer.id = d.proposer_id").
		Where("d.id = ?", suggestionID).Scan(&record)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return &record, nil
}

// LockSuggestionByID 串行化同一提议的终态审核。
func (DictionaryRepository) LockSuggestionByID(
	ctx context.Context,
	suggestionID uint64,
) (*model.DictionarySuggestion, error) {
	var suggestion model.DictionarySuggestion
	err := db.FromContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", suggestionID).First(&suggestion).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &suggestion, nil
}

// FindMinePage 返回当前用户自己的全部提议，按提交时间倒序。
func (DictionaryRepository) FindMinePage(
	ctx context.Context,
	proposerID uint64,
	params pagination.Params,
) ([]SuggestionRecord, pagination.Meta, error) {
	query := db.FromContext(ctx).Table("dictionary_suggestions AS d").
		Where("d.proposer_id = ?", proposerID)
	return findSuggestionPage(query, params, "d.created_at DESC, d.id DESC")
}

// FindPendingPage 返回管理端待办；kind 为空时按 kind、提交时间排序。
func (DictionaryRepository) FindPendingPage(
	ctx context.Context,
	kind *model.SuggestionKind,
	params pagination.Params,
) ([]SuggestionRecord, pagination.Meta, error) {
	query := db.FromContext(ctx).Table("dictionary_suggestions AS d").
		Where("d.status = ?", model.SuggestionStatusPending)
	if kind != nil {
		query = query.Where("d.kind = ?", *kind)
	}
	return findSuggestionPage(query, params, "d.kind, d.created_at, d.id")
}

func findSuggestionPage(
	query *gorm.DB,
	params pagination.Params,
	order string,
) ([]SuggestionRecord, pagination.Meta, error) {
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, pagination.Meta{}, err
	}
	rows := make([]SuggestionRecord, 0, params.Limit)
	err := query.Select(`d.*, proposer.name AS proposer_name,
		proposer.deleted_at AS proposer_deleted_at`).
		Joins("JOIN users AS proposer ON proposer.id = d.proposer_id").
		Order(order).Offset(params.Offset()).Limit(params.Limit).Scan(&rows).Error
	if err != nil {
		return nil, pagination.Meta{}, err
	}
	return rows, pagination.NewMeta(params, total), nil
}

// ReviewSuggestion 以单次 UPDATE 写入审核终态及对应产出。
func (DictionaryRepository) ReviewSuggestion(
	ctx context.Context,
	suggestionID uint64,
	fields map[string]any,
) error {
	fields["updated_at"] = time.Now().UTC()
	result := db.FromContext(ctx).Model(&model.DictionarySuggestion{}).
		Where("id = ? AND status = ?", suggestionID, model.SuggestionStatusPending).
		Updates(fields)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DictionaryByID 按实体主键读取任一字典表条目。
func DictionaryByID[T any](ctx context.Context, itemID uint64) (*T, error) {
	var item T
	err := db.FromContext(ctx).Where("id = ?", itemID).First(&item).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &item, nil
}

// CreateDictionary 创建一条管理端词表项。
func CreateDictionary[T any](ctx context.Context, item *T) error {
	return db.FromContext(ctx).Create(item).Error
}

// UpdateDictionary 局部更新一条词表项。
func UpdateDictionary[T any](ctx context.Context, itemID uint64, fields map[string]any) error {
	fields["updated_at"] = time.Now().UTC()
	result := db.FromContext(ctx).Model(new(T)).Where("id = ?", itemID).Updates(fields)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteDictionary 物理删除从未被帖子或审核历史引用的词表项。
func DeleteDictionary[T any](ctx context.Context, itemID uint64) error {
	result := db.FromContext(ctx).Where("id = ?", itemID).Delete(new(T))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// FindOrCreateFlavor 审批时按唯一名称复用或创建口味。
func (DictionaryRepository) FindOrCreateFlavor(
	ctx context.Context,
	name string,
	sortOrder int32,
) (*model.Flavor, error) {
	item := &model.Flavor{Name: name, SortOrder: sortOrder, IsActive: true}
	result := db.FromContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(item)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 1 {
		return item, nil
	}
	err := db.FromContext(ctx).Where("name = ?", name).First(item).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return item, nil
}

// FindOrCreateCuisine 审批时按唯一名称复用或创建菜系。
func (DictionaryRepository) FindOrCreateCuisine(
	ctx context.Context,
	name string,
	sortOrder int32,
) (*model.Cuisine, error) {
	item := &model.Cuisine{Name: name, SortOrder: sortOrder, IsActive: true}
	result := db.FromContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(item)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 1 {
		return item, nil
	}
	err := db.FromContext(ctx).Where("name = ?", name).First(item).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return item, nil
}

// FindOrCreateCanteen 审批时优先按提议名称复用餐厅；否则用管理员补充字段创建。
func (DictionaryRepository) FindOrCreateCanteen(
	ctx context.Context,
	name string,
	code string,
	campus string,
	sortOrder int32,
) (*model.Canteen, error) {
	item := &model.Canteen{
		Code: code, Name: name, Campus: campus, SortOrder: sortOrder, IsActive: true,
	}
	result := db.FromContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(item)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 1 {
		return item, nil
	}
	err := db.FromContext(ctx).Where("name = ?", name).First(item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrAlreadyExists
	}
	if err != nil {
		return nil, err
	}
	return item, nil
}

// FindCanteenByName 按全局唯一名称查找餐厅。
func (DictionaryRepository) FindCanteenByName(
	ctx context.Context,
	name string,
) (*model.Canteen, error) {
	var item model.Canteen
	err := db.FromContext(ctx).Where("name = ?", name).First(&item).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &item, nil
}

// FindOrCreateWindow 审批时按所属餐厅、楼层与名称复用或创建窗口。
func (DictionaryRepository) FindOrCreateWindow(
	ctx context.Context,
	canteenID uint64,
	name string,
	floor *string,
	sortOrder int32,
) (*model.CanteenWindow, error) {
	item := &model.CanteenWindow{
		CanteenID: canteenID, Name: name, Floor: floor, SortOrder: sortOrder, IsActive: true,
	}
	result := db.FromContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(item)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 1 {
		return item, nil
	}
	err := db.FromContext(ctx).Where(
		"canteen_id = ? AND floor IS NOT DISTINCT FROM ? AND name = ?", canteenID, floor, name,
	).First(item).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return item, nil
}

// UpsertPostFlavor 将批准的口味按提议立场绑定回来源帖子。
func (DictionaryRepository) UpsertPostFlavor(
	ctx context.Context,
	postID uint64,
	postType model.PostType,
	flavorID uint64,
	stance model.FlavorStance,
) error {
	row := &model.PostFlavor{
		PostID: postID, FlavorID: flavorID, Stance: stance, PostType: postType,
	}
	return db.FromContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "post_id"}, {Name: "flavor_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"stance", "post_type"}),
	}).Create(row).Error
}

// IsSQLState 判断 PostgreSQL 错误类型，供 service 映射稳定业务码。
func IsSQLState(err error, states ...string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	for _, state := range states {
		if pgErr.Code == state {
			return true
		}
	}
	return false
}
