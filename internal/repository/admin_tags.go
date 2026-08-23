package repository

import (
	"context"
	"time"

	"gorm.io/gorm/clause"

	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/pagination"
)

// AdminTagFilter 是管理端话题标签列表的可选筛选条件。
type AdminTagFilter struct {
	Name       *string
	Moderation *model.ModerationStatus
	IsDeleted  *bool
}

// HotTagRecord 是实时热门标签聚合的一行。
type HotTagRecord struct {
	ID        uint64
	Name      string
	PostCount int64 `gorm:"column:post_count"`
}

// FindTagCursorPage 以 (created_at, id) 复合游标返回标签管理页。
func (AdminRepository) FindTagCursorPage(
	ctx context.Context,
	filter AdminTagFilter,
	params pagination.CursorParams,
) ([]model.Tag, bool, error) {
	query := db.FromContext(ctx).Model(&model.Tag{})
	if filter.Name != nil {
		query = query.Where("name ILIKE ? ESCAPE '\\'", literalContainsPattern(*filter.Name))
	}
	if filter.Moderation != nil {
		query = query.Where("moderation = ?", *filter.Moderation)
	}
	if filter.IsDeleted != nil {
		if *filter.IsDeleted {
			query = query.Where("deleted_at IS NOT NULL")
		} else {
			query = query.Where("deleted_at IS NULL")
		}
	}
	if params.After != nil {
		query = query.Where(
			"(created_at, id) < (?, ?)", params.After.CreatedAt, params.After.ID,
		)
	}
	rows := make([]model.Tag, 0, params.Limit+1)
	if err := query.Order("created_at DESC, id DESC").Limit(params.Limit + 1).Find(&rows).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(rows) > params.Limit
	if hasMore {
		rows = rows[:params.Limit]
	}
	return rows, hasMore, nil
}

// LockTagsByIDs 按 id 升序锁定标签，供重命名与合并保持固定锁序。
func (AdminRepository) LockTagsByIDs(ctx context.Context, ids []uint64) ([]model.Tag, error) {
	rows := make([]model.Tag, 0, len(ids))
	err := db.FromContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ?", ids).Order("id").Find(&rows).Error
	return rows, err
}

// FindTagPostIDs 返回当前关联指定标签的帖子 id，固定升序便于后续加锁。
func (AdminRepository) FindTagPostIDs(ctx context.Context, tagID uint64) ([]uint64, error) {
	var ids []uint64
	err := db.FromContext(ctx).Model(&model.PostTag{}).Where("tag_id = ?", tagID).
		Order("post_id").Pluck("post_id", &ids).Error
	return ids, err
}

// LockPostsByIDs 按 id 升序锁定受标签管理动作影响的帖子。
func (AdminRepository) LockPostsByIDs(ctx context.Context, ids []uint64) ([]model.Post, error) {
	if len(ids) == 0 {
		return []model.Post{}, nil
	}
	rows := make([]model.Post, 0, len(ids))
	err := db.FromContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ?", ids).Order("id").Find(&rows).Error
	return rows, err
}

// RenameTag 更新标签规范展示名；唯一约束负责拒绝大小写不敏感重名。
func (AdminRepository) RenameTag(ctx context.Context, tagID uint64, name string, now time.Time) error {
	result := db.FromContext(ctx).Model(&model.Tag{}).Where("id = ?", tagID).
		UpdateColumns(map[string]any{"name": name, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// MergeTagRelations 将源标签关联迁移到目标后清空源关联，并软删除源标签。
func (AdminRepository) MergeTagRelations(
	ctx context.Context,
	sourceID uint64,
	targetID uint64,
	now time.Time,
) error {
	tx := db.FromContext(ctx)
	if err := tx.Exec(`
		INSERT INTO post_tags (post_id, tag_id)
		SELECT post_id, ? FROM post_tags WHERE tag_id = ?
		ON CONFLICT DO NOTHING
	`, targetID, sourceID).Error; err != nil {
		return err
	}
	if err := tx.Where("tag_id = ?", sourceID).Delete(&model.PostTag{}).Error; err != nil {
		return err
	}
	result := tx.Model(&model.Tag{}).Where("id = ?", sourceID).
		UpdateColumns(map[string]any{"deleted_at": now, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// SetTagDeletedAt 幂等地下架或恢复标签，不触碰 post_tags 关联。
func (AdminRepository) SetTagDeletedAt(
	ctx context.Context,
	tagID uint64,
	deletedAt *time.Time,
	now time.Time,
) error {
	result := db.FromContext(ctx).Model(&model.Tag{}).Where("id = ?", tagID).
		UpdateColumns(map[string]any{"deleted_at": deletedAt, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// FindHotTags 按未删除且已发布可见帖子的关联数实时聚合 TopN。
func (AdminRepository) FindHotTags(ctx context.Context, limit int) ([]HotTagRecord, error) {
	rows := make([]HotTagRecord, 0, limit)
	err := db.FromContext(ctx).Table("tags AS t").
		Select("t.id, t.name, count(*) AS post_count").
		Joins("JOIN post_tags AS pt ON pt.tag_id = t.id").
		Joins("JOIN posts AS p ON p.id = pt.post_id").
		Where("t.deleted_at IS NULL AND p.deleted_at IS NULL AND p.status = ?", model.PostStatusApproved).
		Group("t.id, t.name").Order("post_count DESC, lower(t.name), t.id").Limit(limit).
		Scan(&rows).Error
	return rows, err
}
