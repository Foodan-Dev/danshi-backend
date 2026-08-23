package service

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lib/pq"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

const maxCommentContentRunes = 2000

// CreateCommentInput 是评论或回复的创建输入。
type CreateCommentInput struct {
	Content          string
	ParentID         *uint64
	ReplyToUserID    *uint64
	MentionedUserIDs []uint64
}

// UpdateCommentInput 是评论正文与提及关系的全量编辑输入。
type UpdateCommentInput struct {
	Content          string
	MentionedUserIDs []uint64
}

// Create 创建评论主体、提及关联、审核流水与通知；首次创建不写编辑历史。
func (s *CommentService) Create(
	ctx context.Context,
	postID uint64,
	input CreateCommentInput,
	authorID uint64,
) (*CommentMutationResult, error) {
	content, mentionIDs, err := s.normalizeCommentPayload(ctx, input.Content, input.MentionedUserIDs)
	if err != nil {
		return nil, err
	}
	post, err := s.posts.LockByID(ctx, postID, repository.QueryOptions{IncludeDeleted: true})
	if err != nil {
		return nil, repository.ToAPIError(err, apierr.BizPostNotFound, "帖子")
	}
	if post.DeletedAt != nil {
		return nil, apierr.NotFound(apierr.BizPostDeleted, "帖子")
	}
	if post.Status != model.PostStatusApproved {
		return nil, apierr.Conflict(apierr.BizPostNotPublished, "帖子尚未发布")
	}
	parent, rootID, replyToUserID, err := s.resolveCommentParent(ctx, postID, post.AuthorID, input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	comment := &model.Comment{
		PostID: postID, AuthorID: authorID, ParentID: input.ParentID, RootID: rootID,
		ReplyToUserID: replyToUserID, Content: content,
		Moderation: model.ModerationStatusPending, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.comments.Create(ctx, comment); err != nil {
		return nil, apierr.Internal(err)
	}
	if err := s.comments.ReplaceMentions(ctx, comment.ID, mentionIDs); err != nil {
		return nil, apierr.Internal(err)
	}
	moderation, err := s.moderateComment(ctx, comment.ID, content)
	if err != nil {
		return nil, err
	}
	if moderation == model.ModerationStatusPass {
		rows := createCommentNotifications(comment, parent, post.AuthorID, mentionIDs)
		if err := s.comments.CreateNotifications(ctx, rows); err != nil {
			return nil, apierr.Internal(err)
		}
	}
	return s.commentMutationResult(ctx, comment.ID, post.AuthorID, authorID)
}

// Update 串行化评论编辑，先追加被替换的当前正文，再更新评论。
func (s *CommentService) Update(
	ctx context.Context,
	commentID uint64,
	input UpdateCommentInput,
	authorID uint64,
) (*CommentMutationResult, error) {
	content, mentionIDs, err := s.normalizeCommentPayload(ctx, input.Content, input.MentionedUserIDs)
	if err != nil {
		return nil, err
	}
	comment, err := s.comments.LockByID(ctx, commentID)
	if err != nil {
		return nil, commentRepositoryError(err)
	}
	if comment.AuthorID != authorID {
		return nil, apierr.Forbidden(apierr.BizNotOwner, "只能修改自己的评论")
	}
	if comment.DeletedAt != nil && (comment.DeletedReason == nil ||
		*comment.DeletedReason != model.DeleteReasonModeration) {
		return nil, apierr.NotFound(apierr.BizCommentDeleted, "评论")
	}
	post, err := s.posts.FindByID(ctx, comment.PostID, repository.QueryOptions{IncludeDeleted: true})
	if err != nil {
		return nil, repository.ToAPIError(err, apierr.BizPostNotFound, "帖子")
	}
	if post.DeletedAt != nil {
		return nil, apierr.NotFound(apierr.BizPostDeleted, "帖子")
	}
	oldMentionIDs, err := s.comments.MentionIDs(ctx, commentID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	now := time.Now().UTC()
	revision, err := s.comments.NextHistoryRevision(ctx, commentID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	history := &model.CommentHistory{
		CommentID: commentID, Revision: revision,
		EditedBy: authorID, EditedAt: now, Content: comment.Content,
	}
	if err := s.comments.CreateHistory(ctx, history); err != nil {
		return nil, commentHistoryWriteError(err)
	}
	if err := s.comments.UpdateContent(ctx, commentID, content, now); err != nil {
		return nil, commentRepositoryError(err)
	}
	if err := s.comments.ReplaceMentions(ctx, commentID, mentionIDs); err != nil {
		return nil, apierr.Internal(err)
	}
	moderation, err := s.moderateComment(ctx, commentID, content)
	if err != nil {
		return nil, err
	}
	if moderation == model.ModerationStatusPass {
		newMentions := differenceIDs(mentionIDs, oldMentionIDs)
		rows := mentionNotifications(commentID, authorID, content, newMentions)
		if err := s.comments.CreateNotifications(ctx, rows); err != nil {
			return nil, apierr.Internal(err)
		}
	}
	return s.commentMutationResult(ctx, commentID, post.AuthorID, authorID)
}

// Delete 软删除作者自己的单条评论，真实回复链与子回复保持不变。
func (s *CommentService) Delete(ctx context.Context, commentID, authorID uint64) error {
	comment, err := s.comments.LockByID(ctx, commentID)
	if err != nil {
		return commentRepositoryError(err)
	}
	if comment.DeletedAt != nil {
		return apierr.NotFound(apierr.BizCommentDeleted, "评论")
	}
	if comment.AuthorID != authorID {
		return apierr.Forbidden(apierr.BizNotOwner, "只能删除自己的评论")
	}
	now := time.Now().UTC()
	if err := s.comments.SoftDelete(
		ctx, commentID, model.DeleteReasonAuthor, &authorID, now,
	); err != nil {
		return commentRepositoryError(err)
	}
	return nil
}

func (s *CommentService) resolveCommentParent(
	ctx context.Context,
	postID uint64,
	postAuthorID uint64,
	input CreateCommentInput,
) (*model.Comment, *uint64, uint64, error) {
	if input.ParentID == nil {
		if err := validateReplyToUser(input.ReplyToUserID, postAuthorID); err != nil {
			return nil, nil, 0, err
		}
		return nil, nil, postAuthorID, nil
	}
	if *input.ParentID == 0 {
		return nil, nil, 0, apierr.InvalidField(
			"parent_id", apierr.FieldInvalidFormat, "parent_id 必须是正整数",
		)
	}
	parent, err := s.comments.LockByID(ctx, *input.ParentID)
	if err != nil {
		return nil, nil, 0, commentRepositoryError(err)
	}
	if parent.DeletedAt != nil {
		return nil, nil, 0, apierr.NotFound(apierr.BizCommentDeleted, "父评论")
	}
	if parent.Moderation != model.ModerationStatusPass {
		return nil, nil, 0, apierr.NotFound(apierr.BizCommentNotFound, "父评论")
	}
	if parent.PostID != postID {
		return nil, nil, 0, apierr.InvalidField(
			"parent_id", apierr.FieldConflict, "父评论不属于当前帖子",
		)
	}
	rootID := parent.ID
	if parent.RootID != nil {
		rootID = *parent.RootID
		root, rootErr := s.comments.FindByID(ctx, rootID)
		if rootErr != nil {
			return nil, nil, 0, commentRepositoryError(rootErr)
		}
		if root.DeletedAt != nil || root.Moderation != model.ModerationStatusPass {
			return nil, nil, 0, apierr.NotFound(apierr.BizCommentNotFound, "父评论")
		}
	}
	if err := validateReplyToUser(input.ReplyToUserID, parent.AuthorID); err != nil {
		return nil, nil, 0, err
	}
	return parent, &rootID, parent.AuthorID, nil
}

func (s *CommentService) normalizeCommentPayload(
	ctx context.Context,
	rawContent string,
	rawMentionIDs []uint64,
) (string, []uint64, error) {
	content := strings.TrimSpace(rawContent)
	if content == "" {
		return "", nil, apierr.InvalidField("content", apierr.FieldRequired, "content 不能为空")
	}
	if utf8.RuneCountInString(content) > maxCommentContentRunes {
		return "", nil, apierr.InvalidField(
			"content", apierr.FieldTooLong, "content 不能超过 2000 个字符",
		)
	}
	for _, userID := range rawMentionIDs {
		if userID == 0 {
			return "", nil, apierr.InvalidField(
				"mentioned_user_ids", apierr.FieldInvalidFormat, "用户 id 必须是正整数",
			)
		}
	}
	mentionIDs := uniqueIDs(rawMentionIDs)
	found, err := s.comments.ActiveUsers(ctx, mentionIDs)
	if err != nil {
		return "", nil, apierr.Internal(err)
	}
	if len(found) != len(mentionIDs) {
		return "", nil, apierr.InvalidField(
			"mentioned_user_ids", apierr.FieldConflict, "包含不存在或已注销的用户",
		)
	}
	for _, user := range found {
		if user.Name == "" || !strings.Contains(content, "@"+user.Name) {
			return "", nil, apierr.InvalidField(
				"mentioned_user_ids", apierr.FieldConflict,
				"被提及用户必须以 @昵称 的形式出现在评论正文中",
			)
		}
	}
	return content, mentionIDs, nil
}

func (s *CommentService) moderateComment(
	ctx context.Context,
	commentID uint64,
	content string,
) (model.ModerationStatus, error) {
	result, err := s.moderator.Review(ctx, ModerationRequest{
		Target: ModerationTargetComment, Text: content,
	})
	if err != nil {
		return "", err
	}
	if err := validateModerationResult(result); err != nil {
		return "", err
	}
	record := moderationRecordForComment(commentID, result)
	if err := s.comments.CreateModerationRecord(ctx, record); err != nil {
		return "", apierr.Internal(err)
	}
	if result.Verdict != model.ModerationVerdictPass {
		s.alerter.Alert(ctx, ModerationAlert{
			Target: ModerationTargetComment, TargetID: commentID, Provider: result.Provider,
			ProviderJobID: result.ProviderJobID, Verdict: result.Verdict,
			Labels: append([]string{}, result.Labels...),
		})
	}
	moderation := model.ModerationStatus(result.Verdict)
	if err := s.comments.ApplyModeration(ctx, commentID, moderation, time.Now().UTC()); err != nil {
		return "", commentRepositoryError(err)
	}
	return moderation, nil
}

func (s *CommentService) commentMutationResult(
	ctx context.Context,
	commentID uint64,
	postAuthorID uint64,
	currentUserID uint64,
) (*CommentMutationResult, error) {
	comment, err := s.comments.FindByID(ctx, commentID)
	if err != nil {
		return nil, commentRepositoryError(err)
	}
	relations, err := s.comments.LoadRelations(ctx, []model.Comment{*comment}, currentUserID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return &CommentMutationResult{
		Comment: buildCommentItem(comment, postAuthorID, comment.RootID == nil, relations),
	}, nil
}

func moderationRecordForComment(
	commentID uint64,
	result ModerationResult,
) *model.ModerationRecord {
	labels := pq.StringArray(result.Labels)
	if labels == nil {
		labels = pq.StringArray{}
	}
	return &model.ModerationRecord{
		CommentID: &commentID,
		Scene:     model.ModerationSceneText, Provider: result.Provider,
		ProviderJobID: result.ProviderJobID, Verdict: result.Verdict,
		Labels: labels, Score: result.Score, RawResponse: result.RawResponse,
		CreatedAt: time.Now().UTC(),
	}
}

func validateReplyToUser(provided *uint64, expected uint64) error {
	if provided == nil || *provided == expected {
		return nil
	}
	return apierr.InvalidField(
		"reply_to_user_id", apierr.FieldConflict, "reply_to_user_id 必须由回复结构决定",
	)
}

func commentHistoryWriteError(err error) error {
	if repository.IsUniqueViolation(err, "uq_comment_histories_revision") {
		return apierr.Conflict(apierr.BizConflict, "评论版本冲突，请重试")
	}
	return apierr.Internal(err)
}

func uniqueIDs(values []uint64) []uint64 {
	seen := make(map[uint64]struct{}, len(values))
	result := make([]uint64, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func differenceIDs(values, existing []uint64) []uint64 {
	seen := make(map[uint64]struct{}, len(existing))
	for _, value := range existing {
		seen[value] = struct{}{}
	}
	result := make([]uint64, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; !exists {
			result = append(result, value)
		}
	}
	return result
}

func commentPreview(content string) string {
	runes := []rune(content)
	if len(runes) > 100 {
		runes = runes[:100]
	}
	return string(runes)
}

func createCommentNotifications(
	comment *model.Comment,
	parent *model.Comment,
	postAuthorID uint64,
	mentionIDs []uint64,
) []model.Notification {
	preview := commentPreview(comment.Content)
	rows := make([]model.Notification, 0, len(mentionIDs)+1)
	if parent == nil && postAuthorID != comment.AuthorID {
		postID := comment.PostID
		rows = append(rows, model.Notification{
			RecipientID: postAuthorID, SenderID: comment.AuthorID, Type: model.NotificationTypeComment,
			RelatedPostID: &postID, Content: &preview,
		})
	}
	if parent != nil && parent.AuthorID != comment.AuthorID {
		rootID := parent.ID
		if comment.RootID != nil {
			rootID = *comment.RootID
		}
		rows = append(rows, model.Notification{
			RecipientID: parent.AuthorID, SenderID: comment.AuthorID, Type: model.NotificationTypeReply,
			RelatedCommentID: &rootID, Content: &preview,
		})
	}
	return append(rows, mentionNotifications(comment.ID, comment.AuthorID, comment.Content, mentionIDs)...)
}

func mentionNotifications(
	commentID uint64,
	authorID uint64,
	content string,
	mentionIDs []uint64,
) []model.Notification {
	preview := commentPreview(content)
	rows := make([]model.Notification, 0, len(mentionIDs))
	for _, mentionID := range mentionIDs {
		if mentionID == authorID {
			continue
		}
		id := commentID
		rows = append(rows, model.Notification{
			RecipientID: mentionID, SenderID: authorID, Type: model.NotificationTypeMention,
			RelatedCommentID: &id, Content: &preview,
		})
	}
	return rows
}
