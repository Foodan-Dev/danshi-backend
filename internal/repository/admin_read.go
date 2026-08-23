package repository

import (
	"context"

	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/pagination"
)

// AdminRepository 是管理端跨实体读取与状态写入的持久化边界。
type AdminRepository struct{}

// AdminPostFilter 是管理端帖子列表的可选筛选条件。
type AdminPostFilter struct {
	Status   *model.PostStatus
	PostType *model.PostType
	AuthorID *uint64
}

// AdminPostRecord 是管理端帖子列表的一行，包含作者与批量加载的图片。
type AdminPostRecord struct {
	model.Post
	AuthorName  string   `gorm:"column:author_name"`
	AuthorEmail string   `gorm:"column:author_email"`
	Images      []string `gorm:"-"`
}

// AdminCommentRecord 是管理端评论列表的一行。
type AdminCommentRecord struct {
	model.Comment
	AuthorName  string `gorm:"column:author_name"`
	AuthorEmail string `gorm:"column:author_email"`
}

// AdminUserFilter 是管理端用户列表的可选筛选条件。
type AdminUserFilter struct {
	Role     *model.UserRole
	IsActive *bool
}

// AdminUserRecord 是用户主体、头像及统计数据的一次查询结果。
type AdminUserRecord struct {
	model.User
	Roles          pq.StringArray `gorm:"column:roles;type:text[]"`
	AvatarURL      *string        `gorm:"column:avatar_url"`
	PostCount      int64
	LikeCount      int64
	FavoriteCount  int64
	FollowerCount  int64
	FollowingCount int64
}

// PendingModerationRecord 是待人工复核队列的一行及其可读内容摘要。
type PendingModerationRecord struct {
	model.ModerationRecord
	TargetType string  `gorm:"column:target_type"`
	TargetID   uint64  `gorm:"column:target_id"`
	Content    *string `gorm:"column:content"`
}

// FindPostPage 返回管理端帖子页；调用方必须显式决定是否包含软删除行。
func (AdminRepository) FindPostPage(
	ctx context.Context,
	filter AdminPostFilter,
	params pagination.Params,
	opts QueryOptions,
) ([]AdminPostRecord, pagination.Meta, error) {
	query := db.FromContext(ctx).Table("posts AS p")
	if !opts.IncludeDeleted {
		query = query.Where("p.deleted_at IS NULL")
	}
	if filter.Status != nil {
		query = query.Where("p.status = ?", *filter.Status)
	}
	if filter.PostType != nil {
		query = query.Where("p.post_type = ?", *filter.PostType)
	}
	if filter.AuthorID != nil {
		query = query.Where("p.author_id = ?", *filter.AuthorID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, pagination.Meta{}, err
	}
	records := make([]AdminPostRecord, 0, params.Limit)
	err := query.Select("p.*, author.name AS author_name, author.email AS author_email").
		Joins("JOIN users AS author ON author.id = p.author_id").
		Order("p.created_at DESC, p.id DESC").Offset(params.Offset()).Limit(params.Limit).
		Scan(&records).Error
	if err != nil {
		return nil, pagination.Meta{}, err
	}
	if err := loadAdminPostImages(ctx, records); err != nil {
		return nil, pagination.Meta{}, err
	}
	return records, pagination.NewMeta(params, total), nil
}

// FindCommentPage 返回管理端评论页；调用方必须显式决定是否包含软删除行。
func (AdminRepository) FindCommentPage(
	ctx context.Context,
	postID *uint64,
	params pagination.Params,
	opts QueryOptions,
) ([]AdminCommentRecord, pagination.Meta, error) {
	query := db.FromContext(ctx).Table("comments AS c")
	if !opts.IncludeDeleted {
		query = query.Where("c.deleted_at IS NULL")
	}
	if postID != nil {
		query = query.Where("c.post_id = ?", *postID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, pagination.Meta{}, err
	}
	records := make([]AdminCommentRecord, 0, params.Limit)
	err := query.Select("c.*, author.name AS author_name, author.email AS author_email").
		Joins("JOIN users AS author ON author.id = c.author_id").
		Order("c.created_at DESC, c.id DESC").Offset(params.Offset()).Limit(params.Limit).
		Scan(&records).Error
	if err != nil {
		return nil, pagination.Meta{}, err
	}
	return records, pagination.NewMeta(params, total), nil
}

// FindUserPage 返回管理端用户页；统计字段由同一条查询的子查询批量计算。
func (AdminRepository) FindUserPage(
	ctx context.Context,
	filter AdminUserFilter,
	params pagination.Params,
	opts QueryOptions,
) ([]AdminUserRecord, pagination.Meta, error) {
	query := db.FromContext(ctx).Table("users AS u")
	if !opts.IncludeDeleted {
		query = query.Where("u.deleted_at IS NULL")
	}
	if filter.Role != nil {
		if *filter.Role == model.UserRoleUser {
			query = query.Where("NOT EXISTS (SELECT 1 FROM user_roles ur WHERE ur.user_id = u.id)")
		} else {
			query = query.Where(`EXISTS (
				SELECT 1 FROM user_roles ur WHERE ur.user_id = u.id AND ur.role = ?
			)`, *filter.Role)
		}
	}
	if filter.IsActive != nil {
		active := `u.deleted_at IS NULL AND NOT (
			u.ban_is_permanent OR COALESCE(u.banned_until > CURRENT_TIMESTAMP, false)
		)`
		if *filter.IsActive {
			query = query.Where(active)
		} else {
			query = query.Where("NOT (" + active + ")")
		}
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, pagination.Meta{}, err
	}
	records := make([]AdminUserRecord, 0, params.Limit)
	err := query.Select(adminUserColumns).
		Joins("LEFT JOIN image_assets AS avatar ON avatar.id = u.avatar_image_asset_id").
		Order("u.created_at DESC, u.id DESC").Offset(params.Offset()).Limit(params.Limit).
		Scan(&records).Error
	if err != nil {
		return nil, pagination.Meta{}, err
	}
	return records, pagination.NewMeta(params, total), nil
}

// FindUserByID 返回管理端单用户详情所需的主体、角色、头像与统计。
func (AdminRepository) FindUserByID(
	ctx context.Context,
	userID uint64,
	opts QueryOptions,
) (*AdminUserRecord, error) {
	query := db.FromContext(ctx).Table("users AS u").Where("u.id = ?", userID)
	if !opts.IncludeDeleted {
		query = query.Where("u.deleted_at IS NULL")
	}
	var record AdminUserRecord
	result := query.Select(adminUserColumns).
		Joins("LEFT JOIN image_assets AS avatar ON avatar.id = u.avatar_image_asset_id").
		Scan(&record)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return &record, nil
}

// FindUserBanRecords 返回目标用户完整封禁历史，最新动作在前。
func (AdminRepository) FindUserBanRecords(
	ctx context.Context,
	userID uint64,
) ([]model.UserBanRecord, error) {
	records := make([]model.UserBanRecord, 0)
	err := db.FromContext(ctx).Where("user_id = ?", userID).
		Order("created_at DESC, id DESC").Find(&records).Error
	return records, err
}

// FindPendingModerationPage 返回尚未被 supersedes_id 指过的机器 review 记录。
func (AdminRepository) FindPendingModerationPage(
	ctx context.Context,
	params pagination.Params,
) ([]PendingModerationRecord, pagination.Meta, error) {
	query := pendingModerationQuery(ctx)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, pagination.Meta{}, err
	}
	records := make([]PendingModerationRecord, 0, params.Limit)
	err := query.Select(pendingModerationColumns).
		Joins("LEFT JOIN posts AS p ON p.id = mr.post_id").
		Joins("LEFT JOIN comments AS c ON c.id = mr.comment_id").
		Joins("LEFT JOIN image_assets AS image ON image.id = mr.image_asset_id").
		Joins("LEFT JOIN tags AS tag ON tag.id = mr.tag_id").
		Joins("LEFT JOIN users AS target_user ON target_user.id = mr.user_id").
		Order("mr.created_at, mr.id").Offset(params.Offset()).Limit(params.Limit).
		Scan(&records).Error
	if err != nil {
		return nil, pagination.Meta{}, err
	}
	return records, pagination.NewMeta(params, total), nil
}

// FindPendingPostReview 返回指定帖子的最新一条未处理机器 review 记录。
func (AdminRepository) FindPendingPostReview(
	ctx context.Context,
	postID uint64,
) (*model.ModerationRecord, error) {
	var record model.ModerationRecord
	err := pendingModerationQuery(ctx).Where("mr.post_id = ?", postID).
		Order("mr.created_at DESC, mr.id DESC").First(&record).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &record, nil
}

func pendingModerationQuery(ctx context.Context) *gorm.DB {
	return db.FromContext(ctx).Table("moderation_records AS mr").Where(`
		mr.verdict = ? AND mr.provider <> ?
		AND NOT EXISTS (
			SELECT 1 FROM moderation_records AS manual
			WHERE manual.supersedes_id = mr.id
		)
	`, model.ModerationVerdictReview, model.ModerationProviderManual)
}

func loadAdminPostImages(ctx context.Context, records []AdminPostRecord) error {
	postIDs := make([]uint64, 0, len(records))
	byID := make(map[uint64]int, len(records))
	for index := range records {
		postIDs = append(postIDs, records[index].ID)
		byID[records[index].ID] = index
		records[index].Images = []string{}
	}
	if len(postIDs) == 0 {
		return nil
	}
	type imageRow struct {
		PostID    uint64
		PublicURL string
	}
	var rows []imageRow
	err := db.FromContext(ctx).Table("post_images AS pi").
		Select("pi.post_id, image.public_url").
		Joins("JOIN image_assets AS image ON image.id = pi.image_asset_id").
		Where("pi.post_id IN ?", postIDs).Order("pi.post_id, pi.position").Scan(&rows).Error
	if err != nil {
		return err
	}
	for _, row := range rows {
		index := byID[row.PostID]
		records[index].Images = append(records[index].Images, row.PublicURL)
	}
	return nil
}

const adminUserColumns = `
	u.*, avatar.public_url AS avatar_url,
	ARRAY(SELECT ur.role FROM user_roles AS ur WHERE ur.user_id = u.id ORDER BY ur.role) AS roles,
	(SELECT count(*) FROM posts AS p
	 WHERE p.author_id = u.id AND p.deleted_at IS NULL) AS post_count,
	(SELECT COALESCE(sum(p.like_count), 0) FROM posts AS p
	 WHERE p.author_id = u.id AND p.deleted_at IS NULL) AS like_count,
	(SELECT COALESCE(sum(p.favorite_count), 0) FROM posts AS p
	 WHERE p.author_id = u.id AND p.deleted_at IS NULL) AS favorite_count,
	(SELECT count(*) FROM follows AS f WHERE f.following_id = u.id) AS follower_count,
	(SELECT count(*) FROM follows AS f WHERE f.follower_id = u.id) AS following_count`

const pendingModerationColumns = `
	mr.*,
	CASE
		WHEN mr.post_id IS NOT NULL THEN 'post'
		WHEN mr.comment_id IS NOT NULL THEN 'comment'
		WHEN mr.image_asset_id IS NOT NULL THEN 'image_asset'
		WHEN mr.tag_id IS NOT NULL THEN 'tag'
		ELSE 'user'
	END AS target_type,
	COALESCE(mr.post_id, mr.comment_id, mr.image_asset_id, mr.tag_id, mr.user_id) AS target_id,
	CASE
		WHEN mr.post_id IS NOT NULL THEN concat(p.title, E'\n', p.content)
		WHEN mr.comment_id IS NOT NULL THEN c.content
		WHEN mr.image_asset_id IS NOT NULL THEN image.public_url
		WHEN mr.tag_id IS NOT NULL THEN tag.name
		WHEN mr.field = 'name' THEN target_user.name
		WHEN mr.field = 'bio' THEN target_user.bio
	END AS content`
