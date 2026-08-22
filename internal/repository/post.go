package repository

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/jingyijun/danshi_backend_go/internal/infra/db"
	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/pagination"
)

// PostRepository 是无状态的帖子仓储；事务边界由请求 UoW 持有。
type PostRepository struct{}

// PostFilter 是帖子列表的数据库筛选条件。
type PostFilter struct {
	PostType    *model.PostType
	ShareType   *model.ShareType
	Category    *model.PostCategory
	CanteenCode *string
	Cuisine     *string
	Flavors     []string
	Tags        []string
	MinPrice    *decimal.Decimal
	MaxPrice    *decimal.Decimal
	SortBy      string
}

// PostRecord 是帖子主体及列表、详情所需的一对一展示字段。
type PostRecord struct {
	model.Post
	AuthorName      string     `gorm:"column:author_name"`
	AuthorDeletedAt *time.Time `gorm:"column:author_deleted_at"`
	AvatarURL       *string    `gorm:"column:avatar_url"`
	CanteenCode     *string    `gorm:"column:canteen_code"`
	CanteenName     *string    `gorm:"column:canteen_name"`
	CanteenCampus   *string    `gorm:"column:canteen_campus"`
	WindowName      *string    `gorm:"column:window_name"`
	WindowFloor     *string    `gorm:"column:window_floor"`
	CuisineName     *string    `gorm:"column:cuisine_name"`
	Revision        int32      `gorm:"column:revision"`
}

// Create 创建帖子主体。计数列依赖数据库默认值，调用方不得传入。
func (PostRepository) Create(ctx context.Context, post *model.Post) error {
	return db.FromContext(ctx).Create(post).Error
}

// LockByID 按固定锁序的第一步锁定帖子主体。
func (PostRepository) LockByID(
	ctx context.Context,
	postID uint64,
	options ...QueryOptions,
) (*model.Post, error) {
	var post model.Post
	query := db.FromContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", postID)
	if len(options) == 0 || !options[0].IncludeDeleted {
		query = query.Where("deleted_at IS NULL")
	}
	if err := query.First(&post).Error; err != nil {
		return nil, NormalizeError(err)
	}
	return &post, nil
}

// FindByID 返回帖子及展示所需的一对一关系。
func (PostRepository) FindByID(
	ctx context.Context,
	postID uint64,
	options ...QueryOptions,
) (*PostRecord, error) {
	query := postRecordQuery(ctx).Where("p.id = ?", postID)
	if len(options) == 0 || !options[0].IncludeDeleted {
		query = query.Where("p.deleted_at IS NULL")
	}
	var record PostRecord
	result := query.Scan(&record)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return &record, nil
}

// FindPage 返回公开帖子列表；公开条件在仓储入口固定，调用方不能漏加。
func (PostRepository) FindPage(
	ctx context.Context,
	filter PostFilter,
	params pagination.Params,
) ([]PostRecord, pagination.Meta, error) {
	query := db.FromContext(ctx).Table("posts AS p").
		Where("p.deleted_at IS NULL AND p.status = ?", model.PostStatusApproved)
	query = applyPostFilter(query, filter)

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, pagination.Meta{}, err
	}

	query = addPostRecordJoins(query).Select(postRecordColumns)
	query = applyPostSort(query, filter.SortBy)
	records := make([]PostRecord, 0, params.Limit)
	if err := query.Offset(params.Offset()).Limit(params.Limit).Scan(&records).Error; err != nil {
		return nil, pagination.Meta{}, err
	}
	return records, pagination.NewMeta(params, total), nil
}

// IncrementView 只更新浏览数，不触发 GORM autoUpdateTime。
func (PostRepository) IncrementView(ctx context.Context, postID uint64) error {
	result := db.FromContext(ctx).Exec(
		"UPDATE posts SET view_count = view_count + 1 WHERE id = ? AND deleted_at IS NULL",
		postID,
	)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateContent 写入一版帖子主体内容。调用方必须先锁主体，并在同事务追加历史。
func (PostRepository) UpdateContent(ctx context.Context, postID uint64, fields map[string]any) error {
	result := db.FromContext(ctx).Model(&model.Post{}).Where("id = ?", postID).Updates(fields)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateStatus 只改变审核状态，不把状态流转记作内容编辑。
func (PostRepository) UpdateStatus(ctx context.Context, postID uint64, status model.PostStatus) error {
	return db.FromContext(ctx).Model(&model.Post{}).
		Where("id = ?", postID).UpdateColumn("status", status).Error
}

// SoftDelete 标记作者删除；删除不改写内容更新时间。
func (PostRepository) SoftDelete(ctx context.Context, postID, actorID uint64, now time.Time) error {
	reason := model.DeleteReasonAuthor
	result := db.FromContext(ctx).Model(&model.Post{}).Where("id = ? AND deleted_at IS NULL", postID).
		UpdateColumns(map[string]any{
			"deleted_at": now, "deleted_reason": reason, "deleted_by": actorID,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// LatestHistory 返回锁定帖子当前最新的全量版本。
func (PostRepository) LatestHistory(ctx context.Context, postID uint64) (*model.PostHistory, error) {
	var history model.PostHistory
	err := db.FromContext(ctx).Where("post_id = ?", postID).
		Order("revision DESC").First(&history).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &history, nil
}

// CreateHistory 追加一条全量版本快照。
func (PostRepository) CreateHistory(ctx context.Context, history *model.PostHistory) error {
	return db.FromContext(ctx).Create(history).Error
}

// ListHistories 按版本倒序返回帖子全量历史。
func (PostRepository) ListHistories(ctx context.Context, postID uint64) ([]model.PostHistory, error) {
	histories := make([]model.PostHistory, 0)
	err := db.FromContext(ctx).Where("post_id = ?", postID).
		Order("revision DESC").Find(&histories).Error
	return histories, err
}

func postRecordQuery(ctx context.Context) *gorm.DB {
	return addPostRecordJoins(db.FromContext(ctx).Table("posts AS p")).Select(postRecordColumns)
}

func addPostRecordJoins(query *gorm.DB) *gorm.DB {
	return query.
		Joins("JOIN users AS author ON author.id = p.author_id").
		Joins("LEFT JOIN image_assets AS avatar ON avatar.id = author.avatar_image_asset_id").
		Joins("LEFT JOIN canteens AS canteen ON canteen.id = p.canteen_id").
		Joins("LEFT JOIN canteen_windows AS cw ON cw.id = p.canteen_window_id").
		Joins("LEFT JOIN cuisines AS cuisine ON cuisine.id = p.cuisine_id").
		Joins("LEFT JOIN LATERAL (SELECT max(revision) AS revision FROM post_histories WHERE post_id = p.id) AS history ON true")
}

func applyPostFilter(query *gorm.DB, filter PostFilter) *gorm.DB {
	if filter.PostType != nil {
		query = query.Where("p.post_type = ?", *filter.PostType)
	}
	if filter.ShareType != nil {
		query = query.Where("p.share_type = ?", *filter.ShareType)
	}
	if filter.Category != nil {
		query = query.Where("p.category = ?", *filter.Category)
	}
	if filter.CanteenCode != nil {
		query = query.Where("EXISTS (SELECT 1 FROM canteens c WHERE c.id = p.canteen_id AND c.code = ?)", *filter.CanteenCode)
	}
	if filter.Cuisine != nil {
		query = query.Where("EXISTS (SELECT 1 FROM cuisines c WHERE c.id = p.cuisine_id AND c.name = ?)", *filter.Cuisine)
	}
	for _, flavor := range filter.Flavors {
		query = query.Where(`EXISTS (
			SELECT 1 FROM post_flavors pf JOIN flavors f ON f.id = pf.flavor_id
			WHERE pf.post_id = p.id AND f.name = ?)`, flavor)
	}
	for _, tag := range filter.Tags {
		query = query.Where(`EXISTS (
			SELECT 1 FROM post_tags pt JOIN tags t ON t.id = pt.tag_id
			WHERE pt.post_id = p.id AND t.deleted_at IS NULL AND lower(t.name) = lower(?))`, tag)
	}
	if filter.MinPrice != nil {
		query = query.Where("p.price >= ?", *filter.MinPrice)
	}
	if filter.MaxPrice != nil {
		query = query.Where("p.price <= ?", *filter.MaxPrice)
	}
	return query
}

func applyPostSort(query *gorm.DB, sortBy string) *gorm.DB {
	switch sortBy {
	case "hot":
		return query.Order("(p.like_count * 2 + p.favorite_count + p.comment_count + p.view_count * 0.1) DESC").
			Order("p.created_at DESC, p.id DESC")
	case "trending":
		return query.Order(`((p.like_count * 2.0 + p.favorite_count * 1.5 + p.comment_count) /
			power(extract(epoch FROM (now() - p.created_at)) / 3600.0 + 2, 2.0)) DESC`).
			Order("p.created_at DESC, p.id DESC")
	case "price":
		return query.Order("p.price ASC NULLS LAST").Order("p.created_at DESC, p.id DESC")
	default:
		return query.Order("p.created_at DESC, p.id DESC")
	}
}

const postRecordColumns = `
	p.*,
	author.name AS author_name,
	author.deleted_at AS author_deleted_at,
	avatar.public_url AS avatar_url,
	canteen.code AS canteen_code,
	canteen.name AS canteen_name,
	canteen.campus AS canteen_campus,
	cw.name AS window_name,
	cw.floor AS window_floor,
	cuisine.name AS cuisine_name,
	COALESCE(history.revision, 0) AS revision`
