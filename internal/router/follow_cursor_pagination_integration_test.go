package router_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

type followListDirection struct {
	name   string
	suffix string
}

var followListDirections = []followListDirection{
	{name: "following", suffix: "/following"},
	{name: "followers", suffix: "/followers"},
}

func TestFollowListCursorPaginationAgainstPostgres(t *testing.T) {
	h := testutil.NewHarness(t)
	base := time.Date(2026, time.August, 24, 8, 0, 0, 123456000, time.UTC)

	for directionIndex, direction := range followListDirections {
		direction := direction
		t.Run(direction.name+" insertion does not repeat", func(t *testing.T) {
			owner := h.Fixtures.CreateActor(h.Config)
			rows := createFollowRows(t, h.Database.GORM, h, owner.User.ID, direction,
				base.Add(time.Duration(directionIndex)*time.Hour), 4, false)
			first := getFollowPage(t, h, owner, direction, url.Values{"limit": {"2"}})
			require.Equal(t, reverseUserIDs(rows[2:]), followListIDs(first.Users))
			require.True(t, first.Pagination.HasMore)
			require.NotNil(t, first.Pagination.NextCursor)

			inserted := h.Fixtures.CreateUser()
			createFollowRelation(t, h.Database.GORM, owner.User.ID, inserted.ID, direction,
				base.Add(time.Duration(directionIndex)*time.Hour+10*time.Microsecond))
			second := getFollowPage(t, h, owner, direction, url.Values{
				"limit": {"2"}, "cursor": {*first.Pagination.NextCursor},
			})
			require.Equal(t, reverseUserIDs(rows[:2]), followListIDs(second.Users))
			require.NotContains(t, followListIDs(second.Users), first.Users[1].ID,
				"新关注关系插入后不得重复第一页边界用户")
		})

		t.Run(direction.name+" deletion does not skip", func(t *testing.T) {
			owner := h.Fixtures.CreateActor(h.Config)
			rows := createFollowRows(t, h.Database.GORM, h, owner.User.ID, direction,
				base.Add(time.Duration(directionIndex+2)*time.Hour), 4, false)
			first := getFollowPage(t, h, owner, direction, url.Values{"limit": {"2"}})
			deleteFollowRelation(t, h.Database.GORM, owner.User.ID, first.Users[0].ID, direction)

			second := getFollowPage(t, h, owner, direction, url.Values{
				"limit": {"2"}, "cursor": {*first.Pagination.NextCursor},
			})
			require.Equal(t, reverseUserIDs(rows[:2]), followListIDs(second.Users),
				"前一页的关注关系取消后不得跳过尚未读取的用户")
		})

		t.Run(direction.name+" same microsecond is stable", func(t *testing.T) {
			owner := h.Fixtures.CreateActor(h.Config)
			rows := createFollowRows(t, h.Database.GORM, h, owner.User.ID, direction,
				base.Add(time.Duration(directionIndex+4)*time.Hour), 5, true)
			values := url.Values{"limit": {"2"}}
			var got []uint64
			for {
				page := getFollowPage(t, h, owner, direction, values)
				got = append(got, followListIDs(page.Users)...)
				if !page.Pagination.HasMore {
					require.Nil(t, page.Pagination.NextCursor)
					break
				}
				require.NotNil(t, page.Pagination.NextCursor)
				values.Set("cursor", *page.Pagination.NextCursor)
			}
			require.Equal(t, reverseUserIDs(rows), got,
				"相同 created_at 必须由对侧用户 id DESC 保证不重不漏")
		})
	}

	t.Run("cursor is authenticated and endpoint scoped", func(t *testing.T) {
		owner := h.Fixtures.CreateActor(h.Config)
		createFollowRows(t, h.Database.GORM, h, owner.User.ID, followListDirections[0],
			base.Add(8*time.Hour), 3, false)
		first := getFollowPage(t, h, owner, followListDirections[0], url.Values{"limit": {"1"}})
		require.NotNil(t, first.Pagination.NextCursor)

		for name, token := range map[string]string{
			"tampered":    tamperCursor(*first.Pagination.NextCursor),
			"wrong scope": *first.Pagination.NextCursor,
		} {
			suffix := followListDirections[0].suffix
			if name == "wrong scope" {
				suffix = followListDirections[1].suffix
			}
			status, response, _ := performJSON(t, h.Engine, http.MethodGet,
				userPath(owner.User.ID)+suffix+"?limit=1&cursor="+url.QueryEscape(token), nil, owner.Token)
			requireFieldError(t, status, response, "cursor", apierr.FieldInvalidFormat)
		}
	})
}

func TestFollowListCursorIndexesAgainstPostgres(t *testing.T) {
	h := testutil.NewHarness(t)
	owner := h.Fixtures.CreateActor(h.Config)
	seedFollowPlanRows(t, h.Database.GORM, owner.User.ID, true)
	seedFollowPlanRows(t, h.Database.GORM, owner.User.ID, false)
	require.NoError(t, h.Database.GORM.Exec("ANALYZE users, follows").Error)

	assertFollowPlanUsesIndex(t, h.Database.GORM, `
		SELECT f.following_id
		FROM follows AS f
		JOIN users AS u ON u.id = f.following_id
		WHERE f.follower_id = ? AND u.deleted_at IS NULL
		ORDER BY f.created_at DESC, f.following_id DESC
		LIMIT 20
	`, owner.User.ID, "idx_follows_follower_created_following")
	assertFollowPlanUsesIndex(t, h.Database.GORM, `
		SELECT f.follower_id
		FROM follows AS f
		JOIN users AS u ON u.id = f.follower_id
		WHERE f.following_id = ? AND u.deleted_at IS NULL
		ORDER BY f.created_at DESC, f.follower_id DESC
		LIMIT 20
	`, owner.User.ID, "idx_follows_following_created_follower")
}

func createFollowRows(
	t *testing.T,
	gdb *gorm.DB,
	h *testutil.Harness,
	ownerID uint64,
	direction followListDirection,
	base time.Time,
	count int,
	sameMicrosecond bool,
) []model.User {
	t.Helper()
	rows := make([]model.User, 0, count)
	for index := range count {
		user := h.Fixtures.CreateUser()
		createdAt := base
		if !sameMicrosecond {
			createdAt = base.Add(time.Duration(index) * time.Microsecond)
		}
		createFollowRelation(t, gdb, ownerID, user.ID, direction, createdAt)
		rows = append(rows, user)
	}
	return rows
}

func createFollowRelation(
	t *testing.T,
	gdb *gorm.DB,
	ownerID uint64,
	otherID uint64,
	direction followListDirection,
	createdAt time.Time,
) {
	t.Helper()
	relation := model.Follow{FollowerID: ownerID, FollowingID: otherID, CreatedAt: createdAt}
	if direction.name == "followers" {
		relation.FollowerID, relation.FollowingID = otherID, ownerID
	}
	require.NoError(t, gdb.Create(&relation).Error)
}

func deleteFollowRelation(
	t *testing.T,
	gdb *gorm.DB,
	ownerID uint64,
	otherID uint64,
	direction followListDirection,
) {
	t.Helper()
	followerID, followingID := ownerID, otherID
	if direction.name == "followers" {
		followerID, followingID = otherID, ownerID
	}
	require.NoError(t, gdb.Where(
		"follower_id = ? AND following_id = ?", followerID, followingID,
	).Delete(&model.Follow{}).Error)
}

func getFollowPage(
	t *testing.T,
	h *testutil.Harness,
	owner testutil.Actor,
	direction followListDirection,
	values url.Values,
) service.UserFollowList {
	t.Helper()
	status, response, _ := performJSON(t, h.Engine, http.MethodGet,
		userPath(owner.User.ID)+direction.suffix+"?"+values.Encode(), nil, owner.Token)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
	var page service.UserFollowList
	decodeData(t, response, &page)
	return page
}

func followListIDs(items []service.UserListItem) []uint64 {
	ids := make([]uint64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func reverseUserIDs(users []model.User) []uint64 {
	ids := make([]uint64, 0, len(users))
	for index := len(users) - 1; index >= 0; index-- {
		ids = append(ids, users[index].ID)
	}
	return ids
}

func seedFollowPlanRows(t *testing.T, gdb *gorm.DB, ownerID uint64, following bool) {
	t.Helper()
	prefix := "followers_" + strconv.FormatUint(ownerID, 10)
	insert := `
		WITH seeded AS (
			INSERT INTO users (email, password_hash, name)
			SELECT CAST(? AS text) || '-plan-' || value || '@fdueat.com',
				'$2b$12$test', CAST(? AS text) || 'planuser' || value
			FROM generate_series(1, 600) AS value
			RETURNING id
		)
		INSERT INTO follows (follower_id, following_id, created_at)
		SELECT id, ?, TIMESTAMPTZ '2026-08-24 08:00:00+00' + id * INTERVAL '1 microsecond'
		FROM seeded
	`
	if following {
		prefix = "following_" + strconv.FormatUint(ownerID, 10)
		insert = `
			WITH seeded AS (
				INSERT INTO users (email, password_hash, name)
			SELECT CAST(? AS text) || '-plan-' || value || '@fdueat.com',
				'$2b$12$test', CAST(? AS text) || 'planuser' || value
				FROM generate_series(1, 600) AS value
				RETURNING id
			)
			INSERT INTO follows (follower_id, following_id, created_at)
			SELECT ?, id, TIMESTAMPTZ '2026-08-24 08:00:00+00' + id * INTERVAL '1 microsecond'
			FROM seeded
		`
	}
	require.NoError(t, gdb.Exec(insert, prefix, prefix, int64(ownerID)).Error)
}

func assertFollowPlanUsesIndex(t *testing.T, gdb *gorm.DB, query string, ownerID uint64, indexName string) {
	t.Helper()
	var raw string
	require.NoError(t, gdb.Raw("EXPLAIN (ANALYZE, COSTS OFF, FORMAT JSON) "+query, ownerID).
		Row().Scan(&raw))
	var documents []map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &documents))
	require.NotEmpty(t, documents)
	plan, ok := documents[0]["Plan"].(map[string]any)
	require.True(t, ok, "EXPLAIN JSON 缺少 Plan: %s", raw)
	require.True(t, planUsesIndex(plan, indexName), "查询计划未使用 %s: %s", indexName, raw)
	require.False(t, planContainsNode(plan, "Sort"), "游标查询不得对全部关注关系排序: %s", raw)
	t.Logf("%s plan: %s", indexName, raw)
}

func planUsesIndex(plan map[string]any, indexName string) bool {
	if plan["Index Name"] == indexName {
		return true
	}
	for _, child := range planChildren(plan) {
		if planUsesIndex(child, indexName) {
			return true
		}
	}
	return false
}

func planContainsNode(plan map[string]any, nodeType string) bool {
	if plan["Node Type"] == nodeType {
		return true
	}
	for _, child := range planChildren(plan) {
		if planContainsNode(child, nodeType) {
			return true
		}
	}
	return false
}

func planChildren(plan map[string]any) []map[string]any {
	raw, ok := plan["Plans"].([]any)
	if !ok {
		return nil
	}
	children := make([]map[string]any, 0, len(raw))
	for _, value := range raw {
		if child, childOK := value.(map[string]any); childOK {
			children = append(children, child)
		}
	}
	return children
}
