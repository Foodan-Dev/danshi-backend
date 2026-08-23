package router_test

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	dbinfra "github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

var hotPathSelectBudgets = map[string]int64{
	"GET /api/v2/posts":                            9,
	"GET /api/v2/posts/:post_id/comments":          10,
	"GET /api/v2/comments/:comment_id/replies":     10,
	"GET /api/v2/users/:user_id/posts":             10,
	"GET /api/v2/users/:user_id/favorites":         10,
	"GET /api/v2/users/:user_id/following":         4,
	"GET /api/v2/users/:user_id/followers":         4,
	"GET /api/v2/notifications":                    4,
	"GET /api/v2/search/posts":                     9,
	"GET /api/v2/search/users":                     3,
	"GET /api/v2/dictionary-suggestions/mine":      3,
	"GET /api/v2/admin/dictionary-suggestions":     3,
	"GET /api/v2/admin/posts/pending":              4,
	"GET /api/v2/admin/posts":                      4,
	"GET /api/v2/admin/users":                      3,
	"GET /api/v2/admin/users/:user_id/posts":       5,
	"GET /api/v2/admin/admins":                     3,
	"GET /api/v2/admin/super-admins":               3,
	"GET /api/v2/admin/comments":                   3,
	"GET /api/v2/admin/moderation-records/pending": 3,
}

var queryBudgetExemptGETRoutes = map[string]string{
	"GET /api/v2/auth/me":                      "单资源读取，不接受分页参数",
	"GET /api/v2/auth/sessions":                "当前用户的小型设备集合，不接受分页参数",
	"GET /api/v2/users/:user_id":               "单资源读取，不接受分页参数",
	"GET /api/v2/posts/:post_id":               "单资源读取，不接受分页参数",
	"GET /api/v2/posts/:post_id/history":       "单资源的完整版本历史，不接受分页参数",
	"GET /api/v2/comments/:comment_id/history": "单资源的完整版本历史，不接受分页参数",
	"GET /api/v2/notifications/unread-count":   "单值聚合，不接受分页参数",
	"GET /api/v2/config":                       "公共静态配置，不接受分页参数",
	"GET /api/v2/admin/users/:user_id":         "单用户取证详情，不接受分页参数",
}

type queryBudgetCase struct {
	operation string
	path      string
	token     string
}

type queryBudgetFixture struct {
	actors      adminActors
	approved    []service.PostCreateResult
	commentRoot uint64
}

func TestHotPathQueryBudgetsAgainstPostgres(t *testing.T) {
	gdb, database := openAuthPostgres(t)
	cfg := authTestConfig()
	sender := newCaptureEmailSender()
	engine := authTestEngine(cfg, database, sender)
	fixture := seedQueryBudgetFixture(t, engine, gdb, database, sender)
	cases := queryBudgetCases(fixture)

	assertQueryBudgetInventory(t, engine, cases)

	var enabled atomic.Bool
	var count atomic.Int64
	registerQueryCounter(t, gdb, &enabled, &count)
	for _, testCase := range cases {
		t.Run(strings.TrimPrefix(testCase.operation, "GET "), func(t *testing.T) {
			measure := func(limit int) int64 {
				count.Store(0)
				enabled.Store(true)
				status, _, _ := performJSON(t, engine, http.MethodGet,
					withQueryBudgetPage(testCase.path, limit), nil, testCase.token)
				enabled.Store(false)
				require.Equal(t, http.StatusOK, status)
				return count.Load()
			}
			small, large := measure(1), measure(6)
			budget := hotPathSelectBudgets[testCase.operation]
			t.Logf("SELECT small=%d large=%d budget=%d", small, large, budget)
			require.Positive(t, small)
			require.Equal(t, small, large, "SELECT 数不得随 page_size 增长")
			require.LessOrEqual(t, large, budget, "SELECT 数超过显式预算")
		})
	}
}

func seedQueryBudgetFixture(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	database *dbinfra.DB,
	sender *captureEmailSender,
) queryBudgetFixture {
	t.Helper()
	actors := registerAdminActors(t, engine, sender, gdb)
	postFixture := loadPostFixture(t, gdb)
	reviewEngine := adminTestEngine(
		authTestConfig(), database, sender, fixedVerdictModerator(model.ModerationVerdictReview),
	)
	approved := make([]service.PostCreateResult, 0, 6)
	for index := range 6 {
		post := createPost(t, engine, actors.Author.Token, sharePostPayload(
			postFixture,
			fmt.Sprintf("查询预算帖子 %d", index),
			[]string{fmt.Sprintf("查询预算%d", index)},
		))
		approved = append(approved, post)
		status, _, _ := performJSON(t, engine, http.MethodPost,
			postPath(post.ID)+"/favorite", nil, actors.Ordinary.Token)
		require.Equal(t, http.StatusOK, status)

		createPost(t, reviewEngine, actors.Author.Token, sharePostPayload(
			postFixture,
			fmt.Sprintf("查询预算待审帖子 %d", index),
			[]string{fmt.Sprintf("查询预算待审%d", index)},
		))

		user := model.User{
			Email: fmt.Sprintf("query-budget-user-%d@fdueat.com", index), PasswordHash: "$2b$12$test",
			Name: fmt.Sprintf("查询预算用户 %d", index),
		}
		require.NoError(t, gdb.Create(&user).Error)
		require.NoError(t, gdb.Create(&model.Follow{
			FollowerID: actors.Ordinary.User.ID, FollowingID: user.ID,
		}).Error)
		require.NoError(t, gdb.Create(&model.Follow{
			FollowerID: user.ID, FollowingID: actors.Ordinary.User.ID,
		}).Error)

		createSuggestion(t, engine, actors.Ordinary.Token, map[string]any{
			"kind": model.SuggestionKindFlavor, "proposed_name": fmt.Sprintf("查询预算口味 %d", index),
			"flavor_stance": model.FlavorStanceHas,
		})
	}

	var firstRoot uint64
	for rootIndex := range 6 {
		root := createComment(t, engine, actors.Commenter.Token, approved[0].ID, map[string]any{
			"content":            fmt.Sprintf("@%s 查询预算楼主 %d", actors.Ordinary.User.Name, rootIndex),
			"mentioned_user_ids": []uint64{actors.Ordinary.User.ID},
		})
		if firstRoot == 0 {
			firstRoot = root.Comment.ID
		}
		for replyIndex := range 6 {
			createComment(t, engine, actors.Author.Token, approved[0].ID, map[string]any{
				"content":            fmt.Sprintf("@%s 查询预算回复 %d-%d", actors.Ordinary.User.Name, rootIndex, replyIndex),
				"parent_id":          root.Comment.ID,
				"mentioned_user_ids": []uint64{actors.Ordinary.User.ID},
			})
		}
	}
	return queryBudgetFixture{actors: actors, approved: approved, commentRoot: firstRoot}
}

func queryBudgetCases(fixture queryBudgetFixture) []queryBudgetCase {
	actors := fixture.actors
	return []queryBudgetCase{
		{operation: "GET /api/v2/posts", path: "/api/v2/posts", token: actors.Ordinary.Token},
		{
			operation: "GET /api/v2/posts/:post_id/comments",
			path:      fmt.Sprintf("/api/v2/posts/%d/comments", fixture.approved[0].ID),
			token:     actors.Ordinary.Token,
		},
		{
			operation: "GET /api/v2/comments/:comment_id/replies",
			path:      fmt.Sprintf("/api/v2/comments/%d/replies", fixture.commentRoot),
			token:     actors.Ordinary.Token,
		},
		{
			operation: "GET /api/v2/users/:user_id/posts",
			path:      userPath(actors.Author.User.ID) + "/posts", token: actors.Ordinary.Token,
		},
		{
			operation: "GET /api/v2/users/:user_id/favorites",
			path:      userPath(actors.Ordinary.User.ID) + "/favorites", token: actors.Ordinary.Token,
		},
		{
			operation: "GET /api/v2/users/:user_id/following",
			path:      userPath(actors.Ordinary.User.ID) + "/following", token: actors.Ordinary.Token,
		},
		{
			operation: "GET /api/v2/users/:user_id/followers",
			path:      userPath(actors.Ordinary.User.ID) + "/followers", token: actors.Ordinary.Token,
		},
		{operation: "GET /api/v2/notifications", path: "/api/v2/notifications", token: actors.Ordinary.Token},
		{operation: "GET /api/v2/search/posts", path: "/api/v2/search/posts?q=查询预算", token: actors.Ordinary.Token},
		{operation: "GET /api/v2/search/users", path: "/api/v2/search/users?q=查询预算用户", token: actors.Ordinary.Token},
		{
			operation: "GET /api/v2/dictionary-suggestions/mine",
			path:      "/api/v2/dictionary-suggestions/mine", token: actors.Ordinary.Token,
		},
		{
			operation: "GET /api/v2/admin/dictionary-suggestions",
			path:      "/api/v2/admin/dictionary-suggestions", token: actors.Dict.Token,
		},
		{operation: "GET /api/v2/admin/posts/pending", path: "/api/v2/admin/posts/pending", token: actors.Admin.Token},
		{operation: "GET /api/v2/admin/posts", path: "/api/v2/admin/posts", token: actors.Admin.Token},
		{operation: "GET /api/v2/admin/users", path: "/api/v2/admin/users", token: actors.Super.Token},
		{
			operation: "GET /api/v2/admin/users/:user_id/posts",
			path:      fmt.Sprintf("/api/v2/admin/users/%d/posts", actors.Author.User.ID),
			token:     actors.Admin.Token,
		},
		{operation: "GET /api/v2/admin/admins", path: "/api/v2/admin/admins", token: actors.Super.Token},
		{operation: "GET /api/v2/admin/super-admins", path: "/api/v2/admin/super-admins", token: actors.Super.Token},
		{operation: "GET /api/v2/admin/comments", path: "/api/v2/admin/comments", token: actors.Admin.Token},
		{
			operation: "GET /api/v2/admin/moderation-records/pending",
			path:      "/api/v2/admin/moderation-records/pending", token: actors.Admin.Token,
		},
	}
}

func assertQueryBudgetInventory(t *testing.T, engine *server.Hertz, cases []queryBudgetCase) {
	t.Helper()
	registered := make(map[string]struct{}, len(cases))
	for _, testCase := range cases {
		require.NotContains(t, registered, testCase.operation, "查询预算用例重复")
		registered[testCase.operation] = struct{}{}
	}
	require.Equal(t, stringKeySet(hotPathSelectBudgets), registered,
		"每条热路径必须恰好有一个预算执行用例")

	classified := stringKeySet(hotPathSelectBudgets)
	for operation, reason := range queryBudgetExemptGETRoutes {
		require.NotEmpty(t, reason, "%s 的免测理由不能为空", operation)
		require.NotContains(t, classified, operation, "GET 路由不能同时登记预算和免测")
		classified[operation] = struct{}{}
	}
	actual := make(map[string]struct{})
	for _, route := range engine.Routes() {
		if route.Method == http.MethodGet && strings.HasPrefix(route.Path, "/api/v2/") {
			actual[route.Method+" "+route.Path] = struct{}{}
		}
	}
	require.Equal(t, classified, actual,
		"每条业务 GET 路由必须登记 SELECT 预算或注明不接受分页参数的免测理由")
}

func registerQueryCounter(
	t *testing.T,
	gdb *gorm.DB,
	enabled *atomic.Bool,
	count *atomic.Int64,
) {
	t.Helper()
	callback := func(*gorm.DB) {
		if enabled.Load() {
			count.Add(1)
		}
	}
	require.NoError(t, gdb.Callback().Query().Before("gorm:query").
		Register("query_budget_count_query", callback))
	require.NoError(t, gdb.Callback().Raw().Before("gorm:raw").
		Register("query_budget_count_raw", callback))
	require.NoError(t, gdb.Callback().Row().Before("gorm:row").
		Register("query_budget_count_row", callback))
}

func withQueryBudgetPage(path string, limit int) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return fmt.Sprintf("%s%spage=1&limit=%d", path, separator, limit)
}

func stringKeySet[T any](values map[string]T) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for value := range values {
		result[value] = struct{}{}
	}
	return result
}
