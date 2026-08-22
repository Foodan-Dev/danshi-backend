package service

import (
	"context"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/ptime"
	"github.com/jingyijun/danshi_backend_go/internal/repository"
)

func (s *CommentService) buildCommentList(
	ctx context.Context,
	roots []model.Comment,
	previews []model.Comment,
	postAuthorID uint64,
	currentUserID uint64,
) ([]CommentItem, error) {
	all := make([]model.Comment, 0, len(roots)+len(previews))
	all = append(all, roots...)
	all = append(all, previews...)
	relations, err := s.comments.LoadRelations(ctx, all, currentUserID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	previewMap := make(map[uint64][]CommentItem, len(roots))
	for index := range previews {
		preview := &previews[index]
		if preview.RootID == nil {
			continue
		}
		previewMap[*preview.RootID] = append(
			previewMap[*preview.RootID],
			buildCommentItem(preview, postAuthorID, false, relations),
		)
	}
	items := make([]CommentItem, 0, len(roots))
	for index := range roots {
		item := buildCommentItem(&roots[index], postAuthorID, true, relations)
		item.Replies = previewMap[roots[index].ID]
		if item.Replies == nil {
			item.Replies = []CommentItem{}
		}
		items = append(items, item)
	}
	return items, nil
}

func buildCommentItem(
	comment *model.Comment,
	postAuthorID uint64,
	isRoot bool,
	relations repository.CommentRelations,
) CommentItem {
	content := comment.Content
	mentionedUsers := buildMentionedUsers(comment.ID, relations)
	if comment.DeletedAt != nil {
		content = deletedCommentContent
		mentionedUsers = []MentionedUserView{}
	}
	item := CommentItem{
		ID: comment.ID, Content: content,
		Author:         buildCommentAuthor(comment.AuthorID, relations),
		MentionedUsers: mentionedUsers, LikeCount: comment.LikeCount,
		IsLiked: relations.Liked[comment.ID], IsAuthor: comment.AuthorID == postAuthorID,
		IsEdited: relations.Revisions[comment.ID] > 1, IsDeleted: comment.DeletedAt != nil,
		CreatedAt: ptime.Time(comment.CreatedAt), Replies: []CommentItem{},
	}
	if isRoot {
		item.ReplyCount = comment.ReplyCount
	} else {
		item.ReplyTo = buildReplyTo(comment.ReplyToUserID, relations)
	}
	return item
}

func buildCommentAuthor(
	userID uint64,
	relations repository.CommentRelations,
) CommentAuthorView {
	user, exists := relations.Users[userID]
	if !exists || user.DeletedAt != nil {
		return CommentAuthorView{
			ID: userID, Name: "已注销用户", IsFollowing: relations.Following[userID],
		}
	}
	return CommentAuthorView{
		ID: userID, Name: user.Name, AvatarURL: user.AvatarURL,
		IsFollowing: relations.Following[userID],
	}
}

func buildReplyTo(userID uint64, relations repository.CommentRelations) *ReplyToUserView {
	user, exists := relations.Users[userID]
	name := "已注销用户"
	if exists && user.DeletedAt == nil {
		name = user.Name
	}
	return &ReplyToUserView{ID: userID, Name: name}
}

func buildMentionedUsers(
	commentID uint64,
	relations repository.CommentRelations,
) []MentionedUserView {
	userIDs := relations.Mentions[commentID]
	users := make([]MentionedUserView, 0, len(userIDs))
	for _, userID := range userIDs {
		user, exists := relations.Users[userID]
		name := "已注销用户"
		if exists && user.DeletedAt == nil {
			name = user.Name
		}
		users = append(users, MentionedUserView{ID: userID, Name: name})
	}
	return users
}
