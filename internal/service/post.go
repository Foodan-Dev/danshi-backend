package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/money"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/pagination"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/ptime"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

// BudgetRangeInput 是提问帖可选的预算区间。
type BudgetRangeInput struct {
	Min int32 `json:"min"`
	Max int32 `json:"max"`
}

// PreferencesInput 是提问帖希望具有或避开的口味名称。
type PreferencesInput struct {
	AvoidFlavors  []string `json:"avoid_flavors"`
	PreferFlavors []string `json:"prefer_flavors"`
}

// PostPayload 是创建、全量编辑共享的内容字段。
type PostPayload struct {
	Title           string
	Content         string
	Category        string
	ShareType       *string
	CanteenCode     *string
	CanteenWindowID *uint64
	Cuisine         *string
	Price           *money.Amount
	Flavors         []string
	Tags            []string
	Images          []string
	BudgetRange     *BudgetRangeInput
	Preferences     *PreferencesInput
}

// CreatePostInput 是创建帖子领域输入。
type CreatePostInput struct {
	PostPayload
	PostType string
	Status   *string
}

// UpdatePostInput 是帖子全量编辑领域输入。
type UpdatePostInput struct {
	PostPayload
	PostType   *string
	Status     *string
	EditReason *string
}

// ListPostsInput 是公开信息流筛选条件。
type ListPostsInput struct {
	PostType    string
	ShareType   string
	Category    string
	CanteenCode string
	Cuisine     string
	Flavors     []string
	Tags        []string
	MinPrice    *money.Amount
	MaxPrice    *money.Amount
	SortBy      string
	Pagination  pagination.Params
	Cursor      pagination.CursorRequest
}

// CanteenView 是帖子响应中的餐厅对象。
type CanteenView struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Campus string `json:"campus"`
}

// CanteenWindowView 是帖子响应中的窗口对象。
type CanteenWindowView struct {
	ID    uint64  `json:"id"`
	Name  string  `json:"name"`
	Floor *string `json:"floor"`
}

// PostAuthorView 是帖子作者的公开信息。
type PostAuthorView struct {
	ID          uint64  `json:"id"`
	Name        string  `json:"name"`
	AvatarURL   *string `json:"avatar_url"`
	IsFollowing *bool   `json:"is_following"`
}

// PostStatsView 是帖子计数器展示。
type PostStatsView struct {
	LikeCount     int32 `json:"like_count"`
	FavoriteCount int32 `json:"favorite_count"`
	CommentCount  int32 `json:"comment_count"`
	ViewCount     int32 `json:"view_count"`
}

// BudgetRangeView 是帖子响应中的预算区间。
type BudgetRangeView struct {
	Min int32 `json:"min"`
	Max int32 `json:"max"`
}

// PreferencesView 是帖子响应中的口味偏好。
type PreferencesView struct {
	AvoidFlavors  []string `json:"avoid_flavors"`
	PreferFlavors []string `json:"prefer_flavors"`
}

// PostListItem 是列表与详情共享的帖子响应。
type PostListItem struct {
	ID            uint64             `json:"id"`
	PostType      model.PostType     `json:"post_type"`
	ShareType     *model.ShareType   `json:"share_type"`
	Title         string             `json:"title"`
	Content       string             `json:"content"`
	Category      model.PostCategory `json:"category"`
	Canteen       *CanteenView       `json:"canteen"`
	CanteenWindow *CanteenWindowView `json:"canteen_window"`
	Cuisine       *string            `json:"cuisine"`
	Flavors       []string           `json:"flavors"`
	Tags          []string           `json:"tags"`
	Price         *money.Amount      `json:"price"`
	Images        []string           `json:"images"`
	Author        PostAuthorView     `json:"author"`
	Stats         PostStatsView      `json:"stats"`
	IsLiked       bool               `json:"is_liked"`
	IsFavorited   bool               `json:"is_favorited"`
	IsEdited      bool               `json:"is_edited"`
	Status        model.PostStatus   `json:"status"`
	CreatedAt     ptime.Time         `json:"created_at"`
	UpdatedAt     ptime.Time         `json:"updated_at"`
}

// PostDetail 是帖子详情响应。
type PostDetail struct {
	PostListItem
	BudgetRange *BudgetRangeView `json:"budget_range"`
	Preferences *PreferencesView `json:"preferences"`
}

// PostList 是帖子列表与分页信息。
type PostList struct {
	Posts      []PostListItem  `json:"posts"`
	Pagination pagination.Meta `json:"pagination"`
}

// PostFeedList 是 GET /posts 的列表响应；latest 与其它排序使用不同分页分支。
type PostFeedList struct {
	Posts      []PostListItem        `json:"posts"`
	Pagination pagination.HybridMeta `json:"pagination"`
}

// PostCreateResult 是创建、编辑成功后的稳定摘要。
type PostCreateResult struct {
	ID       uint64           `json:"id"`
	PostType model.PostType   `json:"post_type"`
	Status   model.PostStatus `json:"status"`
}

// PostLikeResult 是点赞动作响应。
type PostLikeResult struct {
	IsLiked   bool  `json:"is_liked"`
	LikeCount int32 `json:"like_count"`
}

// PostFavoriteResult 是收藏动作响应。
type PostFavoriteResult struct {
	IsFavorited   bool  `json:"is_favorited"`
	FavoriteCount int32 `json:"favorite_count"`
}

// PostHistoryView 是一条可展示的全量版本历史。
type PostHistoryView struct {
	ID         uint64          `json:"id"`
	Revision   int32           `json:"revision"`
	EditedBy   uint64          `json:"edited_by"`
	EditedAt   ptime.Time      `json:"edited_at"`
	Snapshot   json.RawMessage `json:"snapshot"`
	EditReason *string         `json:"edit_reason"`
}

// PostHistoryList 是作者查看帖子编辑历史的响应。
type PostHistoryList struct {
	Histories []PostHistoryView `json:"histories"`
}

// PostService 实现 Post 域业务协议。
type PostService struct {
	moderator     ContentModerator
	alerter       ModerationAlerter
	posts         repository.PostRepository
	notifications repository.NotificationRepository
	cursor        *pagination.CursorCodec
}

// NewPostService 创建帖子服务。
func NewPostService(moderator ContentModerator, alerters ...ModerationAlerter) *PostService {
	return newPostService(
		moderator, pagination.NewEphemeralCursorCodec("posts.latest"), alerters...,
	)
}

// NewPostServiceWithCursorSecret 创建使用运行时密钥认证游标的帖子服务。
func NewPostServiceWithCursorSecret(
	moderator ContentModerator,
	cursorSecret string,
	alerters ...ModerationAlerter,
) *PostService {
	return newPostService(
		moderator, pagination.NewCursorCodec(cursorSecret, "posts.latest"), alerters...,
	)
}

func newPostService(
	moderator ContentModerator,
	cursor *pagination.CursorCodec,
	alerters ...ModerationAlerter,
) *PostService {
	if moderator == nil {
		moderator = UnavailableContentModerator{}
	}
	alerter := ModerationAlerter(DiscardModerationAlerter{})
	if len(alerters) > 0 && alerters[0] != nil {
		alerter = alerters[0]
	}
	return &PostService{
		moderator: moderator, alerter: alerter, cursor: cursor,
	}
}

// List 返回公开且未删除的信息流，并批量预加载全部关联。
func (s *PostService) List(
	ctx context.Context,
	input ListPostsInput,
	currentUserID uint64,
) (*PostFeedList, error) {
	filter, err := normalizePostFilter(input)
	if err != nil {
		return nil, err
	}
	records, meta, err := s.findListPage(ctx, filter, input)
	if err != nil {
		return nil, err
	}
	relations, err := s.loadRelations(ctx, records, currentUserID)
	if err != nil {
		return nil, err
	}
	items := make([]PostListItem, 0, len(records))
	for index := range records {
		items = append(items, buildPostListItem(&records[index], relations, currentUserID))
	}
	return &PostFeedList{Posts: items, Pagination: meta}, nil
}

func (s *PostService) findListPage(
	ctx context.Context,
	filter repository.PostFilter,
	input ListPostsInput,
) ([]repository.PostRecord, pagination.HybridMeta, error) {
	if filter.SortBy != "latest" {
		records, meta, err := s.posts.FindPage(ctx, filter, input.Pagination)
		if err != nil {
			return nil, pagination.HybridMeta{}, apierr.Internal(err)
		}
		return records, pagination.NewOffsetHybridMeta(meta), nil
	}
	params, err := s.cursor.DecodeRequest(postCursorRequest(input))
	if err != nil {
		return nil, pagination.HybridMeta{}, err
	}
	records, hasMore, err := s.posts.FindLatestPage(ctx, filter, params)
	if err != nil {
		return nil, pagination.HybridMeta{}, apierr.Internal(err)
	}
	meta, err := s.postCursorMeta(records, params.Limit, hasMore)
	if err != nil {
		return nil, pagination.HybridMeta{}, err
	}
	return records, pagination.NewCursorHybridMeta(meta), nil
}

func postCursorRequest(input ListPostsInput) pagination.CursorRequest {
	request := input.Cursor
	if request.Limit != 0 {
		return request
	}
	request.Limit = input.Pagination.Limit
	if request.Limit == 0 {
		request.Limit = pagination.DefaultLimit
	}
	return request
}

func (s *PostService) postCursorMeta(
	records []repository.PostRecord,
	limit int,
	hasMore bool,
) (pagination.CursorMeta, error) {
	meta := pagination.CursorMeta{Limit: limit, HasMore: hasMore}
	if !hasMore || len(records) == 0 {
		return meta, nil
	}
	last := records[len(records)-1]
	token, err := s.cursor.Encode(pagination.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
	if err != nil {
		return pagination.CursorMeta{}, apierr.Internal(err)
	}
	meta.NextCursor = &token
	return meta, nil
}

// Get 返回详情并原子增加浏览数；浏览不会改变 updated_at。
func (s *PostService) Get(
	ctx context.Context,
	postID uint64,
	currentUserID uint64,
) (*PostDetail, error) {
	record, err := s.visibleRecord(ctx, postID, currentUserID)
	if err != nil {
		return nil, err
	}
	if err := s.posts.IncrementView(ctx, postID); err != nil {
		return nil, repository.ToAPIError(err, apierr.BizPostNotFound, "帖子")
	}
	record.ViewCount++
	relations, err := s.loadRelations(ctx, []repository.PostRecord{*record}, currentUserID)
	if err != nil {
		return nil, err
	}
	item := buildPostListItem(record, relations, currentUserID)
	detail := &PostDetail{PostListItem: item}
	if record.BudgetMin != nil && record.BudgetMax != nil {
		detail.BudgetRange = &BudgetRangeView{Min: *record.BudgetMin, Max: *record.BudgetMax}
	}
	detail.Preferences = buildPreferences(relations.Flavors[postID])
	return detail, nil
}

// Histories 仅允许作者查看未删除帖子的全量版本历史。
func (s *PostService) Histories(
	ctx context.Context,
	postID uint64,
	currentUserID uint64,
) (*PostHistoryList, error) {
	record, err := s.posts.FindByID(ctx, postID, repository.QueryOptions{IncludeDeleted: true})
	if err != nil {
		return nil, repository.ToAPIError(err, apierr.BizPostNotFound, "帖子")
	}
	if record.DeletedAt != nil {
		return nil, apierr.NotFound(apierr.BizPostDeleted, "帖子")
	}
	if record.AuthorID != currentUserID {
		return nil, apierr.Forbidden(apierr.BizNotOwner, "只能查看自己的帖子编辑历史")
	}
	rows, err := s.posts.ListHistories(ctx, postID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	histories := make([]PostHistoryView, 0, len(rows))
	for _, row := range rows {
		histories = append(histories, PostHistoryView{
			ID: row.ID, Revision: row.Revision, EditedBy: row.EditedBy,
			EditedAt: ptime.Time(row.EditedAt), Snapshot: row.Snapshot, EditReason: row.EditReason,
		})
	}
	return &PostHistoryList{Histories: histories}, nil
}

// Like 幂等点赞并返回触发器维护后的计数。
func (s *PostService) Like(ctx context.Context, postID, userID uint64) (*PostLikeResult, error) {
	post, err := s.interactivePost(ctx, postID)
	if err != nil {
		return nil, err
	}
	created, err := s.posts.Like(ctx, userID, postID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if created && post.AuthorID != userID {
		notification := &model.Notification{
			RecipientID: post.AuthorID, SenderID: userID, Type: model.NotificationTypeLikePost,
			RelatedPostID: &post.ID,
		}
		if err := s.notifications.Create(ctx, notification); err != nil {
			return nil, apierr.Internal(err)
		}
	}
	likeCount, _, err := s.posts.Counters(ctx, postID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return &PostLikeResult{IsLiked: true, LikeCount: likeCount}, nil
}

// Unlike 幂等取消点赞。
func (s *PostService) Unlike(ctx context.Context, postID, userID uint64) (*PostLikeResult, error) {
	if err := s.ensureInteractive(ctx, postID); err != nil {
		return nil, err
	}
	if err := s.posts.Unlike(ctx, userID, postID); err != nil {
		return nil, apierr.Internal(err)
	}
	likeCount, _, err := s.posts.Counters(ctx, postID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return &PostLikeResult{IsLiked: false, LikeCount: likeCount}, nil
}

// Favorite 幂等收藏并返回触发器维护后的计数。
func (s *PostService) Favorite(
	ctx context.Context,
	postID uint64,
	userID uint64,
) (*PostFavoriteResult, error) {
	if err := s.ensureInteractive(ctx, postID); err != nil {
		return nil, err
	}
	if err := s.posts.Favorite(ctx, userID, postID); err != nil {
		return nil, apierr.Internal(err)
	}
	_, favoriteCount, err := s.posts.Counters(ctx, postID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return &PostFavoriteResult{IsFavorited: true, FavoriteCount: favoriteCount}, nil
}

// Unfavorite 幂等取消收藏。
func (s *PostService) Unfavorite(
	ctx context.Context,
	postID uint64,
	userID uint64,
) (*PostFavoriteResult, error) {
	if err := s.ensureInteractive(ctx, postID); err != nil {
		return nil, err
	}
	if err := s.posts.Unfavorite(ctx, userID, postID); err != nil {
		return nil, apierr.Internal(err)
	}
	_, favoriteCount, err := s.posts.Counters(ctx, postID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return &PostFavoriteResult{IsFavorited: false, FavoriteCount: favoriteCount}, nil
}

func (s *PostService) visibleRecord(
	ctx context.Context,
	postID uint64,
	currentUserID uint64,
) (*repository.PostRecord, error) {
	record, err := s.posts.FindByID(ctx, postID, repository.QueryOptions{IncludeDeleted: true})
	if err != nil {
		return nil, repository.ToAPIError(err, apierr.BizPostNotFound, "帖子")
	}
	if record.DeletedAt != nil {
		return nil, apierr.NotFound(apierr.BizPostDeleted, "帖子")
	}
	if record.Status != model.PostStatusApproved && record.AuthorID != currentUserID {
		return nil, apierr.NotFound(apierr.BizPostNotPublished, "帖子")
	}
	return record, nil
}

func (s *PostService) ensureInteractive(ctx context.Context, postID uint64) error {
	_, err := s.interactivePost(ctx, postID)
	return err
}

func (s *PostService) interactivePost(ctx context.Context, postID uint64) (*repository.PostRecord, error) {
	record, err := s.posts.FindByID(ctx, postID, repository.QueryOptions{IncludeDeleted: true})
	if err != nil {
		return nil, repository.ToAPIError(err, apierr.BizPostNotFound, "帖子")
	}
	if record.DeletedAt != nil {
		return nil, apierr.NotFound(apierr.BizPostDeleted, "帖子")
	}
	if record.Status != model.PostStatusApproved {
		return nil, apierr.Conflict(apierr.BizPostNotPublished, "帖子尚未发布")
	}
	return record, nil
}

func (s *PostService) loadRelations(
	ctx context.Context,
	records []repository.PostRecord,
	currentUserID uint64,
) (repository.PostRelations, error) {
	postIDs := make([]uint64, 0, len(records))
	authorIDs := make([]uint64, 0, len(records))
	for _, record := range records {
		postIDs = append(postIDs, record.ID)
		authorIDs = append(authorIDs, record.AuthorID)
	}
	relations, err := s.posts.LoadRelations(ctx, postIDs, authorIDs, currentUserID)
	if err != nil {
		return repository.PostRelations{}, apierr.Internal(err)
	}
	return relations, nil
}

func normalizePostFilter(input ListPostsInput) (repository.PostFilter, error) {
	filter := repository.PostFilter{
		Flavors: cleanNames(input.Flavors), Tags: cleanNames(input.Tags), SortBy: input.SortBy,
	}
	if filter.SortBy == "" {
		filter.SortBy = "latest"
	}
	if !oneOf(filter.SortBy, "latest", "hot", "trending", "price") {
		return filter, apierr.InvalidField("sort_by", apierr.FieldInvalidEnum, "sort_by 取值不合法")
	}
	if input.PostType != "" {
		value := model.PostType(input.PostType)
		if !validPostType(value) {
			return filter, apierr.InvalidField("post_type", apierr.FieldInvalidEnum, "post_type 取值不合法")
		}
		filter.PostType = &value
	}
	if input.ShareType != "" {
		value := model.ShareType(input.ShareType)
		if !validShareType(value) {
			return filter, apierr.InvalidField("share_type", apierr.FieldInvalidEnum, "share_type 取值不合法")
		}
		filter.ShareType = &value
	}
	if input.Category != "" {
		value := model.PostCategory(input.Category)
		if !validCategory(value) {
			return filter, apierr.InvalidField("category", apierr.FieldInvalidEnum, "category 取值不合法")
		}
		filter.Category = &value
	}
	filter.CanteenCode = optionalTrimmed(input.CanteenCode)
	filter.Cuisine = optionalTrimmed(input.Cuisine)
	if input.MinPrice != nil {
		value := input.MinPrice.Decimal()
		filter.MinPrice = &value
	}
	if input.MaxPrice != nil {
		value := input.MaxPrice.Decimal()
		filter.MaxPrice = &value
	}
	if filter.MinPrice != nil && filter.MaxPrice != nil && filter.MinPrice.GreaterThan(*filter.MaxPrice) {
		return filter, apierr.InvalidField("max_price", apierr.FieldConflict, "max_price 不能小于 min_price")
	}
	return filter, nil
}

func optionalTrimmed(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func postRepositoryError(err error) error {
	if errors.Is(err, repository.ErrNotFound) {
		return apierr.NotFound(apierr.BizPostNotFound, "帖子")
	}
	return apierr.Internal(err)
}
