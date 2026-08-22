// Package repository 提供只依赖当前 UoW context 的数据访问层。
//
// repository 不持有 *gorm.DB，也不自行开启或提交事务。所有入口都从
// db.FromContext(ctx) 取得当前请求的事务句柄，保证一次业务操作里的多表写入
// 要么全部提交，要么全部回滚。
package repository

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/infra/db"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/pagination"
)

// ErrNotFound 是 repository 层统一的“没有命中行”错误。
// service 不应依赖 GORM 的错误值；需要向 API 暴露 404 时统一交给 ToAPIError。
var ErrNotFound = errors.New("repository: resource not found")

// ErrAlreadyExists 表示唯一键冲突但不向上层泄露数据库约束名。
var ErrAlreadyExists = errors.New("repository: resource already exists")

// QueryOptions 是实体查询的公共选项。
// IncludeDeleted 必须由调用方显式开启；默认查询自动排除 deleted_at 非空的行。
type QueryOptions struct {
	IncludeDeleted bool
}

// Scope 为分页查询追加领域过滤或排序条件。
type Scope func(*gorm.DB) *gorm.DB

// FindPage 执行总数查询与分页查询，并返回统一分页元信息。
// 对声明了 DeletedAt 字段的实体，默认附加 deleted_at IS NULL。
func FindPage[T any](
	ctx context.Context,
	params pagination.Params,
	opts QueryOptions,
	scopes ...Scope,
) ([]T, pagination.Meta, error) {
	query := entityQuery[T](ctx, opts)
	for _, scope := range scopes {
		query = scope(query)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, pagination.Meta{}, err
	}

	items := make([]T, 0, params.Limit)
	if err := query.Offset(params.Offset()).Limit(params.Limit).Find(&items).Error; err != nil {
		return nil, pagination.Meta{}, err
	}
	return items, pagination.NewMeta(params, total), nil
}

// UpsertAssociation 幂等创建一条物理增删的关联记录。
// 唯一约束是并发正确性的真源；禁止在调用本函数前先 SELECT 判重。
func UpsertAssociation[T any](ctx context.Context, value *T) error {
	return db.FromContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(value).Error
}

// DeleteAssociation 幂等物理删除一条关联记录。
// value 必须携带模型声明的完整主键；GORM 会在缺少条件时拒绝全表删除。
func DeleteAssociation[T any](ctx context.Context, value *T) error {
	return db.FromContext(ctx).Delete(value).Error
}

// NormalizeError 隔离 repository 与 GORM 的 not-found 错误约定。
func NormalizeError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

// ToAPIError 是 ErrNotFound 到 API 404 的唯一转换约定；其它数据库错误归为 500。
func ToAPIError(err error, code apierr.BizCode, resource string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotFound) {
		return apierr.NotFound(code, resource)
	}
	return apierr.Internal(err)
}

func entityQuery[T any](ctx context.Context, opts QueryOptions) *gorm.DB {
	query := db.FromContext(ctx).Model(new(T))
	if !opts.IncludeDeleted && hasDeletedAt[T]() {
		query = query.Where("deleted_at IS NULL")
	}
	return query
}

func hasDeletedAt[T any]() bool {
	typ := reflect.TypeFor[T]()
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return false
	}
	_, ok := typ.FieldByName("DeletedAt")
	return ok
}

func literalContainsPattern(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + replacer.Replace(value) + "%"
}
