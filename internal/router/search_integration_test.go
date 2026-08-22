package router_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/service"
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
		testPostSearch(t, engine, author, fixture, post.ID)
	})

	t.Run("user ilike follow state and active visibility", func(t *testing.T) {
		testUserSearch(t, engine, gdb, author, viewer)
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
