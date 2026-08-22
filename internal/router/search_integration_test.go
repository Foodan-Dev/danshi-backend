package router_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/service"
	"github.com/jingyijun/danshi_backend_go/internal/testutil"
)

func TestSearchDomainAgainstPostgres(t *testing.T) {
	gdb, database := openAuthPostgres(t)
	cfg := authTestConfig()
	sender := newCaptureEmailSender()
	engine := authTestEngine(cfg, database, sender)
	author := registerPostTestUser(t, engine, sender, "search-author@fdueat.com", "CaseSearch 作者")
	viewer := registerPostTestUser(t, engine, sender, "search-viewer@fdueat.com", "搜索访客")
	fixture := loadPostFixture(t, gdb)

	t.Run("search route inventory", func(t *testing.T) {
		testSearchRouteInventory(t, engine)
	})

	post := createSearchPost(t, engine, author, fixture)

	t.Run("post ilike filters escaped highlight and rune snippet", func(t *testing.T) {
		testPostSearch(t, engine, gdb, author, fixture, post.ID)
	})

	t.Run("literal wildcard empty long pagination and public visibility", func(t *testing.T) {
		testSearchBoundaries(t, engine, gdb, author, fixture)
	})

	t.Run("post filters stay identical to public feed", func(t *testing.T) {
		testSearchFilterParity(t, engine, author, fixture)
	})

	t.Run("user ilike follow state and active visibility", func(t *testing.T) {
		testUserSearch(t, engine, gdb, author, viewer)
	})

	t.Run("user wildcard characters are literal", func(t *testing.T) {
		testUserSearchLiteralWildcards(t, engine, gdb, viewer)
	})

	t.Run("authentication and query validation", func(t *testing.T) {
		testSearchGuards(t, engine, viewer)
	})
}

func testSearchRouteInventory(t *testing.T, engine *server.Hertz) {
	t.Helper()
	operations := make([]string, 0)
	for _, route := range engine.Routes() {
		if strings.HasPrefix(route.Path, "/api/v2/search") {
			operations = append(operations, route.Method+" "+route.Path)
		}
	}
	require.ElementsMatch(t, []string{
		"GET /api/v2/search/posts",
		"GET /api/v2/search/users",
	}, operations)
}

func createSearchPost(
	t *testing.T,
	engine *server.Hertz,
	author service.AuthResult,
	fixture postFixture,
) service.PostCreateResult {
	t.Helper()
	payload := sharePostPayload(fixture, "<script>火锅 CaseSearch</script>", []string{"搜索组合"})
	payload["content"] = "<b>火锅</b>" + strings.Repeat("界", 210)
	return createPost(t, engine, author.Token, payload)
}

func testPostSearch(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	author service.AuthResult,
	fixture postFixture,
	postID uint64,
) {
	t.Helper()
	query := url.Values{
		"q":            {"火锅"},
		"post_type":    {string(model.PostTypeShare)},
		"share_type":   {string(model.ShareTypeRecommend)},
		"category":     {string(model.PostCategoryFood)},
		"canteen_code": {fixture.Canteen.Code},
		"cuisine":      {fixture.Cuisine.Name},
		"flavors":      {fixture.Flavors[0].Name},
		"tags":         {"搜索组合"},
		"min_price":    {"18.00"},
		"max_price":    {"19.00"},
	}
	status, response, _ := performJSON(t, engine, http.MethodGet,
		"/api/v2/search/posts?"+query.Encode(), nil, author.Token)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
	var result service.SearchPostList
	decodeData(t, response, &result)
	require.Len(t, result.Posts, 1, "关键词与全部帖子筛选条件必须按 AND 组合")
	item := result.Posts[0]
	require.Equal(t, postID, item.ID)
	require.Equal(t, "&lt;script&gt;<em>火锅</em> CaseSearch&lt;/script&gt;", item.Highlight.Title)
	require.Equal(t, "&lt;b&gt;<em>火锅</em>&lt;/b&gt;"+strings.Repeat("界", 191)+"...",
		item.Highlight.Content)
	require.Equal(t, 203, len([]rune(item.Content)), "正文摘要必须按 200 个 rune 截断后追加省略号")
	require.Equal(t, "...", string([]rune(item.Content)[200:]))

	query.Set("q", "casesearch")
	status, response, _ = performJSON(t, engine, http.MethodGet,
		"/api/v2/search/posts?"+query.Encode(), nil, author.Token)
	require.Equal(t, http.StatusOK, status)
	decodeData(t, response, &result)
	require.Len(t, result.Posts, 1, "标题 ILIKE 必须大小写不敏感")

	query.Set("canteen_code", "canteen-does-not-exist")
	status, response, _ = performJSON(t, engine, http.MethodGet,
		"/api/v2/search/posts?"+query.Encode(), nil, author.Token)
	require.Equal(t, http.StatusOK, status)
	decodeData(t, response, &result)
	require.Empty(t, result.Posts, "search 与 post 信息流必须共享 canteen_code 语义")

	pendingPayload := sharePostPayload(fixture, "火锅待审不可见", []string{"搜索待审"})
	pending := createPost(t, engine, author.Token, pendingPayload)
	require.NoError(t, gdb.Model(&model.Post{}).Where("id = ?", pending.ID).
		UpdateColumn("status", model.PostStatusPending).Error)
	deletedPayload := sharePostPayload(fixture, "火锅已删不可见", []string{"搜索已删"})
	deleted := createPost(t, engine, author.Token, deletedPayload)
	deletedAt := time.Now().UTC()
	require.NoError(t, gdb.Model(&model.Post{}).Where("id = ?", deleted.ID).UpdateColumns(map[string]any{
		"deleted_at": deletedAt, "deleted_reason": model.DeleteReasonAuthor,
		"deleted_by": author.User.ID,
	}).Error)
	query = url.Values{"q": {"火锅"}, "limit": {"100"}}
	status, response, _ = performJSON(t, engine, http.MethodGet,
		"/api/v2/search/posts?"+query.Encode(), nil, author.Token)
	require.Equal(t, http.StatusOK, status)
	decodeData(t, response, &result)
	require.True(t, searchPostPresent(result.Posts, postID))
	require.False(t, searchPostPresent(result.Posts, pending.ID), "待审帖子不得进入公开搜索")
	require.False(t, searchPostPresent(result.Posts, deleted.ID), "软删除帖子不得进入公开搜索")
}

func testSearchBoundaries(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	author service.AuthResult,
	fixture postFixture,
) {
	t.Helper()
	literalIDs := make(map[string]uint64)
	for _, literal := range []string{"%", "_", `\`} {
		payload := sharePostPayload(fixture, "字面搜索 "+literal+" 唯一", []string{"字面搜索"})
		literalIDs[literal] = createPost(t, engine, author.Token, payload).ID
	}
	for literal, expectedID := range literalIDs {
		query := url.Values{"q": {literal}, "limit": {"100"}}
		status, response, _ := performJSON(t, engine, http.MethodGet,
			"/api/v2/search/posts?"+query.Encode(), nil, author.Token)
		require.Equal(t, http.StatusOK, status, "literal=%q", literal)
		var result service.SearchPostList
		decodeData(t, response, &result)
		require.Equal(t, []uint64{expectedID}, searchPostIDs(result.Posts),
			"SQL 通配符和转义字符必须按字面量搜索：%q", literal)
	}

	visible := createPost(t, engine, author.Token,
		sharePostPayload(fixture, "空关键词公开帖子", []string{"空关键词"}))
	pending := createPost(t, engine, author.Token,
		sharePostPayload(fixture, "空关键词待审帖子", []string{"空关键词"}))
	require.NoError(t, gdb.Model(&model.Post{}).Where("id = ?", pending.ID).
		UpdateColumn("status", model.PostStatusPending).Error)

	status, response, _ := performJSON(t, engine, http.MethodGet,
		"/api/v2/search/posts?q=&limit=100", nil, author.Token)
	require.Equal(t, http.StatusOK, status)
	var empty service.SearchPostList
	decodeData(t, response, &empty)
	require.True(t, searchPostPresent(empty.Posts, visible.ID))
	require.False(t, searchPostPresent(empty.Posts, pending.ID))

	longQuery := url.Values{"q": {strings.Repeat("界", 512)}}
	status, response, _ = performJSON(t, engine, http.MethodGet,
		"/api/v2/search/posts?"+longQuery.Encode(), nil, author.Token)
	require.Equal(t, http.StatusOK, status)
	var longResult service.SearchPostList
	decodeData(t, response, &longResult)
	require.Empty(t, longResult.Posts)

	status, response, _ = performJSON(t, engine, http.MethodGet,
		"/api/v2/search/posts?q=&page=999&limit=100", nil, author.Token)
	require.Equal(t, http.StatusOK, status)
	var beyond service.SearchPostList
	decodeData(t, response, &beyond)
	require.Empty(t, beyond.Posts)
}

func testSearchFilterParity(
	t *testing.T,
	engine *server.Hertz,
	author service.AuthResult,
	fixture postFixture,
) {
	t.Helper()
	cases := []url.Values{
		{"post_type": {string(model.PostTypeShare)}},
		{"share_type": {string(model.ShareTypeRecommend)}},
		{"category": {string(model.PostCategoryFood)}},
		{"canteen_code": {fixture.Canteen.Code}},
		{"cuisine": {fixture.Cuisine.Name}},
		{"flavors": {fixture.Flavors[0].Name}},
		{"tags": {"搜索组合"}},
		{"min_price": {"18.00"}},
		{"max_price": {"19.00"}},
	}
	for _, filter := range cases {
		feedQuery := cloneSearchValues(filter)
		feedQuery.Set("limit", "100")
		status, response, _ := performJSON(t, engine, http.MethodGet,
			"/api/v2/posts?"+feedQuery.Encode(), nil, author.Token)
		require.Equal(t, http.StatusOK, status, "feed filter=%s", filter.Encode())
		var feed service.PostList
		decodeData(t, response, &feed)

		searchQuery := cloneSearchValues(feedQuery)
		searchQuery.Set("q", "")
		status, response, _ = performJSON(t, engine, http.MethodGet,
			"/api/v2/search/posts?"+searchQuery.Encode(), nil, author.Token)
		require.Equal(t, http.StatusOK, status, "search filter=%s", filter.Encode())
		var search service.SearchPostList
		decodeData(t, response, &search)
		require.Equal(t, postListIDs(feed.Posts), searchPostIDs(search.Posts),
			"search 必须复用 posts 列表筛选语义：%s", filter.Encode())
	}
}

func testUserSearch(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	author service.AuthResult,
	viewer service.AuthResult,
) {
	t.Helper()
	status, _, _ := performJSON(t, engine, http.MethodPost,
		userPath(author.User.ID)+"/follow", nil, viewer.Token)
	require.Equal(t, http.StatusOK, status)

	status, response, _ := performJSON(t, engine, http.MethodGet,
		"/api/v2/search/users?q=casesearch", nil, viewer.Token)
	require.Equal(t, http.StatusOK, status)
	var result service.SearchUserList
	decodeData(t, response, &result)
	require.Len(t, result.Users, 1)
	require.Equal(t, author.User.ID, result.Users[0].ID)
	require.True(t, result.Users[0].IsFollowing)

	require.NoError(t, gdb.Model(&model.User{}).Where("id = ?", author.User.ID).
		UpdateColumns(map[string]any{"ban_is_permanent": true, "ban_reason": "搜索可见性测试"}).Error)
	status, response, _ = performJSON(t, engine, http.MethodGet,
		"/api/v2/search/users?q=casesearch", nil, viewer.Token)
	require.Equal(t, http.StatusOK, status)
	decodeData(t, response, &result)
	require.Empty(t, result.Users, "永久封禁用户不应出现在搜索结果")

	deletedAt := time.Now().UTC()
	require.NoError(t, gdb.Model(&model.User{}).Where("id = ?", author.User.ID).
		UpdateColumns(map[string]any{
			"ban_is_permanent": false, "ban_reason": nil, "deleted_at": deletedAt,
		}).Error)
	status, response, _ = performJSON(t, engine, http.MethodGet,
		"/api/v2/search/users?q=casesearch", nil, viewer.Token)
	require.Equal(t, http.StatusOK, status)
	decodeData(t, response, &result)
	require.Empty(t, result.Users, "已注销用户不应出现在搜索结果")
}

func testUserSearchLiteralWildcards(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	viewer service.AuthResult,
) {
	t.Helper()
	fixtures := testutil.NewFixtures(t, gdb)
	users := make(map[string]model.User)
	for _, literal := range []string{"%", "_", `\`} {
		name := "用户字面" + literal + "唯一"
		users[literal] = fixtures.CreateUser(func(user *model.User) { user.Name = name })
	}
	for literal, expected := range users {
		query := url.Values{"q": {literal}, "limit": {"100"}}
		status, response, _ := performJSON(t, engine, http.MethodGet,
			"/api/v2/search/users?"+query.Encode(), nil, viewer.Token)
		require.Equal(t, http.StatusOK, status, "literal=%q", literal)
		var result service.SearchUserList
		decodeData(t, response, &result)
		require.Equal(t, []uint64{expected.ID}, searchUserIDs(result.Users),
			"用户搜索必须按字面量处理：%q", literal)
	}

	status, response, _ := performJSON(t, engine, http.MethodGet,
		"/api/v2/search/users?q=&page=999&limit=100", nil, viewer.Token)
	require.Equal(t, http.StatusOK, status)
	var beyond service.SearchUserList
	decodeData(t, response, &beyond)
	require.Empty(t, beyond.Users)
}

func testSearchGuards(
	t *testing.T,
	engine *server.Hertz,
	viewer service.AuthResult,
) {
	t.Helper()
	status, _, _ := performJSON(t, engine, http.MethodGet, "/api/v2/search/posts?q=火锅", nil, "")
	require.Equal(t, http.StatusUnauthorized, status)
	status, _, _ = performJSON(t, engine, http.MethodGet, "/api/v2/search/posts", nil, viewer.Token)
	require.Equal(t, http.StatusUnprocessableEntity, status)
}

func cloneSearchValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for key, items := range values {
		cloned[key] = append([]string(nil), items...)
	}
	return cloned
}

func searchPostIDs(posts []service.SearchPostItem) []uint64 {
	ids := make([]uint64, 0, len(posts))
	for _, post := range posts {
		ids = append(ids, post.ID)
	}
	return ids
}

func postListIDs(posts []service.PostListItem) []uint64 {
	ids := make([]uint64, 0, len(posts))
	for _, post := range posts {
		ids = append(ids, post.ID)
	}
	return ids
}

func searchPostPresent(posts []service.SearchPostItem, id uint64) bool {
	for _, post := range posts {
		if post.ID == id {
			return true
		}
	}
	return false
}

func searchUserIDs(users []service.SearchUserItem) []uint64 {
	ids := make([]uint64, 0, len(users))
	for _, user := range users {
		ids = append(ids, user.ID)
	}
	return ids
}
