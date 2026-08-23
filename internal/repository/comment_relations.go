package repository

import (
	"context"
	"time"

	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
)

// CommentUserRow 是评论展示所需的用户与头像批量查询结果。
type CommentUserRow struct {
	ID        uint64
	Name      string
	DeletedAt *time.Time
	AvatarURL *string
}

// CommentRelations 是一批评论的提及、用户与当前用户互动状态。
type CommentRelations struct {
	Mentions  map[uint64][]uint64
	Users     map[uint64]CommentUserRow
	Liked     map[uint64]bool
	Following map[uint64]bool
	Revisions map[uint64]int32
}

// LoadRelations 用固定数量批量查询加载评论展示关系，查询数不随评论数增长。
func (CommentRepository) LoadRelations(
	ctx context.Context,
	comments []model.Comment,
	currentUserID uint64,
) (CommentRelations, error) {
	relations := CommentRelations{
		Mentions: make(map[uint64][]uint64), Users: make(map[uint64]CommentUserRow),
		Liked: make(map[uint64]bool), Following: make(map[uint64]bool),
		Revisions: make(map[uint64]int32),
	}
	if len(comments) == 0 {
		return relations, nil
	}
	commentIDs, userIDs, authorIDs := commentRelationIDs(comments)
	if err := loadCommentMentions(ctx, commentIDs, &relations, &userIDs); err != nil {
		return CommentRelations{}, err
	}
	if err := loadCommentUsers(ctx, userIDs, &relations); err != nil {
		return CommentRelations{}, err
	}
	if err := loadCommentRevisions(ctx, commentIDs, &relations); err != nil {
		return CommentRelations{}, err
	}
	if currentUserID == 0 {
		return relations, nil
	}
	if err := loadCommentLikes(ctx, currentUserID, commentIDs, &relations); err != nil {
		return CommentRelations{}, err
	}
	if err := loadCommentFollows(ctx, currentUserID, authorIDs, &relations); err != nil {
		return CommentRelations{}, err
	}
	return relations, nil
}

func loadCommentRevisions(
	ctx context.Context,
	commentIDs []uint64,
	relations *CommentRelations,
) error {
	var rows []struct {
		CommentID uint64
		Revision  int32
	}
	err := db.FromContext(ctx).Model(&model.CommentHistory{}).
		Select("comment_id, max(revision) AS revision").Where("comment_id IN ?", commentIDs).
		Group("comment_id").Scan(&rows).Error
	for _, row := range rows {
		relations.Revisions[row.CommentID] = row.Revision
	}
	return err
}

func commentRelationIDs(comments []model.Comment) ([]uint64, []uint64, []uint64) {
	commentIDs := make([]uint64, 0, len(comments))
	userIDs := make([]uint64, 0, len(comments)*2)
	authorIDs := make([]uint64, 0, len(comments))
	for _, comment := range comments {
		commentIDs = append(commentIDs, comment.ID)
		userIDs = append(userIDs, comment.AuthorID, comment.ReplyToUserID)
		authorIDs = append(authorIDs, comment.AuthorID)
	}
	return uniqueSortedIDs(commentIDs), uniqueSortedIDs(userIDs), uniqueSortedIDs(authorIDs)
}

func loadCommentMentions(
	ctx context.Context,
	commentIDs []uint64,
	relations *CommentRelations,
	userIDs *[]uint64,
) error {
	var rows []model.CommentMention
	err := db.FromContext(ctx).Where("comment_id IN ?", commentIDs).
		Order("comment_id, user_id").Find(&rows).Error
	if err != nil {
		return err
	}
	for _, row := range rows {
		relations.Mentions[row.CommentID] = append(relations.Mentions[row.CommentID], row.UserID)
		*userIDs = append(*userIDs, row.UserID)
	}
	*userIDs = uniqueSortedIDs(*userIDs)
	return nil
}

func loadCommentUsers(ctx context.Context, userIDs []uint64, relations *CommentRelations) error {
	if len(userIDs) == 0 {
		return nil
	}
	var rows []CommentUserRow
	err := db.FromContext(ctx).Table("users AS u").
		Select("u.id, u.name, u.deleted_at, avatar.public_url AS avatar_url").
		Joins("LEFT JOIN image_assets AS avatar ON avatar.id = u.avatar_image_asset_id").
		Where("u.id IN ?", userIDs).Order("u.id").Scan(&rows).Error
	for _, row := range rows {
		relations.Users[row.ID] = row
	}
	return err
}

func loadCommentLikes(
	ctx context.Context,
	currentUserID uint64,
	commentIDs []uint64,
	relations *CommentRelations,
) error {
	var liked []uint64
	err := db.FromContext(ctx).Model(&model.CommentLike{}).
		Where("user_id = ? AND comment_id IN ?", currentUserID, commentIDs).
		Pluck("comment_id", &liked).Error
	for _, commentID := range liked {
		relations.Liked[commentID] = true
	}
	return err
}

func loadCommentFollows(
	ctx context.Context,
	currentUserID uint64,
	authorIDs []uint64,
	relations *CommentRelations,
) error {
	if len(authorIDs) == 0 {
		return nil
	}
	var following []uint64
	err := db.FromContext(ctx).Model(&model.Follow{}).
		Where("follower_id = ? AND following_id IN ?", currentUserID, authorIDs).
		Pluck("following_id", &following).Error
	for _, authorID := range following {
		relations.Following[authorID] = true
	}
	return err
}
