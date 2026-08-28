package service

import (
	"context"
	"errors"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/authz"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/pagination"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/ptime"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

const commentReplyPreviewLimit = 2

// CommentService 实现真实回复链、按楼拍扁展示与评论版本协议。
type CommentService struct {
	comments  repository.CommentRepository
	posts     repository.PostRepository
	moderator ContentModerator
	alerter   ModerationAlerter
	rootTime  *pagination.CursorCodec
	rootHot   *pagination.CursorCodec
	replies   *pagination.CursorCodec
}

// NewCommentService 创建评论服务。
func NewCommentService(moderator ContentModerator, alerters ...ModerationAlerter) *CommentService {
	return newCommentService(
		moderator,
		pagination.NewEphemeralCursorCodec("comments.roots.time"),
		pagination.NewEphemeralCursorCodec("comments.roots.hot"),
		pagination.NewEphemeralCursorCodec("comments.replies"),
		alerters...,
	)
}

// NewCommentServiceWithCursorSecret 创建跨实例可互认评论游标的服务。
func NewCommentServiceWithCursorSecret(
	moderator ContentModerator,
	cursorSecret string,
	alerters ...ModerationAlerter,
) *CommentService {
	return newCommentService(
		moderator,
		pagination.NewCursorCodec(cursorSecret, "comments.roots.time"),
		pagination.NewCursorCodec(cursorSecret, "comments.roots.hot"),
		pagination.NewCursorCodec(cursorSecret, "comments.replies"),
		alerters...,
	)
}

func newCommentService(
	moderator ContentModerator,
	rootTime *pagination.CursorCodec,
	rootHot *pagination.CursorCodec,
	replies *pagination.CursorCodec,
	alerters ...ModerationAlerter,
) *CommentService {
	if moderator == nil {
		moderator = UnavailableContentModerator{}
	}
	alerter := ModerationAlerter(DiscardModerationAlerter{})
	if len(alerters) > 0 && alerters[0] != nil {
		alerter = alerters[0]
	}
	return &CommentService{
		moderator: moderator, alerter: alerter,
		rootTime: rootTime, rootHot: rootHot, replies: replies,
	}
}

// CommentAuthorView 是评论作者的公开信息。
type CommentAuthorView struct {
	ID          uint64  `json:"id"`
	Name        string  `json:"name"`
	AvatarURL   *string `json:"avatar_url"`
	IsFollowing bool    `json:"is_following"`
}

// MentionedUserView 是正文中被提及的用户。
type MentionedUserView struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
}

// ReplyToUserView 是真实回复链上直接被回复的用户。
type ReplyToUserView struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
}

// CommentItem 是楼主评论与拍扁回复共享的响应形态。
type CommentItem struct {
	ID             uint64                 `json:"id"`
	Content        string                 `json:"content"`
	Author         CommentAuthorView      `json:"author"`
	ReplyTo        *ReplyToUserView       `json:"reply_to"`
	MentionedUsers []MentionedUserView    `json:"mentioned_users"`
	LikeCount      int32                  `json:"like_count"`
	IsLiked        bool                   `json:"is_liked"`
	IsAuthor       bool                   `json:"is_author"`
	IsEdited       bool                   `json:"is_edited"`
	Moderation     model.ModerationStatus `json:"moderation"`
	CreatedAt      ptime.Time             `json:"created_at"`
	ReplyCount     int32                  `json:"reply_count"`
	Replies        []CommentItem          `json:"replies"`
}

// CommentList 是帖子楼主评论页及回复预览。
type CommentList struct {
	Comments   []CommentItem         `json:"comments"`
	Pagination pagination.CursorMeta `json:"pagination"`
}

// CommentReplies 是一楼内按真实 root_id 拍扁的回复页。
type CommentReplies struct {
	Replies    []CommentItem         `json:"replies"`
	Pagination pagination.CursorMeta `json:"pagination"`
}

// CommentMutationResult 是创建、编辑成功后的评论数据。
type CommentMutationResult struct {
	Comment CommentItem `json:"comment"`
}

// CommentLikeResult 是评论点赞动作的当前状态。
type CommentLikeResult struct {
	IsLiked   bool  `json:"is_liked"`
	LikeCount int32 `json:"like_count"`
}

// CommentHistoryView 是评论被替换的一版不可变正文。
type CommentHistoryView struct {
	ID         uint64                 `json:"id"`
	Revision   int32                  `json:"revision"`
	EditedBy   uint64                 `json:"edited_by"`
	EditedAt   ptime.Time             `json:"edited_at"`
	Content    string                 `json:"content"`
	Moderation *HistoryModerationView `json:"moderation"`
}

// CommentHistoryList 是按版本倒序的评论历史。
type CommentHistoryList struct {
	Histories []CommentHistoryView `json:"histories"`
}

// List 返回一帖的楼主评论，并为每楼批量附带最新两条回复。
func (s *CommentService) List(
	ctx context.Context,
	postID uint64,
	currentUserID uint64,
	sortBy string,
	request pagination.CursorRequest,
) (*CommentList, error) {
	post, err := s.visiblePost(ctx, postID, currentUserID)
	if err != nil {
		return nil, err
	}
	sortBy, err = normalizeCommentSort(sortBy)
	if err != nil {
		return nil, err
	}
	codec := s.rootTime
	if sortBy == "hot" {
		codec = s.rootHot
	}
	params, err := codec.DecodeRequest(request)
	if err != nil {
		return nil, err
	}
	if sortBy == "hot" && params.After != nil && params.After.Rank == nil {
		return nil, apierr.InvalidField("cursor", apierr.FieldInvalidFormat, "cursor 无效或已被篡改")
	}
	roots, hasMore, err := s.comments.FindRootPage(ctx, postID, currentUserID, sortBy, params)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	rootIDs := make([]uint64, 0, len(roots))
	for _, root := range roots {
		rootIDs = append(rootIDs, root.ID)
	}
	previews, err := s.comments.FindReplyPreviews(
		ctx, rootIDs, currentUserID, commentReplyPreviewLimit,
	)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	items, err := s.buildCommentList(ctx, roots, previews, post.AuthorID, currentUserID)
	if err != nil {
		return nil, err
	}
	meta, err := commentCursorMeta(codec, roots, params.Limit, hasMore, sortBy == "hot")
	if err != nil {
		return nil, err
	}
	return &CommentList{Comments: items, Pagination: meta}, nil
}

// Replies 返回指定楼主评论下按 root_id 归集的全部回复。
func (s *CommentService) Replies(
	ctx context.Context,
	commentID uint64,
	currentUserID uint64,
	request pagination.CursorRequest,
) (*CommentReplies, error) {
	root, err := s.comments.FindByID(ctx, commentID)
	if err != nil {
		return nil, commentRepositoryError(err)
	}
	if !commentVisibleToViewer(root, currentUserID) {
		return nil, apierr.NotFound(apierr.BizCommentNotFound, "评论")
	}
	if root.RootID != nil {
		return nil, apierr.InvalidField("comment_id", apierr.FieldConflict, "只能查询楼主评论的回复")
	}
	post, err := s.visiblePost(ctx, root.PostID, currentUserID)
	if err != nil {
		return nil, err
	}
	params, err := s.replies.DecodeRequest(request)
	if err != nil {
		return nil, err
	}
	replies, hasMore, err := s.comments.FindReplyPage(ctx, root.ID, currentUserID, params)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	relations, err := s.comments.LoadRelations(ctx, replies, currentUserID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	items := make([]CommentItem, 0, len(replies))
	for index := range replies {
		items = append(items, buildCommentItem(&replies[index], post.AuthorID, false, relations))
	}
	meta, err := commentCursorMeta(s.replies, replies, params.Limit, hasMore, false)
	if err != nil {
		return nil, err
	}
	return &CommentReplies{Replies: items, Pagination: meta}, nil
}

// Histories 允许作者与具备内容审核能力的管理员查看评论旧版本历史。
func (s *CommentService) Histories(
	ctx context.Context,
	commentID uint64,
	principal *Principal,
) (*CommentHistoryList, error) {
	comment, err := s.comments.FindByID(ctx, commentID)
	if err != nil {
		return nil, commentRepositoryError(err)
	}
	isModerator := authz.HasCapability(principal.User.Roles, authz.CapReviewContent)
	if comment.AuthorID != principal.User.ID && !isModerator {
		return nil, apierr.Forbidden(apierr.BizNotOwner, "只能查看自己的评论编辑历史")
	}
	rows, err := s.comments.ListHistories(ctx, commentID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	moderationRows, err := s.comments.ListHistoryModeration(ctx, commentID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	moderationByRevision := historyModerationByRevision(moderationRows, isModerator)
	histories := make([]CommentHistoryView, 0, len(rows))
	for _, row := range rows {
		histories = append(histories, CommentHistoryView{
			ID: row.ID, Revision: row.Revision, EditedBy: row.EditedBy,
			EditedAt: ptime.Time(row.EditedAt), Content: row.Content,
			Moderation: moderationByRevision[row.Revision],
		})
	}
	return &CommentHistoryList{Histories: histories}, nil
}

// Like 幂等创建点赞动作，计数由数据库触发器维护。
func (s *CommentService) Like(ctx context.Context, commentID, userID uint64) (*CommentLikeResult, error) {
	comment, err := s.interactiveComment(ctx, commentID)
	if err != nil {
		return nil, err
	}
	created, err := s.comments.Like(ctx, userID, commentID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if created && comment.AuthorID != userID {
		row := model.Notification{
			RecipientID: comment.AuthorID, SenderID: userID, Type: model.NotificationTypeLikeComment,
			RelatedCommentID: &comment.ID,
		}
		if err := s.comments.CreateNotifications(ctx, []model.Notification{row}); err != nil {
			return nil, apierr.Internal(err)
		}
	}
	count, err := s.comments.LikeCount(ctx, commentID)
	if err != nil {
		return nil, commentRepositoryError(err)
	}
	return &CommentLikeResult{IsLiked: true, LikeCount: count}, nil
}

// Unlike 幂等物理删除点赞动作。
func (s *CommentService) Unlike(ctx context.Context, commentID, userID uint64) (*CommentLikeResult, error) {
	if _, err := s.interactiveComment(ctx, commentID); err != nil {
		return nil, err
	}
	if err := s.comments.Unlike(ctx, userID, commentID); err != nil {
		return nil, apierr.Internal(err)
	}
	count, err := s.comments.LikeCount(ctx, commentID)
	if err != nil {
		return nil, commentRepositoryError(err)
	}
	return &CommentLikeResult{IsLiked: false, LikeCount: count}, nil
}

func (s *CommentService) visiblePost(
	ctx context.Context,
	postID uint64,
	currentUserID uint64,
) (*repository.PostRecord, error) {
	post, err := s.posts.FindByID(ctx, postID, repository.QueryOptions{IncludeDeleted: true})
	if err != nil {
		return nil, repository.ToAPIError(err, apierr.BizPostNotFound, "帖子")
	}
	if post.DeletedAt != nil {
		return nil, apierr.NotFound(apierr.BizPostDeleted, "帖子")
	}
	if post.Status != model.PostStatusApproved && post.AuthorID != currentUserID {
		return nil, apierr.NotFound(apierr.BizPostNotPublished, "帖子")
	}
	return post, nil
}

func (s *CommentService) interactiveComment(
	ctx context.Context,
	commentID uint64,
) (*model.Comment, error) {
	comment, err := s.comments.FindByID(ctx, commentID)
	if err != nil {
		return nil, commentRepositoryError(err)
	}
	if comment.DeletedAt != nil {
		return nil, apierr.NotFound(apierr.BizCommentNotFound, "评论")
	}
	if comment.Moderation != model.ModerationStatusPass {
		return nil, apierr.NotFound(apierr.BizCommentNotFound, "评论")
	}
	if comment.RootID != nil {
		root, rootErr := s.comments.FindByID(ctx, *comment.RootID)
		if rootErr != nil {
			return nil, commentRepositoryError(rootErr)
		}
		if root.DeletedAt != nil || root.Moderation != model.ModerationStatusPass {
			return nil, apierr.NotFound(apierr.BizCommentNotFound, "评论")
		}
	}
	post, err := s.posts.FindByID(ctx, comment.PostID, repository.QueryOptions{IncludeDeleted: true})
	if err != nil {
		return nil, repository.ToAPIError(err, apierr.BizPostNotFound, "帖子")
	}
	if post.DeletedAt != nil || post.Status != model.PostStatusApproved {
		return nil, apierr.Conflict(apierr.BizPostNotPublished, "帖子尚未发布")
	}
	return comment, nil
}

func commentVisibleToViewer(comment *model.Comment, currentUserID uint64) bool {
	if comment.DeletedAt == nil && comment.Moderation == model.ModerationStatusPass {
		return true
	}
	if comment.AuthorID != currentUserID || comment.Moderation == model.ModerationStatusPass {
		return false
	}
	return comment.DeletedAt == nil || comment.DeletedReason != nil &&
		*comment.DeletedReason == model.DeleteReasonModeration
}

func commentCursorMeta(
	codec *pagination.CursorCodec,
	comments []model.Comment,
	limit int,
	hasMore bool,
	ranked bool,
) (pagination.CursorMeta, error) {
	meta := pagination.CursorMeta{Limit: limit, HasMore: hasMore}
	if !hasMore || len(comments) == 0 {
		return meta, nil
	}
	last := comments[len(comments)-1]
	cursor := pagination.Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
	if ranked {
		rank := int64(last.LikeCount)
		cursor.Rank = &rank
	}
	token, err := codec.Encode(cursor)
	if err != nil {
		return pagination.CursorMeta{}, apierr.Internal(err)
	}
	meta.NextCursor = &token
	return meta, nil
}

func normalizeCommentSort(value string) (string, error) {
	if value == "" {
		return "latest", nil
	}
	if value != "latest" && value != "hot" {
		return "", apierr.InvalidField("sort_by", apierr.FieldInvalidEnum, "sort_by 取值不合法")
	}
	return value, nil
}

func commentRepositoryError(err error) error {
	if errors.Is(err, repository.ErrNotFound) {
		return apierr.NotFound(apierr.BizCommentNotFound, "评论")
	}
	return apierr.Internal(err)
}
