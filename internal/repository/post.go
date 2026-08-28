package repository

import (
	"context"
	"time"

	"github.com/lib/pq"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/pagination"
)

// PostRepository 是无状态的帖子仓储；事务边界由请求 UoW 持有。
type PostRepository struct{}

// PostFilter 是帖子列表的数据库筛选条件。
type PostFilter struct {
	Keyword     *string
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

// PostModerationIssueRow 是作者可见的当前非 pass 审核部分。
type PostModerationIssueRow struct {
	Part          string
	ImagePosition *int16
	Moderation    model.ModerationStatus
	Labels        pq.StringArray `gorm:"type:text[]"`
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

// FindModerationIssues 返回正文和附图当前非 pass 结论，不暴露供应商原始响应或置信分。
func (PostRepository) FindModerationIssues(
	ctx context.Context,
	postID uint64,
) ([]PostModerationIssueRow, error) {
	rows := make([]PostModerationIssueRow, 0)
	err := db.FromContext(ctx).Raw(`
		WITH latest_text AS (
			SELECT mr.*
			FROM moderation_records AS mr
			WHERE mr.post_id = ?
			ORDER BY mr.created_at DESC, mr.id DESC
			LIMIT 1
		)
		SELECT
			'text'::text AS part,
			NULL::smallint AS image_position,
			latest_text.verdict::text AS moderation,
			CASE
				WHEN latest_text.provider = ? THEN COALESCE(machine.labels, ARRAY[]::text[])
				ELSE latest_text.labels
			END AS labels
		FROM latest_text
		LEFT JOIN moderation_records AS machine ON machine.id = latest_text.supersedes_id
		WHERE latest_text.verdict <> ?
		UNION ALL
		SELECT
			'image'::text AS part,
			pi.position AS image_position,
			image.moderation::text AS moderation,
			CASE
				WHEN image_mr.provider = ? THEN COALESCE(image_machine.labels, ARRAY[]::text[])
				ELSE COALESCE(image_mr.labels, ARRAY[]::text[])
			END AS labels
		FROM post_images AS pi
		JOIN image_assets AS image ON image.id = pi.image_asset_id
		LEFT JOIN LATERAL (
			SELECT mr.*
			FROM moderation_records AS mr
			WHERE mr.image_asset_id = image.id
			ORDER BY mr.created_at DESC, mr.id DESC
			LIMIT 1
		) AS image_mr ON true
		LEFT JOIN moderation_records AS image_machine ON image_machine.id = image_mr.supersedes_id
		WHERE pi.post_id = ? AND image.moderation <> ?
		ORDER BY image_position NULLS FIRST
	`, postID, model.ModerationProviderManual, model.ModerationVerdictPass,
		model.ModerationProviderManual, postID, model.ModerationStatusPass).
		Scan(&rows).Error
	return rows, err
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

// FindLatestPage 以 (created_at, id) 复合游标返回公开信息流，不执行全表 COUNT。
func (PostRepository) FindLatestPage(
	ctx context.Context,
	filter PostFilter,
	params pagination.CursorParams,
) ([]PostRecord, bool, error) {
	query := db.FromContext(ctx).Table("posts AS p").
		Where("p.deleted_at IS NULL AND p.status = ?", model.PostStatusApproved)
	query = applyPostFilter(query, filter)
	if params.After != nil {
		query = query.Where(
			"(p.created_at, p.id) < (?, ?)", params.After.CreatedAt, params.After.ID,
		)
	}
	query = addPostRecordJoins(query).Select(postRecordColumns).
		Order("p.created_at DESC, p.id DESC")
	records := make([]PostRecord, 0, params.Limit+1)
	if err := query.Limit(params.Limit + 1).Scan(&records).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(records) > params.Limit
	if hasMore {
		records = records[:params.Limit]
	}
	return records, hasMore, nil
}

// FindAuthorPage 返回指定作者的帖子；是否包含软删除行由调用者身份显式决定。
func (PostRepository) FindAuthorPage(
	ctx context.Context,
	authorID uint64,
	status *model.PostStatus,
	includeDeleted bool,
	params pagination.Params,
) ([]PostRecord, pagination.Meta, error) {
	query := db.FromContext(ctx).Table("posts AS p").
		Where("p.author_id = ?", authorID)
	if !includeDeleted {
		query = query.Where("p.deleted_at IS NULL")
	}
	if status != nil {
		query = query.Where("p.status = ?", *status)
	}
	return findScopedPostPage(query, params, "p.created_at DESC, p.id DESC")
}

// FindFavoritePage 返回用户按收藏时间倒序排列的已发布帖子。
func (PostRepository) FindFavoritePage(
	ctx context.Context,
	userID uint64,
	params pagination.Params,
) ([]PostRecord, pagination.Meta, error) {
	query := db.FromContext(ctx).Table("posts AS p").
		Joins("JOIN favorites AS favorite_scope ON favorite_scope.post_id = p.id").
		Where("favorite_scope.user_id = ? AND p.deleted_at IS NULL AND p.status = ?",
			userID, model.PostStatusApproved)
	return findScopedPostPage(query, params, "favorite_scope.created_at DESC, p.id DESC")
}

func findScopedPostPage(
	query *gorm.DB,
	params pagination.Params,
	order string,
) ([]PostRecord, pagination.Meta, error) {
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, pagination.Meta{}, err
	}
	records := make([]PostRecord, 0, params.Limit)
	query = addPostRecordJoins(query).Select(postRecordColumns).Order(order)
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

// SoftDelete 按明确来源软删除帖子；删除不改写内容更新时间。
func (PostRepository) SoftDelete(
	ctx context.Context,
	postID uint64,
	actorID uint64,
	reason model.DeleteReason,
	now time.Time,
) error {
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

// Restore 清除任意来源的帖子软删除标记，内容与关联保持原样。
func (PostRepository) Restore(ctx context.Context, postID uint64) error {
	result := db.FromContext(ctx).Model(&model.Post{}).
		Where("id = ? AND deleted_at IS NOT NULL", postID).
		UpdateColumns(map[string]any{
			"deleted_at": nil, "deleted_reason": nil, "deleted_by": nil,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// CurrentContentRevision 返回主表当前内容的版本号，即最大历史 revision + 1。
func (PostRepository) CurrentContentRevision(ctx context.Context, postID uint64) (int32, error) {
	var revision int32
	err := db.FromContext(ctx).Model(&model.PostHistory{}).
		Select("COALESCE(max(revision), 0) + 1").Where("post_id = ?", postID).Scan(&revision).Error
	return revision, err
}

// NextHistoryRevision 返回下一条旧版本快照的连续 revision。调用方必须已锁定帖子主体。
func (r PostRepository) NextHistoryRevision(ctx context.Context, postID uint64) (int32, error) {
	return r.CurrentContentRevision(ctx, postID)
}

// CreateHistory 追加一条被替换版本的完整快照。
func (PostRepository) CreateHistory(ctx context.Context, history *model.PostHistory) error {
	return db.FromContext(ctx).Create(history).Error
}

// ListHistories 按版本倒序返回帖子被替换的旧版本。
func (PostRepository) ListHistories(ctx context.Context, postID uint64) ([]model.PostHistory, error) {
	histories := make([]model.PostHistory, 0)
	err := db.FromContext(ctx).Where("post_id = ?", postID).
		Order("revision DESC").Find(&histories).Error
	return histories, err
}

// FindHistory 返回指定帖子的一版不可变旧快照。
func (PostRepository) FindHistory(
	ctx context.Context,
	postID uint64,
	revision int32,
) (*model.PostHistory, error) {
	var history model.PostHistory
	err := db.FromContext(ctx).Where("post_id = ? AND revision = ?", postID, revision).
		First(&history).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &history, nil
}

// LatestPostModerationForRevision 返回指定内容版本当前生效的最新文本审核结论。
func (PostRepository) LatestPostModerationForRevision(
	ctx context.Context,
	postID uint64,
	revision int32,
) (*model.ModerationRecord, error) {
	var record model.ModerationRecord
	err := db.FromContext(ctx).
		Where("post_id = ? AND content_revision = ?", postID, revision).
		Order("created_at DESC, id DESC").First(&record).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &record, nil
}

// ListHistoryModeration 返回每个旧版本最新一次机器文本审核结论。
func (PostRepository) ListHistoryModeration(
	ctx context.Context,
	postID uint64,
) ([]model.ModerationRecord, error) {
	records := make([]model.ModerationRecord, 0)
	err := db.FromContext(ctx).Raw(`
		SELECT DISTINCT ON (content_revision) mr.*
		FROM moderation_records AS mr
		WHERE mr.post_id = ? AND mr.content_revision IS NOT NULL AND mr.reviewer_id IS NULL
		ORDER BY content_revision, created_at DESC, id DESC
	`, postID).Scan(&records).Error
	return records, err
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
	if filter.Keyword != nil {
		pattern := literalContainsPattern(*filter.Keyword)
		query = query.Where(
			`(p.title ILIKE ? ESCAPE '\' OR p.content ILIKE ? ESCAPE '\')`,
			pattern,
			pattern,
		)
	}
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
