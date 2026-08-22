package service

import (
	"context"
	"html"
	"regexp"
	"strings"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/pagination"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/ptime"
	"github.com/jingyijun/danshi_backend_go/internal/repository"
)

const searchPostContentRunes = 200

// SearchPostsInput 是帖子关键词及与信息流一致的组合筛选条件。
type SearchPostsInput struct {
	Query   string
	Filters ListPostsInput
}

// SearchPostAuthor 是搜索结果中的作者公开信息。
type SearchPostAuthor struct {
	ID        uint64  `json:"id"`
	Name      string  `json:"name"`
	AvatarURL *string `json:"avatar_url"`
}

// SearchPostStats 是搜索卡片使用的帖子计数。
type SearchPostStats struct {
	LikeCount    int32 `json:"like_count"`
	ViewCount    int32 `json:"view_count"`
	CommentCount int32 `json:"comment_count"`
}

// SearchHighlight 是经过 HTML 转义后唯一允许包含 em 标签的高亮文本。
type SearchHighlight struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// SearchPostItem 是帖子搜索结果项。
type SearchPostItem struct {
	ID        uint64             `json:"id"`
	Title     string             `json:"title"`
	Content   string             `json:"content"`
	Category  model.PostCategory `json:"category"`
	Images    []string           `json:"images"`
	Author    SearchPostAuthor   `json:"author"`
	Stats     SearchPostStats    `json:"stats"`
	Highlight SearchHighlight    `json:"highlight"`
	CreatedAt ptime.Time         `json:"created_at"`
}

// SearchPostList 是帖子搜索分页响应。
type SearchPostList struct {
	Posts      []SearchPostItem `json:"posts"`
	Pagination pagination.Meta  `json:"pagination"`
}

// SearchUserStats 是用户搜索结果中的公开统计。
type SearchUserStats struct {
	PostCount     int64 `json:"post_count"`
	FollowerCount int64 `json:"follower_count"`
}

// SearchUserItem 是昵称搜索结果项。
type SearchUserItem struct {
	ID          uint64          `json:"id"`
	Name        string          `json:"name"`
	AvatarURL   *string         `json:"avatar_url"`
	Bio         *string         `json:"bio"`
	Stats       SearchUserStats `json:"stats"`
	IsFollowing bool            `json:"is_following"`
}

// SearchUserList 是用户搜索分页响应。
type SearchUserList struct {
	Users      []SearchUserItem `json:"users"`
	Pagination pagination.Meta  `json:"pagination"`
}

// SearchService 实现帖子与用户的关键词搜索。
type SearchService struct {
	posts repository.PostRepository
	users repository.UserRepository
}

// NewSearchService 创建搜索服务。
func NewSearchService() *SearchService { return &SearchService{} }

// Posts 搜索已发布帖子，并复用信息流的全部筛选语义。
func (s *SearchService) Posts(
	ctx context.Context,
	input SearchPostsInput,
	currentUserID uint64,
) (*SearchPostList, error) {
	query := strings.TrimSpace(input.Query)
	input.Filters.SortBy = "latest"
	filter, err := normalizePostFilter(input.Filters)
	if err != nil {
		return nil, err
	}
	if query != "" {
		filter.Keyword = &query
	}
	records, meta, err := s.posts.FindPage(ctx, filter, input.Filters.Pagination)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	postIDs := make([]uint64, 0, len(records))
	authorIDs := make([]uint64, 0, len(records))
	for _, record := range records {
		postIDs = append(postIDs, record.ID)
		authorIDs = append(authorIDs, record.AuthorID)
	}
	relations, err := s.posts.LoadRelations(ctx, postIDs, authorIDs, currentUserID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	items := make([]SearchPostItem, 0, len(records))
	for index := range records {
		items = append(items, searchPostItem(&records[index], relations, query))
	}
	return &SearchPostList{Posts: items, Pagination: meta}, nil
}

// Users 按昵称搜索未注销且未封禁用户。
func (s *SearchService) Users(
	ctx context.Context,
	query string,
	params pagination.Params,
	currentUserID uint64,
) (*SearchUserList, error) {
	rows, meta, err := s.users.SearchPage(ctx, strings.TrimSpace(query), currentUserID, params)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	items := make([]SearchUserItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, SearchUserItem{
			ID: row.ID, Name: row.Name, AvatarURL: row.AvatarURL, Bio: row.Bio,
			Stats:       SearchUserStats{PostCount: row.PostCount, FollowerCount: row.FollowerCount},
			IsFollowing: row.IsFollowing,
		})
	}
	return &SearchUserList{Users: items, Pagination: meta}, nil
}

func searchPostItem(
	record *repository.PostRecord,
	relations repository.PostRelations,
	query string,
) SearchPostItem {
	content := searchSnippet(record.Content, searchPostContentRunes)
	name, avatarURL := record.AuthorName, record.AvatarURL
	if record.AuthorDeletedAt != nil {
		name, avatarURL = "已注销用户", nil
	}
	return SearchPostItem{
		ID: record.ID, Title: record.Title, Content: content, Category: record.Category,
		Images: nonNilStrings(relations.Images[record.ID]),
		Author: SearchPostAuthor{ID: record.AuthorID, Name: name, AvatarURL: avatarURL},
		Stats: SearchPostStats{
			LikeCount: record.LikeCount, ViewCount: record.ViewCount, CommentCount: record.CommentCount,
		},
		Highlight: SearchHighlight{
			Title: highlightEscaped(record.Title, query), Content: highlightEscaped(content, query),
		},
		CreatedAt: ptime.Time(record.CreatedAt),
	}
}

func searchSnippet(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}

func highlightEscaped(value, query string) string {
	if query == "" {
		return html.EscapeString(value)
	}
	pattern := regexp.MustCompile(`(?i:` + regexp.QuoteMeta(query) + `)`)
	indexes := pattern.FindAllStringIndex(value, -1)
	if len(indexes) == 0 {
		return html.EscapeString(value)
	}
	var result strings.Builder
	start := 0
	for _, index := range indexes {
		result.WriteString(html.EscapeString(value[start:index[0]]))
		result.WriteString("<em>")
		result.WriteString(html.EscapeString(value[index[0]:index[1]]))
		result.WriteString("</em>")
		start = index[1]
	}
	result.WriteString(html.EscapeString(value[start:]))
	return result.String()
}
