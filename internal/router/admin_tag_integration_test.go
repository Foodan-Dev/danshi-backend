package router_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

func TestAdminTagCapabilitiesAgainstPostgres(t *testing.T) {
	gdb, database := openAuthPostgres(t)
	cfg := authTestConfig()
	sender := newCaptureEmailSender()
	engine := authTestEngine(cfg, database, sender)
	actors := registerAdminActors(t, engine, sender, gdb)
	fixture := loadPostFixture(t, gdb)

	both := createPost(t, engine, actors.Author.Token,
		sharePostPayload(fixture, "标签合并双重关联", []string{"合并源", "合并目标"}))
	sourceOnly := createPost(t, engine, actors.Author.Token,
		sharePostPayload(fixture, "标签合并单关联", []string{"合并源"}))
	renamePost := createPost(t, engine, actors.Author.Token,
		sharePostPayload(fixture, "标签重命名历史", []string{"改名前"}))
	source := findTagByName(t, gdb, "合并源")
	target := findTagByName(t, gdb, "合并目标")
	rename := findTagByName(t, gdb, "改名前")

	t.Run("capability boundaries", func(t *testing.T) {
		testAdminTagRoleGuards(t, engine, actors, source.ID, target.ID)
	})

	t.Run("list filters and cursor", func(t *testing.T) {
		status, response, _ := performJSON(t, engine, http.MethodGet,
			"/api/v2/admin/tags?name=合并&moderation=pass&is_deleted=false&limit=1", nil,
			actors.Admin.Token)
		require.Equal(t, http.StatusOK, status)
		var first service.AdminTagList
		decodeData(t, response, &first)
		require.Len(t, first.Tags, 1)
		require.True(t, first.Pagination.HasMore)
		require.NotNil(t, first.Pagination.NextCursor)

		status, response, _ = performJSON(t, engine, http.MethodGet,
			"/api/v2/admin/tags?name=合并&moderation=pass&is_deleted=false&limit=1&cursor="+
				*first.Pagination.NextCursor, nil, actors.Admin.Token)
		require.Equal(t, http.StatusOK, status)
		var second service.AdminTagList
		decodeData(t, response, &second)
		require.Len(t, second.Tags, 1)
		require.NotEqual(t, first.Tags[0].ID, second.Tags[0].ID)
	})

	t.Run("rename collision and snapshot continuity", func(t *testing.T) {
		status, response, _ := performJSON(t, engine, http.MethodPatch,
			fmt.Sprintf("/api/v2/admin/tags/%d", source.ID),
			map[string]any{"name": "合并目标"}, actors.Admin.Token)
		require.Equal(t, http.StatusConflict, status)
		require.Equal(t, apierr.BizTagNameConflict, response.ErrorCode)

		status, response, _ = performJSON(t, engine, http.MethodPatch,
			fmt.Sprintf("/api/v2/admin/tags/%d", rename.ID),
			map[string]any{"name": "改名后"}, actors.Admin.Token)
		require.Equal(t, http.StatusOK, status)
		var renamed service.AdminTagView
		decodeData(t, response, &renamed)
		require.Equal(t, "改名后", renamed.Name)
		require.EqualValues(t, 2, postRevisionCount(t, gdb, renamePost.ID),
			"管理端重命名必须追加帖子快照，不能破坏下一次编辑")

		payload := sharePostPayload(fixture, "标签重命名后仍可编辑", []string{"改名后"})
		status, response, _ = performJSON(t, engine, http.MethodPut,
			postPath(renamePost.ID), payload, actors.Author.Token)
		require.Equal(t, http.StatusOK, status,
			"重命名后帖子编辑不应永久 409: error_code=%s message=%s",
			response.ErrorCode, response.Message)
	})

	t.Run("merge deduplicates and rewrites histories", func(t *testing.T) {
		status, response, _ := performJSON(t, engine, http.MethodPost,
			fmt.Sprintf("/api/v2/admin/tags/%d/merge", source.ID),
			map[string]any{"target_tag_id": target.ID}, actors.Admin.Token)
		require.Equal(t, http.StatusOK, status,
			"error_code=%s message=%s", response.ErrorCode, response.Message)
		var merged service.AdminTagMergeResult
		decodeData(t, response, &merged)
		require.Equal(t, 2, merged.AffectedPostCount)
		require.True(t, merged.Source.IsDeleted)

		var sourceRelations int64
		require.NoError(t, gdb.Model(&model.PostTag{}).
			Where("tag_id = ?", source.ID).Count(&sourceRelations).Error)
		require.Zero(t, sourceRelations, "合并后不得保留任何源标签关联")
		for _, postID := range []uint64{both.ID, sourceOnly.ID} {
			var targetRelations int64
			require.NoError(t, gdb.Model(&model.PostTag{}).
				Where("post_id = ? AND tag_id = ?", postID, target.ID).
				Count(&targetRelations).Error)
			require.EqualValues(t, 1, targetRelations,
				"同时关联源和目标的帖子合并后只能保留一条目标关联")
			require.EqualValues(t, 2, postRevisionCount(t, gdb, postID))
		}

		payload := sharePostPayload(fixture, "标签合并后仍可编辑", []string{"合并目标"})
		status, response, _ = performJSON(t, engine, http.MethodPut,
			postPath(both.ID), payload, actors.Author.Token)
		require.Equal(t, http.StatusOK, status,
			"合并后帖子编辑不应因快照失配返回 409: error_code=%s message=%s",
			response.ErrorCode, response.Message)
	})

	t.Run("soft delete preserves and restore reactivates associations", func(t *testing.T) {
		before := tagRelationCount(t, gdb, target.ID)
		status, response, _ := performJSON(t, engine, http.MethodDelete,
			fmt.Sprintf("/api/v2/admin/tags/%d", target.ID), nil, actors.Admin.Token)
		require.Equal(t, http.StatusOK, status)
		var deleted service.AdminTagView
		decodeData(t, response, &deleted)
		require.True(t, deleted.IsDeleted)
		require.Equal(t, before, tagRelationCount(t, gdb, target.ID),
			"标签下架不得删除 post_tags 关联")
		require.NotContains(t, getPostDetail(t, engine, both.ID, actors.Ordinary.Token).Tags, target.Name)

		status, response, _ = performJSON(t, engine, http.MethodPost,
			fmt.Sprintf("/api/v2/admin/tags/%d/restore", target.ID), nil, actors.Admin.Token)
		require.Equal(t, http.StatusOK, status)
		var restored service.AdminTagView
		decodeData(t, response, &restored)
		require.False(t, restored.IsDeleted)
		require.Equal(t, before, tagRelationCount(t, gdb, target.ID))
		require.Contains(t, getPostDetail(t, engine, both.ID, actors.Ordinary.Token).Tags, target.Name,
			"恢复后原有关联必须自动重新展示")

		status, response, _ = performJSON(t, engine, http.MethodGet,
			"/api/v2/admin/tags?is_deleted=true", nil, actors.Admin.Token)
		require.Equal(t, http.StatusOK, status)
		var deletedList service.AdminTagList
		decodeData(t, response, &deletedList)
		require.True(t, adminTagPresent(deletedList.Tags, source.ID))
	})

	t.Run("hot tags use visible post associations", func(t *testing.T) {
		status, response, _ := performJSON(t, engine, http.MethodGet,
			"/api/v2/admin/tags/hot?limit=1", nil, actors.Admin.Token)
		require.Equal(t, http.StatusOK, status)
		var hot service.AdminHotTagList
		decodeData(t, response, &hot)
		require.Len(t, hot.Tags, 1)
		require.Equal(t, target.ID, hot.Tags[0].ID)
		require.EqualValues(t, 2, hot.Tags[0].PostCount)
	})
}

func testAdminTagRoleGuards(
	t *testing.T,
	engine *server.Hertz,
	actors adminActors,
	_ uint64,
	targetID uint64,
) {
	t.Helper()
	const missingID = uint64(9223372036854775807)
	routes := []struct {
		method string
		path   string
		body   any
	}{
		{method: http.MethodGet, path: "/api/v2/admin/tags"},
		{method: http.MethodGet, path: "/api/v2/admin/tags/hot"},
		{method: http.MethodPatch, path: fmt.Sprintf("/api/v2/admin/tags/%d", missingID), body: map[string]any{"name": "权限测试"}},
		{method: http.MethodPost, path: fmt.Sprintf("/api/v2/admin/tags/%d/merge", missingID), body: map[string]any{"target_tag_id": targetID}},
		{method: http.MethodDelete, path: fmt.Sprintf("/api/v2/admin/tags/%d", missingID)},
		{method: http.MethodPost, path: fmt.Sprintf("/api/v2/admin/tags/%d/restore", missingID)},
	}
	for _, route := range routes {
		for _, actor := range []service.AuthResult{actors.Ordinary, actors.Dict} {
			status, response, _ := performJSON(
				t, engine, route.method, route.path, route.body, actor.Token,
			)
			require.Equal(t, http.StatusForbidden, status, "%s %s", route.method, route.path)
			require.Equal(t, apierr.BizPermissionDenied, response.ErrorCode)
		}
		for _, actor := range []service.AuthResult{actors.Admin, actors.Super} {
			status, _, _ := performJSON(
				t, engine, route.method, route.path, route.body, actor.Token,
			)
			require.NotEqual(t, http.StatusUnauthorized, status)
			require.NotEqual(t, http.StatusForbidden, status)
		}
	}
}

func findTagByName(t *testing.T, gdb *gorm.DB, name string) model.Tag {
	t.Helper()
	var tag model.Tag
	require.NoError(t, gdb.Where("name = ?", name).First(&tag).Error)
	return tag
}

func postRevisionCount(t *testing.T, gdb *gorm.DB, postID uint64) int64 {
	t.Helper()
	var count int64
	require.NoError(t, gdb.Model(&model.PostHistory{}).Where("post_id = ?", postID).Count(&count).Error)
	return count
}

func tagRelationCount(t *testing.T, gdb *gorm.DB, tagID uint64) int64 {
	t.Helper()
	var count int64
	require.NoError(t, gdb.Model(&model.PostTag{}).Where("tag_id = ?", tagID).Count(&count).Error)
	return count
}

func getPostDetail(
	t *testing.T,
	engine *server.Hertz,
	postID uint64,
	token string,
) service.PostDetail {
	t.Helper()
	status, response, _ := performJSON(t, engine, http.MethodGet, postPath(postID), nil, token)
	require.Equal(t, http.StatusOK, status)
	var detail service.PostDetail
	decodeData(t, response, &detail)
	return detail
}

func adminTagPresent(tags []service.AdminTagView, id uint64) bool {
	for _, tag := range tags {
		if tag.ID == id {
			return true
		}
	}
	return false
}
