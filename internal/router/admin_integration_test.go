package router_test

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	hertzconfig "github.com/cloudwego/hertz/pkg/common/config"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	appconfig "github.com/jingyijun/danshi_backend_go/internal/config"
	dbinfra "github.com/jingyijun/danshi_backend_go/internal/infra/db"
	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/ptime"
	"github.com/jingyijun/danshi_backend_go/internal/router"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

type adminActors struct {
	Super     service.AuthResult
	Admin     service.AuthResult
	Ordinary  service.AuthResult
	Permanent service.AuthResult
	Timed     service.AuthResult
	Illegal   service.AuthResult
	Author    service.AuthResult
	Commenter service.AuthResult
	SelfAdmin service.AuthResult
	SuperUser service.AuthResult
}

func TestAdminDomainAgainstPostgres(t *testing.T) {
	gdb, database := openAuthPostgres(t)
	cfg := authTestConfig()
	sender := newCaptureEmailSender()
	engine := authTestEngine(cfg, database, sender)
	actors := registerAdminActors(t, engine, sender, gdb)
	fixture := loadPostFixture(t, gdb)
	reviewEngine := adminTestEngine(
		cfg, database, sender, fixedVerdictModerator(model.ModerationVerdictReview),
	)

	t.Run("route inventory and role guards", func(t *testing.T) {
		testAdminRouteInventory(t, engine)
		testAdminRoleGuards(t, engine, actors)
	})

	t.Run("permanent timed unban constraint and session revocation", func(t *testing.T) {
		testAdminBanStates(t, engine, gdb, actors)
	})

	t.Run("role update requires super admin", func(t *testing.T) {
		testAdminRoleUpdate(t, engine, gdb, actors)
	})

	t.Run("post review appends manual row", func(t *testing.T) {
		testAdminPostReview(t, reviewEngine, gdb, actors, fixture)
	})

	t.Run("generic queue review restore and counter refill", func(t *testing.T) {
		testAdminCommentReviewAndRestore(t, engine, reviewEngine, gdb, actors, fixture)
	})

	t.Run("admin deletion lists deleted rows and fixed query budget", func(t *testing.T) {
		testAdminListsAndDeletion(t, engine, gdb, actors, fixture)
	})

	t.Run("post restore audits and rejects unapproved images", func(t *testing.T) {
		testAdminPostRestore(t, engine, gdb, actors, fixture)
	})
}

func testAdminRouteInventory(t *testing.T, engine *server.Hertz) {
	t.Helper()
	require.Len(t, engine.Routes(), 79, "P2 全域应注册 77 条业务路由与 2 条 runtime 路由")
	operations := make([]string, 0, 15)
	for _, route := range engine.Routes() {
		if isAdminDomainPath(route.Path) {
			operations = append(operations, route.Method+" "+route.Path)
		}
	}
	require.ElementsMatch(t, []string{
		"GET /api/v2/admin/posts/pending",
		"PUT /api/v2/admin/posts/:post_id/review",
		"GET /api/v2/admin/posts",
		"DELETE /api/v2/admin/posts/:post_id",
		"PUT /api/v2/admin/posts/:post_id/restore",
		"GET /api/v2/admin/users",
		"PUT /api/v2/admin/users/:user_id/status",
		"PUT /api/v2/admin/users/:user_id/role",
		"GET /api/v2/admin/admins",
		"GET /api/v2/admin/super-admins",
		"GET /api/v2/admin/comments",
		"DELETE /api/v2/admin/comments/:comment_id",
		"PUT /api/v2/admin/comments/:comment_id/restore",
		"GET /api/v2/admin/moderation-records/pending",
		"PUT /api/v2/admin/moderation-records/:moderation_record_id/review",
	}, operations)
}

func isAdminDomainPath(path string) bool {
	return strings.HasPrefix(path, "/api/v2/admin/posts") ||
		strings.HasPrefix(path, "/api/v2/admin/users") ||
		path == "/api/v2/admin/admins" || path == "/api/v2/admin/super-admins" ||
		strings.HasPrefix(path, "/api/v2/admin/comments") ||
		strings.HasPrefix(path, "/api/v2/admin/moderation-records")
}

func testAdminRoleGuards(t *testing.T, engine *server.Hertz, actors adminActors) {
	t.Helper()
	const missingID = "9223372036854775807"
	type routeCase struct {
		name          string
		method        string
		path          string
		body          any
		superOnly     bool
		allowedStatus int
		allowedCode   apierr.BizCode
	}
	routes := []routeCase{
		{name: "pending posts", method: http.MethodGet, path: "/api/v2/admin/posts/pending", allowedStatus: http.StatusOK},
		{name: "review post", method: http.MethodPut, path: "/api/v2/admin/posts/" + missingID + "/review", body: map[string]any{"status": model.PostStatusApproved}, allowedStatus: http.StatusNotFound, allowedCode: apierr.BizPostNotFound},
		{name: "posts", method: http.MethodGet, path: "/api/v2/admin/posts", allowedStatus: http.StatusOK},
		{name: "delete post", method: http.MethodDelete, path: "/api/v2/admin/posts/" + missingID, allowedStatus: http.StatusNotFound, allowedCode: apierr.BizPostNotFound},
		{name: "restore post", method: http.MethodPut, path: "/api/v2/admin/posts/" + missingID + "/restore", allowedStatus: http.StatusNotFound, allowedCode: apierr.BizPostNotFound},
		{name: "users", method: http.MethodGet, path: "/api/v2/admin/users", allowedStatus: http.StatusOK},
		{name: "update user status", method: http.MethodPut, path: "/api/v2/admin/users/" + missingID + "/status", body: map[string]any{"ban_is_permanent": false}, allowedStatus: http.StatusNotFound, allowedCode: apierr.BizNotFound},
		{name: "update user role", method: http.MethodPut, path: "/api/v2/admin/users/" + missingID + "/role", body: map[string]any{"role": model.UserRoleUser}, superOnly: true, allowedStatus: http.StatusNotFound, allowedCode: apierr.BizNotFound},
		{name: "admins", method: http.MethodGet, path: "/api/v2/admin/admins", superOnly: true, allowedStatus: http.StatusOK},
		{name: "super admins", method: http.MethodGet, path: "/api/v2/admin/super-admins", superOnly: true, allowedStatus: http.StatusOK},
		{name: "comments", method: http.MethodGet, path: "/api/v2/admin/comments", allowedStatus: http.StatusOK},
		{name: "delete comment", method: http.MethodDelete, path: "/api/v2/admin/comments/" + missingID, allowedStatus: http.StatusNotFound, allowedCode: apierr.BizCommentNotFound},
		{name: "restore comment", method: http.MethodPut, path: "/api/v2/admin/comments/" + missingID + "/restore", allowedStatus: http.StatusNotFound, allowedCode: apierr.BizCommentNotFound},
		{name: "pending moderation", method: http.MethodGet, path: "/api/v2/admin/moderation-records/pending", allowedStatus: http.StatusOK},
		{name: "manual review", method: http.MethodPut, path: "/api/v2/admin/moderation-records/" + missingID + "/review", body: map[string]any{"verdict": model.ModerationVerdictPass}, allowedStatus: http.StatusNotFound, allowedCode: apierr.BizNotFound},
	}
	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			assertAdminMatrixResponse(t, engine, route.method, route.path, route.body, "",
				http.StatusUnauthorized, apierr.BizUnauthorized)
			assertAdminMatrixResponse(t, engine, route.method, route.path, route.body,
				actors.Ordinary.Token, http.StatusForbidden, apierr.BizPermissionDenied)
			if route.superOnly {
				assertAdminMatrixResponse(t, engine, route.method, route.path, route.body,
					actors.Admin.Token, http.StatusForbidden, apierr.BizPermissionDenied)
			} else {
				assertAdminMatrixResponse(t, engine, route.method, route.path, route.body,
					actors.Admin.Token, route.allowedStatus, route.allowedCode)
			}
			assertAdminMatrixResponse(t, engine, route.method, route.path, route.body,
				actors.Super.Token, route.allowedStatus, route.allowedCode)
		})
	}
}

func assertAdminMatrixResponse(
	t *testing.T,
	engine *server.Hertz,
	method string,
	path string,
	body any,
	token string,
	wantStatus int,
	wantCode apierr.BizCode,
) {
	t.Helper()
	status, response, _ := performJSON(t, engine, method, path, body, token)
	require.Equal(t, wantStatus, status, "%s %s", method, path)
	require.Equal(t, wantCode, response.ErrorCode, "%s %s", method, path)
}

func testAdminBanStates(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	actors adminActors,
) {
	t.Helper()
	permanentPath := fmt.Sprintf("/api/v2/admin/users/%d/status", actors.Permanent.User.ID)
	status, _, _ := performJSON(t, engine, http.MethodPut, permanentPath, map[string]any{
		"ban_is_permanent": true, "ban_reason": "永久封禁集成测试",
	}, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	var permanent model.User
	require.NoError(t, gdb.First(&permanent, actors.Permanent.User.ID).Error)
	require.True(t, permanent.BanIsPermanent)
	require.Nil(t, permanent.BannedUntil)
	require.Equal(t, "永久封禁集成测试", *permanent.BanReason)
	require.Equal(t, actors.Admin.User.ID, *permanent.BannedBy)
	var session model.UserSession
	require.NoError(t, gdb.Where("user_id = ?", permanent.ID).First(&session).Error)
	require.NotNil(t, session.RevokedAt, "封禁与 RevokeAll 必须在同一事务提交")
	assertUnauthorized(t, engine, http.MethodGet, "/api/v2/auth/me", nil, actors.Permanent.Token)

	timedPath := fmt.Sprintf("/api/v2/admin/users/%d/status", actors.Timed.User.ID)
	until := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Microsecond)
	status, _, _ = performJSON(t, engine, http.MethodPut, timedPath, map[string]any{
		"ban_is_permanent": false, "banned_until": ptime.Format(until),
		"ban_reason": "限时封禁集成测试",
	}, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	var timed model.User
	require.NoError(t, gdb.First(&timed, actors.Timed.User.ID).Error)
	require.False(t, timed.BanIsPermanent)
	require.NotNil(t, timed.BannedUntil)
	require.WithinDuration(t, until, *timed.BannedUntil, time.Microsecond)
	require.Equal(t, actors.Admin.User.ID, *timed.BannedBy)
	assertUnauthorized(t, engine, http.MethodGet, "/api/v2/auth/me", nil, actors.Timed.Token)

	status, _, _ = performJSON(t, engine, http.MethodPut, timedPath,
		map[string]any{"ban_is_permanent": false}, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	var unbanned model.User
	require.NoError(t, gdb.First(&unbanned, actors.Timed.User.ID).Error)
	require.False(t, unbanned.BanIsPermanent)
	require.Nil(t, unbanned.BannedUntil)
	require.Nil(t, unbanned.BanReason)
	require.Nil(t, unbanned.BannedBy)

	illegalPath := fmt.Sprintf("/api/v2/admin/users/%d/status", actors.Illegal.User.ID)
	status, response, _ := performJSON(t, engine, http.MethodPut, illegalPath, map[string]any{
		"ban_is_permanent": true,
	}, actors.Admin.Token)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, apierr.BizValidation, response.ErrorCode)
	requireAdminFieldError(t, response, "ban_reason", apierr.FieldRequired)
	var unchanged model.User
	require.NoError(t, gdb.First(&unchanged, actors.Illegal.User.ID).Error)
	require.False(t, unchanged.BanIsPermanent)
	require.Nil(t, unchanged.BannedUntil)
	require.Nil(t, unchanged.BanReason)

	status, response, _ = performJSON(t, engine, http.MethodPut, illegalPath, map[string]any{
		"ban_is_permanent": true, "banned_until": ptime.Format(until), "ban_reason": "非法组合",
	}, actors.Admin.Token)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, apierr.BizValidation, response.ErrorCode)
	requireAdminFieldError(t, response, "banned_until", apierr.FieldConflict)
	require.NoError(t, gdb.First(&unchanged, actors.Illegal.User.ID).Error)
	require.False(t, unchanged.BanIsPermanent)
	require.Nil(t, unchanged.BannedUntil)
	status, _, _ = performJSON(t, engine, http.MethodPut, illegalPath, map[string]any{
		"ban_is_permanent": true, "ban_reason": "超级管理员封禁集成测试",
	}, actors.Super.Token)
	require.Equal(t, http.StatusOK, status, "super_admin 仍应继承管理员的封禁权限")
	status, _, _ = performJSON(t, engine, http.MethodPut, illegalPath,
		map[string]any{"ban_is_permanent": false}, actors.Super.Token)
	require.Equal(t, http.StatusOK, status)
	err := gdb.Model(&model.User{}).Where("id = ?", actors.Illegal.User.ID).UpdateColumns(map[string]any{
		"ban_is_permanent": true, "banned_until": until, "ban_reason": "数据库约束测试",
	}).Error
	require.ErrorContains(t, err, "users_ban_kind_check", "数据库约束必须拒绝永久与限时并存")

	selfPath := fmt.Sprintf("/api/v2/admin/users/%d/status", actors.SelfAdmin.User.ID)
	status, response, _ = performJSON(t, engine, http.MethodPut, selfPath, map[string]any{
		"ban_is_permanent": true, "ban_reason": "管理员主动封禁自己",
	}, actors.SelfAdmin.Token)
	require.Equal(t, http.StatusOK, status, "当前契约允许管理员封禁自己并立即撤销会话")
	require.Empty(t, response.ErrorCode)
	assertUnauthorized(t, engine, http.MethodGet, "/api/v2/auth/me", nil, actors.SelfAdmin.Token)
	var selfBanned model.User
	require.NoError(t, gdb.First(&selfBanned, actors.SelfAdmin.User.ID).Error)
	require.True(t, selfBanned.BanIsPermanent)
	require.Equal(t, actors.SelfAdmin.User.ID, *selfBanned.BannedBy)

	superPath := fmt.Sprintf("/api/v2/admin/users/%d/status", actors.SuperUser.User.ID)
	status, response, _ = performJSON(t, engine, http.MethodPut, superPath, map[string]any{
		"ban_is_permanent": true, "ban_reason": "管理员封禁超级管理员目标",
	}, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status, "普通 admin 的封禁权限覆盖 super_admin 目标")
	require.Empty(t, response.ErrorCode)
	assertUnauthorized(t, engine, http.MethodGet, "/api/v2/auth/me", nil, actors.SuperUser.Token)
	var superBanned model.User
	require.NoError(t, gdb.First(&superBanned, actors.SuperUser.User.ID).Error)
	require.True(t, superBanned.BanIsPermanent)
	require.Equal(t, actors.Admin.User.ID, *superBanned.BannedBy)
}

func testAdminRoleUpdate(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	actors adminActors,
) {
	t.Helper()
	path := fmt.Sprintf("/api/v2/admin/users/%d/role", actors.Ordinary.User.ID)
	status, _, _ := performJSON(t, engine, http.MethodPut, path,
		map[string]any{"role": model.UserRoleAdmin}, actors.Admin.Token)
	require.Equal(t, http.StatusForbidden, status)
	status, _, _ = performJSON(t, engine, http.MethodPut, path,
		map[string]any{"role": model.UserRoleAdmin}, actors.Super.Token)
	require.Equal(t, http.StatusOK, status)
	var ordinary model.User
	require.NoError(t, gdb.First(&ordinary, actors.Ordinary.User.ID).Error)
	require.Equal(t, model.UserRoleAdmin, ordinary.Role)
}

func testAdminPostReview(
	t *testing.T,
	reviewEngine *server.Hertz,
	gdb *gorm.DB,
	actors adminActors,
	fixture postFixture,
) {
	t.Helper()
	post := createPost(t, reviewEngine, actors.Author.Token,
		sharePostPayload(fixture, "管理员帖子复核", []string{"人工复核"}))
	require.Equal(t, model.PostStatusPending, post.Status)
	var machine model.ModerationRecord
	require.NoError(t, gdb.Where("post_id = ? AND verdict = ?", post.ID, model.ModerationVerdictReview).
		First(&machine).Error)
	status, response, _ := performJSON(t, reviewEngine, http.MethodGet,
		"/api/v2/admin/posts/pending", nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	var pending service.AdminPostList
	decodeData(t, response, &pending)
	require.True(t, adminPostPresent(pending.Posts, post.ID))

	status, _, _ = performJSON(t, reviewEngine, http.MethodPut,
		fmt.Sprintf("/api/v2/admin/posts/%d/review", post.ID),
		map[string]any{"status": model.PostStatusApproved, "feedback": "人工确认通过"}, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	var unchanged model.ModerationRecord
	require.NoError(t, gdb.First(&unchanged, machine.ID).Error)
	require.Equal(t, machine.Provider, unchanged.Provider)
	require.Equal(t, model.ModerationVerdictReview, unchanged.Verdict)
	require.Nil(t, unchanged.ReviewerID)
	var manual model.ModerationRecord
	require.NoError(t, gdb.Where("supersedes_id = ?", machine.ID).First(&manual).Error)
	require.Equal(t, model.ModerationProviderManual, manual.Provider)
	require.Equal(t, model.ModerationVerdictPass, manual.Verdict)
	require.Equal(t, actors.Admin.User.ID, *manual.ReviewerID)
	var stored model.Post
	require.NoError(t, gdb.First(&stored, post.ID).Error)
	require.Equal(t, model.PostStatusApproved, stored.Status)
	status, response, _ = performJSON(t, reviewEngine, http.MethodPut,
		fmt.Sprintf("/api/v2/admin/posts/%d/review", post.ID),
		map[string]any{"status": model.PostStatusRejected}, actors.Admin.Token)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, apierr.BizModerationNotPending, response.ErrorCode)
	var manualCount int64
	require.NoError(t, gdb.Model(&model.ModerationRecord{}).
		Where("supersedes_id = ?", machine.ID).Count(&manualCount).Error)
	require.EqualValues(t, 1, manualCount)
}

func testAdminCommentReviewAndRestore(
	t *testing.T,
	engine *server.Hertz,
	reviewEngine *server.Hertz,
	gdb *gorm.DB,
	actors adminActors,
	fixture postFixture,
) {
	t.Helper()
	post := createPost(t, engine, actors.Author.Token,
		sharePostPayload(fixture, "评论人工复核帖子", []string{"评论复核"}))
	comment := createComment(t, reviewEngine, actors.Commenter.Token, post.ID,
		map[string]any{"content": "待人工复核评论"})
	var machine model.ModerationRecord
	require.NoError(t, gdb.Where("comment_id = ? AND verdict = ?",
		comment.Comment.ID, model.ModerationVerdictReview).First(&machine).Error)
	status, response, _ := performJSON(t, engine, http.MethodGet,
		"/api/v2/admin/moderation-records/pending", nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	var queue service.AdminModerationList
	decodeData(t, response, &queue)
	require.True(t, moderationRecordPresent(queue.Records, machine.ID))

	status, _, _ = performJSON(t, engine, http.MethodPut,
		fmt.Sprintf("/api/v2/admin/moderation-records/%d/review", machine.ID),
		map[string]any{"verdict": model.ModerationVerdictBlock, "labels": []string{"off_topic"}},
		actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	var original model.ModerationRecord
	require.NoError(t, gdb.First(&original, machine.ID).Error)
	require.Equal(t, model.ModerationVerdictReview, original.Verdict)
	var manual model.ModerationRecord
	require.NoError(t, gdb.Where("supersedes_id = ?", machine.ID).First(&manual).Error)
	require.Equal(t, model.ModerationVerdictBlock, manual.Verdict)
	status, response, _ = performJSON(t, engine, http.MethodPut,
		fmt.Sprintf("/api/v2/admin/moderation-records/%d/review", machine.ID),
		map[string]any{"verdict": model.ModerationVerdictPass}, actors.Admin.Token)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, apierr.BizConflict, response.ErrorCode)
	var manualCount int64
	require.NoError(t, gdb.Model(&model.ModerationRecord{}).
		Where("supersedes_id = ?", machine.ID).Count(&manualCount).Error)
	require.EqualValues(t, 1, manualCount)
	var deleted model.Comment
	require.NoError(t, gdb.First(&deleted, comment.Comment.ID).Error)
	require.Equal(t, model.DeleteReasonModeration, *deleted.DeletedReason)
	var postAfterDelete model.Post
	require.NoError(t, gdb.First(&postAfterDelete, post.ID).Error)
	require.Zero(t, postAfterDelete.CommentCount)

	status, response, _ = performJSON(t, engine, http.MethodPut,
		fmt.Sprintf("/api/v2/admin/comments/%d/restore", comment.Comment.ID), nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	var restoredResult service.AdminCommentRestoreResult
	decodeData(t, response, &restoredResult)
	require.NotZero(t, restoredResult.ModerationRecordID)
	var restored model.Comment
	require.NoError(t, gdb.First(&restored, comment.Comment.ID).Error)
	require.Nil(t, restored.DeletedAt)
	require.Nil(t, restored.DeletedReason)
	require.Nil(t, restored.DeletedBy)
	require.NoError(t, gdb.First(&postAfterDelete, post.ID).Error)
	require.EqualValues(t, 1, postAfterDelete.CommentCount, "恢复只能靠 deleted_at 触发器回补计数")
	var audit model.ModerationRecord
	require.NoError(t, gdb.First(&audit, restoredResult.ModerationRecordID).Error)
	require.Equal(t, model.ModerationProvider("admin_restore"), audit.Provider)
	var auditData struct {
		ReviewerID uint64 `json:"reviewer_id"`
	}
	require.NoError(t, json.Unmarshal(audit.RawResponse, &auditData))
	require.Equal(t, actors.Admin.User.ID, auditData.ReviewerID)

	status, response, _ = performJSON(t, engine, http.MethodPut,
		fmt.Sprintf("/api/v2/admin/comments/%d/restore", comment.Comment.ID), nil, actors.Admin.Token)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, apierr.BizContentNotRestorable, response.ErrorCode)
	var restoreAudits int64
	require.NoError(t, gdb.Model(&model.ModerationRecord{}).
		Where("comment_id = ? AND provider = ?", comment.Comment.ID, model.ModerationProvider("admin_restore")).
		Count(&restoreAudits).Error)
	require.EqualValues(t, 1, restoreAudits)

	nonModeration := createComment(t, engine, actors.Commenter.Token, post.ID,
		map[string]any{"content": "管理员删除的评论不可按误杀恢复"})
	status, response, _ = performJSON(t, engine, http.MethodDelete,
		fmt.Sprintf("/api/v2/admin/comments/%d", nonModeration.Comment.ID), nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	status, response, _ = performJSON(t, engine, http.MethodPut,
		fmt.Sprintf("/api/v2/admin/comments/%d/restore", nonModeration.Comment.ID), nil, actors.Admin.Token)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, apierr.BizContentNotRestorable, response.ErrorCode)
}

func testAdminListsAndDeletion(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	actors adminActors,
	fixture postFixture,
) {
	t.Helper()
	postIDs := make([]uint64, 0, 6)
	commentIDs := make([]uint64, 0, 6)
	for index := range 6 {
		post := createPost(t, engine, actors.Author.Token, sharePostPayload(
			fixture, fmt.Sprintf("管理列表帖子 %d", index), []string{fmt.Sprintf("管理列表%d", index)},
		))
		comment := createComment(t, engine, actors.Commenter.Token, post.ID,
			map[string]any{"content": fmt.Sprintf("管理列表评论 %d", index)})
		postIDs = append(postIDs, post.ID)
		commentIDs = append(commentIDs, comment.Comment.ID)
	}
	status, _, _ := performJSON(t, engine, http.MethodDelete,
		fmt.Sprintf("/api/v2/admin/comments/%d", commentIDs[0]), nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	status, _, _ = performJSON(t, engine, http.MethodDelete,
		fmt.Sprintf("/api/v2/admin/posts/%d", postIDs[0]), nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	var deletedPost model.Post
	require.NoError(t, gdb.First(&deletedPost, postIDs[0]).Error)
	require.Equal(t, model.DeleteReasonAdmin, *deletedPost.DeletedReason)
	require.Equal(t, actors.Admin.User.ID, *deletedPost.DeletedBy)
	var deletedComment model.Comment
	require.NoError(t, gdb.First(&deletedComment, commentIDs[0]).Error)
	require.Equal(t, model.DeleteReasonAdmin, *deletedComment.DeletedReason)
	require.Equal(t, actors.Admin.User.ID, *deletedComment.DeletedBy)

	deletedAt := time.Now().UTC()
	require.NoError(t, gdb.Model(&model.User{}).Where("id = ?", actors.Illegal.User.ID).
		UpdateColumn("deleted_at", deletedAt).Error)
	status, response, _ := performJSON(t, engine, http.MethodGet,
		"/api/v2/admin/posts?limit=100", nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	var posts service.AdminPostList
	decodeData(t, response, &posts)
	require.True(t, adminPostPresent(posts.Posts, postIDs[0]), "管理员帖子列表必须包含软删除行")
	status, response, _ = performJSON(t, engine, http.MethodGet,
		"/api/v2/admin/comments?limit=100", nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	var comments service.AdminCommentList
	decodeData(t, response, &comments)
	require.True(t, adminCommentPresent(comments.Comments, commentIDs[0]))
	status, response, _ = performJSON(t, engine, http.MethodGet,
		"/api/v2/admin/users?limit=100", nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	var users service.AdminUserList
	decodeData(t, response, &users)
	require.True(t, adminUserPresent(users.Users, actors.Illegal.User.ID))
}

func testAdminPostRestore(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	actors adminActors,
	fixture postFixture,
) {
	t.Helper()
	post := createPost(t, engine, actors.Author.Token,
		sharePostPayload(fixture, "帖子误杀恢复", []string{"帖子恢复"}))
	markPostModerationDeleted(t, gdb, post.ID)
	status, response, _ := performJSON(t, engine, http.MethodPut,
		fmt.Sprintf("/api/v2/admin/posts/%d/restore", post.ID), nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	var restored service.AdminPostRestoreResult
	decodeData(t, response, &restored)
	var stored model.Post
	require.NoError(t, gdb.First(&stored, post.ID).Error)
	require.Nil(t, stored.DeletedAt)
	var audit model.ModerationRecord
	require.NoError(t, gdb.First(&audit, restored.ModerationRecordID).Error)
	require.Equal(t, model.ModerationProvider("admin_restore"), audit.Provider)
	status, response, _ = performJSON(t, engine, http.MethodPut,
		fmt.Sprintf("/api/v2/admin/posts/%d/restore", post.ID), nil, actors.Admin.Token)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, apierr.BizContentNotRestorable, response.ErrorCode)
	var restoreAudits int64
	require.NoError(t, gdb.Model(&model.ModerationRecord{}).
		Where("post_id = ? AND provider = ?", post.ID, model.ModerationProvider("admin_restore")).
		Count(&restoreAudits).Error)
	require.EqualValues(t, 1, restoreAudits)

	nonModeration := createPost(t, engine, actors.Author.Token,
		sharePostPayload(fixture, "管理员删除不可按误杀恢复", []string{"非误杀"}))
	status, response, _ = performJSON(t, engine, http.MethodDelete,
		fmt.Sprintf("/api/v2/admin/posts/%d", nonModeration.ID), nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	status, response, _ = performJSON(t, engine, http.MethodPut,
		fmt.Sprintf("/api/v2/admin/posts/%d/restore", nonModeration.ID), nil, actors.Admin.Token)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, apierr.BizContentNotRestorable, response.ErrorCode)

	asset := createPostAsset(t, gdb, actors.Author.User.ID, "admin-unsafe-restore")
	payload := sharePostPayload(fixture, "违规图片不可恢复", []string{"图片恢复"})
	payload["images"] = []string{asset.PublicURL}
	unsafePost := createPost(t, engine, actors.Author.Token, payload)
	require.NoError(t, gdb.Model(&model.ImageAsset{}).Where("id = ?", asset.ID).
		UpdateColumn("moderation", model.ModerationStatusBlock).Error)
	markPostModerationDeleted(t, gdb, unsafePost.ID)
	status, response, _ = performJSON(t, engine, http.MethodPut,
		fmt.Sprintf("/api/v2/admin/posts/%d/restore", unsafePost.ID), nil, actors.Admin.Token)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "image_not_approved", string(response.ErrorCode))
	var unsafeStored model.Post
	require.NoError(t, gdb.First(&unsafeStored, unsafePost.ID).Error)
	require.NotNil(t, unsafeStored.DeletedAt)
}

func registerAdminActors(
	t *testing.T,
	engine *server.Hertz,
	sender *captureEmailSender,
	gdb *gorm.DB,
) adminActors {
	t.Helper()
	register := func(local, name string) service.AuthResult {
		return registerPostTestUser(t, engine, sender, local+"@fdueat.com", name)
	}
	actors := adminActors{
		Super: register("admin-super", "超级管理员"), Admin: register("admin-normal", "管理员"),
		Ordinary: register("admin-ordinary", "普通用户"), Permanent: register("admin-permanent", "永久封禁用户"),
		Timed: register("admin-timed", "限时封禁用户"), Illegal: register("admin-illegal", "非法组合用户"),
		Author: register("admin-author", "内容作者"), Commenter: register("admin-commenter", "评论作者"),
		SelfAdmin: register("admin-self-ban", "自封管理员"),
		SuperUser: register("admin-super-target", "被封超级管理员"),
	}
	require.NoError(t, gdb.Model(&model.User{}).Where("id = ?", actors.Super.User.ID).
		UpdateColumn("role", model.UserRoleSuperAdmin).Error)
	require.NoError(t, gdb.Model(&model.User{}).Where("id = ?", actors.Admin.User.ID).
		UpdateColumn("role", model.UserRoleAdmin).Error)
	require.NoError(t, gdb.Model(&model.User{}).Where("id = ?", actors.SelfAdmin.User.ID).
		UpdateColumn("role", model.UserRoleAdmin).Error)
	require.NoError(t, gdb.Model(&model.User{}).Where("id = ?", actors.SuperUser.User.ID).
		UpdateColumn("role", model.UserRoleSuperAdmin).Error)
	return actors
}

func adminTestEngine(
	cfg appconfig.Config,
	database *dbinfra.DB,
	sender service.VerificationEmailSender,
	moderator service.ContentModerator,
) *server.Hertz {
	engine := server.New(
		server.WithHandleMethodNotAllowed(true),
		hertzconfig.Option{F: func(_ *hertzconfig.Options) {}},
	)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router.Register(engine, router.Deps{
		Config: cfg, DB: database, Log: log, EmailSender: sender, ContentModerator: moderator,
	})
	return engine
}

func markPostModerationDeleted(t *testing.T, gdb *gorm.DB, postID uint64) {
	t.Helper()
	require.NoError(t, gdb.Model(&model.Post{}).Where("id = ?", postID).UpdateColumns(map[string]any{
		"deleted_at": time.Now().UTC(), "deleted_reason": model.DeleteReasonModeration, "deleted_by": nil,
	}).Error)
}

func adminPostPresent(posts []service.AdminPostView, id uint64) bool {
	for _, post := range posts {
		if post.ID == id {
			return true
		}
	}
	return false
}

func adminCommentPresent(comments []service.AdminCommentView, id uint64) bool {
	for _, comment := range comments {
		if comment.ID == id {
			return true
		}
	}
	return false
}

func adminUserPresent(users []service.AdminUserView, id uint64) bool {
	for _, user := range users {
		if user.ID == id {
			return true
		}
	}
	return false
}

func moderationRecordPresent(records []service.AdminModerationView, id uint64) bool {
	for _, record := range records {
		if record.ID == id {
			return true
		}
	}
	return false
}

func requireAdminFieldError(
	t *testing.T,
	response testAPIResponse,
	field string,
	code apierr.FieldCode,
) {
	t.Helper()
	var data struct {
		Errors []apierr.FieldError `json:"errors"`
	}
	decodeData(t, response, &data)
	for _, item := range data.Errors {
		if item.Field == field && item.Code == code {
			return
		}
	}
	t.Fatalf("未找到字段错误 field=%s code=%s，实际=%+v", field, code, data.Errors)
}
