package repository

import (
	"context"
	"time"

	"gorm.io/gorm/clause"

	"github.com/jingyijun/danshi_backend_go/internal/infra/db"
	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/pagination"
)

// UserProfileRecord 是用户主页主体、头像与统计数据的一次查询结果。
type UserProfileRecord struct {
	model.User
	AvatarURL      *string `gorm:"column:avatar_url"`
	PostCount      int64
	LikeCount      int64
	FavoriteCount  int64
	FollowerCount  int64
	FollowingCount int64
	IsFollowing    bool
}

// UserListRecord 是关注/粉丝列表的一行公开用户信息。
type UserListRecord struct {
	ID             uint64
	Name           string
	AvatarURL      *string `gorm:"column:avatar_url"`
	Bio            *string
	PostCount      int64
	LikeCount      int64
	FavoriteCount  int64
	FollowerCount  int64
	FollowingCount int64
	IsFollowing    bool
}

// SearchPage 按昵称关键词分页返回当前可用用户。
func (UserRepository) SearchPage(
	ctx context.Context,
	keyword string,
	currentUserID uint64,
	params pagination.Params,
) ([]UserListRecord, pagination.Meta, error) {
	query := db.FromContext(ctx).Table("users AS u").Where(`
		u.deleted_at IS NULL
		AND NOT (u.ban_is_permanent OR COALESCE(u.banned_until > CURRENT_TIMESTAMP, false))
	`)
	if keyword != "" {
		query = query.Where(`u.name ILIKE ? ESCAPE '\'`, literalContainsPattern(keyword))
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, pagination.Meta{}, err
	}
	rows := make([]UserListRecord, 0, params.Limit)
	err := query.Select(userListColumns, currentUserID, currentUserID, currentUserID).
		Joins("LEFT JOIN image_assets AS avatar ON avatar.id = u.avatar_image_asset_id").
		Order("u.created_at DESC, u.id DESC").Offset(params.Offset()).Limit(params.Limit).
		Scan(&rows).Error
	if err != nil {
		return nil, pagination.Meta{}, err
	}
	return rows, pagination.NewMeta(params, total), nil
}

// FindByID 按 id 查询未注销用户。
func (UserRepository) FindByID(ctx context.Context, userID uint64) (*model.User, error) {
	var user model.User
	err := db.FromContext(ctx).Where("id = ? AND deleted_at IS NULL", userID).First(&user).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &user, nil
}

// LockByID 串行化同一用户的资料与头像更新。
func (UserRepository) LockByID(ctx context.Context, userID uint64) (*model.User, error) {
	var user model.User
	err := db.FromContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND deleted_at IS NULL", userID).First(&user).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &user, nil
}

// FindProfile 用一次 SELECT 返回用户主页、统计与当前访问者的关注状态。
func (UserRepository) FindProfile(
	ctx context.Context,
	userID uint64,
	currentUserID uint64,
) (*UserProfileRecord, error) {
	var record UserProfileRecord
	result := db.FromContext(ctx).Raw(
		userProfileSQL, currentUserID, currentUserID, currentUserID, userID,
	).Scan(&record)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, ErrNotFound
	}
	return &record, nil
}

// UpdateProfile 写入已经校验过的资料字段。
func (UserRepository) UpdateProfile(ctx context.Context, userID uint64, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	fields["updated_at"] = time.Now().UTC()
	result := db.FromContext(ctx).Model(&model.User{}).Where("id = ? AND deleted_at IS NULL", userID).
		Updates(fields)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateModerationRecord 追加昵称或简介的审核流水。
func (UserRepository) CreateModerationRecord(ctx context.Context, record *model.ModerationRecord) error {
	return db.FromContext(ctx).Create(record).Error
}

// Follow 幂等创建关注关系，并报告本次是否真正插入。
func (UserRepository) Follow(ctx context.Context, followerID, followingID uint64) (bool, error) {
	result := db.FromContext(ctx).Exec(`
		INSERT INTO follows (follower_id, following_id)
		VALUES (?, ?)
		ON CONFLICT DO NOTHING
	`, followerID, followingID)
	return result.RowsAffected == 1, result.Error
}

// Unfollow 幂等物理删除关注关系。
func (UserRepository) Unfollow(ctx context.Context, followerID, followingID uint64) error {
	return db.FromContext(ctx).Where(
		"follower_id = ? AND following_id = ?", followerID, followingID,
	).Delete(&model.Follow{}).Error
}

// FollowerCount 返回指定用户当前粉丝数。
func (UserRepository) FollowerCount(ctx context.Context, userID uint64) (int64, error) {
	var count int64
	err := db.FromContext(ctx).Model(&model.Follow{}).Where("following_id = ?", userID).Count(&count).Error
	return count, err
}

// FindFollowingPage 分页返回 userID 关注的用户。
func (UserRepository) FindFollowingPage(
	ctx context.Context,
	userID uint64,
	currentUserID uint64,
	params pagination.Params,
) ([]UserListRecord, pagination.Meta, error) {
	return findFollowPage(ctx, userID, currentUserID, params, true)
}

// FindFollowersPage 分页返回关注 userID 的用户。
func (UserRepository) FindFollowersPage(
	ctx context.Context,
	userID uint64,
	currentUserID uint64,
	params pagination.Params,
) ([]UserListRecord, pagination.Meta, error) {
	return findFollowPage(ctx, userID, currentUserID, params, false)
}

func findFollowPage(
	ctx context.Context,
	userID uint64,
	currentUserID uint64,
	params pagination.Params,
	following bool,
) ([]UserListRecord, pagination.Meta, error) {
	join, predicate := "JOIN users AS u ON u.id = f.follower_id", "f.following_id = ?"
	if following {
		join, predicate = "JOIN users AS u ON u.id = f.following_id", "f.follower_id = ?"
	}
	base := db.FromContext(ctx).Table("follows AS f").Joins(join).
		Where(predicate+" AND u.deleted_at IS NULL", userID)
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, pagination.Meta{}, err
	}

	rows := make([]UserListRecord, 0, params.Limit)
	query := base.Select(userListColumns, currentUserID, currentUserID, currentUserID).
		Joins("LEFT JOIN image_assets AS avatar ON avatar.id = u.avatar_image_asset_id").
		Order("f.created_at DESC, u.id DESC").Offset(params.Offset()).Limit(params.Limit)
	if err := query.Scan(&rows).Error; err != nil {
		return nil, pagination.Meta{}, err
	}
	return rows, pagination.NewMeta(params, total), nil
}

const userProfileSQL = `
	SELECT u.*, avatar.public_url AS avatar_url,
		(SELECT count(*) FROM posts p WHERE p.author_id = u.id AND p.deleted_at IS NULL) AS post_count,
		(SELECT COALESCE(sum(p.like_count), 0) FROM posts p
		 WHERE p.author_id = u.id AND p.deleted_at IS NULL) AS like_count,
		(SELECT COALESCE(sum(p.favorite_count), 0) FROM posts p
		 WHERE p.author_id = u.id AND p.deleted_at IS NULL) AS favorite_count,
		(SELECT count(*) FROM follows f WHERE f.following_id = u.id) AS follower_count,
		(SELECT count(*) FROM follows f WHERE f.follower_id = u.id) AS following_count,
		CASE WHEN ? = 0 OR ? = u.id THEN false ELSE EXISTS (
			SELECT 1 FROM follows f WHERE f.follower_id = ? AND f.following_id = u.id
		) END AS is_following
	FROM users u
	LEFT JOIN image_assets AS avatar ON avatar.id = u.avatar_image_asset_id
	WHERE u.id = ? AND u.deleted_at IS NULL`

const userListColumns = `
	u.id, u.name, u.bio, avatar.public_url AS avatar_url,
	(SELECT count(*) FROM posts p WHERE p.author_id = u.id AND p.deleted_at IS NULL) AS post_count,
	(SELECT COALESCE(sum(p.like_count), 0) FROM posts p
	 WHERE p.author_id = u.id AND p.deleted_at IS NULL) AS like_count,
	(SELECT COALESCE(sum(p.favorite_count), 0) FROM posts p
	 WHERE p.author_id = u.id AND p.deleted_at IS NULL) AS favorite_count,
	(SELECT count(*) FROM follows own_followers WHERE own_followers.following_id = u.id) AS follower_count,
	(SELECT count(*) FROM follows own_following WHERE own_following.follower_id = u.id) AS following_count,
	CASE WHEN ? = 0 OR ? = u.id THEN false ELSE EXISTS (
		SELECT 1 FROM follows viewer_follow
		WHERE viewer_follow.follower_id = ? AND viewer_follow.following_id = u.id
	) END AS is_following`
