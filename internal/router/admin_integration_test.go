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

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	appconfig "github.com/Foodan-Dev/danshi-backend/internal/config"
	dbinfra "github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/ptime"
	"github.com/Foodan-Dev/danshi-backend/internal/router"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

type adminActors struct {
	Super     service.AuthResult
	Admin     service.AuthResult
	Dict      service.AuthResult
	Multi     service.AuthResult
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

	t.Run("multi-role union and targeted evidence", func(t *testing.T) {
		status, _, _ := performJSON(t, engine, http.MethodGet,
			"/api/v2/admin/posts", nil, actors.Multi.Token)
		require.Equal(t, http.StatusOK, status)
		status, _, _ = performJSON(t, engine, http.MethodGet,
			"/api/v2/admin/dictionary-suggestions", nil, actors.Multi.Token)
		require.Equal(t, http.StatusOK, status)
		status, _, _ = performJSON(t, engine, http.MethodGet,
			"/api/v2/admin/users", nil, actors.Admin.Token)
		require.Equal(t, http.StatusForbidden, status)
		status, _, _ = performJSON(t, engine, http.MethodGet,
			fmt.Sprintf("/api/v2/admin/users/%d", actors.Ordinary.User.ID), nil, actors.Admin.Token)
		require.Equal(t, http.StatusOK, status)
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

	t.Run("admin content lists deleted rows and fixed query budget", func(t *testing.T) {
		testAdminListsAndDeletion(t, engine, gdb, actors, fixture)
	})

	t.Run("post restore audits and rejects unapproved images", func(t *testing.T) {
		testAdminPostRestore(t, engine, gdb, actors, fixture)
	})
}

func testAdminRouteInventory(t *testing.T, engine *server.Hertz) {
	t.Helper()
	require.Len(t, engine.Routes(), 82, "应注册 80 条业务路由与 2 条 runtime 路由")
	operations := make([]string, 0, 17)
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
		"GET /api/v2/admin/users/:user_id",
		"GET /api/v2/admin/users/:user_id/posts",
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
		moderator     bool
		allowedStatus int
		allowedCode   apierr.BizCode
	}
	routes := []routeCase{
		{name: "pending posts", method: http.MethodGet, path: "/api/v2/admin/posts/pending", moderator: true, allowedStatus: http.StatusOK},
		{name: "review post", method: http.MethodPut, path: "/api/v2/admin/posts/" + missingID + "/review", body: map[string]any{"status": model.PostStatusApproved}, moderator: true, allowedStatus: http.StatusNotFound, allowedCode: apierr.BizPostNotFound},
		{name: "posts", method: http.MethodGet, path: "/api/v2/admin/posts", moderator: true, allowedStatus: http.StatusOK},
		{name: "delete post", method: http.MethodDelete, path: "/api/v2/admin/posts/" + missingID, moderator: true, allowedStatus: http.StatusNotFound, allowedCode: apierr.BizPostNotFound},
		{name: "restore post", method: http.MethodPut, path: "/api/v2/admin/posts/" + missingID + "/restore", moderator: true, allowedStatus: http.StatusNotFound, allowedCode: apierr.BizPostNotFound},
		{name: "users", method: http.MethodGet, path: "/api/v2/admin/users", allowedStatus: http.StatusOK},
		{name: "user detail", method: http.MethodGet, path: "/api/v2/admin/users/" + missingID, moderator: true, allowedStatus: http.StatusNotFound, allowedCode: apierr.BizNotFound},
		{name: "user posts", method: http.MethodGet, path: "/api/v2/admin/users/" + missingID + "/posts", moderator: true, allowedStatus: http.StatusNotFound, allowedCode: apierr.BizNotFound},
		{name: "update user status", method: http.MethodPut, path: "/api/v2/admin/users/" + missingID + "/status", body: map[string]any{"ban_is_permanent": false}, moderator: true, allowedStatus: http.StatusNotFound, allowedCode: apierr.BizNotFound},
		{name: "update user role", method: http.MethodPut, path: "/api/v2/admin/users/" + missingID + "/role", body: map[string]any{"role": model.UserRoleModerator, "action": model.UserRoleActionGrant}, allowedStatus: http.StatusNotFound, allowedCode: apierr.BizNotFound},
		{name: "admins", method: http.MethodGet, path: "/api/v2/admin/admins", allowedStatus: http.StatusOK},
		{name: "super admins", method: http.MethodGet, path: "/api/v2/admin/super-admins", allowedStatus: http.StatusOK},
		{name: "comments", method: http.MethodGet, path: "/api/v2/admin/comments", moderator: true, allowedStatus: http.StatusOK},
		{name: "delete comment", method: http.MethodDelete, path: "/api/v2/admin/comments/" + missingID, moderator: true, allowedStatus: http.StatusNotFound, allowedCode: apierr.BizCommentNotFound},
		{name: "restore comment", method: http.MethodPut, path: "/api/v2/admin/comments/" + missingID + "/restore", moderator: true, allowedStatus: http.StatusNotFound, allowedCode: apierr.BizCommentNotFound},
		{name: "pending moderation", method: http.MethodGet, path: "/api/v2/admin/moderation-records/pending", moderator: true, allowedStatus: http.StatusOK},
		{name: "manual review", method: http.MethodPut, path: "/api/v2/admin/moderation-records/" + missingID + "/review", body: map[string]any{"verdict": model.ModerationVerdictPass}, moderator: true, allowedStatus: http.StatusNotFound, allowedCode: apierr.BizNotFound},
	}
	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			assertAdminMatrixResponse(t, engine, route.method, route.path, route.body, "",
				http.StatusUnauthorized, apierr.BizUnauthorized)
			assertAdminMatrixResponse(t, engine, route.method, route.path, route.body,
				actors.Ordinary.Token, http.StatusForbidden, apierr.BizPermissionDenied)
			assertAdminMatrixResponse(t, engine, route.method, route.path, route.body,
				actors.Dict.Token, http.StatusForbidden, apierr.BizPermissionDenied)
			if route.moderator {
				assertAdminMatrixResponse(t, engine, route.method, route.path, route.body,
					actors.Admin.Token, route.allowedStatus, route.allowedCode)
			} else {
				assertAdminMatrixResponse(t, engine, route.method, route.path, route.body,
					actors.Admin.Token, http.StatusForbidden, apierr.BizPermissionDenied)
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
	status, _, _ = performJSON(t, engine, http.MethodPut, timedPath, map[string]any{
		"ban_is_permanent": true, "ban_reason": "再次封禁集成测试",
	}, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	var banRecords []model.UserBanRecord
	require.NoError(t, gdb.Where("user_id = ?", actors.Timed.User.ID).Order("id").Find(&banRecords).Error)
	require.Len(t, banRecords, 3)
	require.Equal(t, "限时封禁集成测试", *banRecords[0].Reason)
	require.Equal(t, model.UserBanActionUnban, banRecords[1].Action)
	require.Equal(t, "再次封禁集成测试", *banRecords[2].Reason)
	require.Error(t, gdb.Model(&model.UserBanRecord{}).Where("id = ?", banRecords[0].ID).
		UpdateColumn("reason", "篡改").Error)
	require.Error(t, gdb.Delete(&model.UserBanRecord{}, banRecords[0].ID).Error)
	status, response, _ := performJSON(t, engine, http.MethodGet,
		fmt.Sprintf("/api/v2/admin/users/%d", actors.Timed.User.ID), nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	var detail service.AdminUserDetail
	decodeData(t, response, &detail)
	require.Len(t, detail.BanRecords, 3)

	illegalPath := fmt.Sprintf("/api/v2/admin/users/%d/status", actors.Illegal.User.ID)
	status, response, _ = performJSON(t, engine, http.MethodPut, illegalPath, map[string]any{
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
		map[string]any{"role": model.UserRoleModerator, "action": model.UserRoleActionGrant}, actors.Admin.Token)
	require.Equal(t, http.StatusForbidden, status)
	status, _, _ = performJSON(t, engine, http.MethodPut, path,
		map[string]any{"role": model.UserRoleModerator, "action": model.UserRoleActionGrant}, actors.Super.Token)
	require.Equal(t, http.StatusOK, status)
	status, _, _ = performJSON(t, engine, http.MethodPut, path,
		map[string]any{"role": model.UserRoleModerator, "action": model.UserRoleActionRevoke}, actors.Super.Token)
	require.Equal(t, http.StatusOK, status)
	status, _, _ = performJSON(t, engine, http.MethodPut, path,
		map[string]any{"role": model.UserRoleModerator, "action": model.UserRoleActionGrant}, actors.Super.Token)
	require.Equal(t, http.StatusOK, status)
	var bindings []model.UserRoleBinding
	require.NoError(t, gdb.Where("user_id = ?", actors.Ordinary.User.ID).Find(&bindings).Error)
	require.Equal(t, []model.UserRole{model.UserRoleModerator}, []model.UserRole{bindings[0].Role})
	var records []model.UserRoleRecord
	require.NoError(t, gdb.Where("user_id = ?", actors.Ordinary.User.ID).Order("id").Find(&records).Error)
	require.Len(t, records, 3)
	require.Equal(t, model.UserRoleActionGrant, records[0].Action)
	require.Equal(t, model.UserRoleActionRevoke, records[1].Action)
	require.Equal(t, model.UserRoleActionGrant, records[2].Action)
	require.Error(t, gdb.Model(&model.UserRoleRecord{}).Where("id = ?", records[0].ID).
		UpdateColumn("action", model.UserRoleActionRevoke).Error)
	require.Error(t, gdb.Delete(&model.UserRoleRecord{}, records[0].ID).Error)
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
		"/api/v2/admin/users?limit=100", nil, actors.Super.Token)
	require.Equal(t, http.StatusOK, status)
	var users service.AdminUserList
	decodeData(t, response, &users)
	require.False(t, adminUserPresent(users.Users, actors.Illegal.User.ID),
		"正常管理员用户列表不得包含注销账号")

	require.NoError(t, gdb.Model(&model.Post{}).Where("id = ?", postIDs[1]).
		UpdateColumn("status", model.PostStatusPending).Error)
	status, response, _ = performJSON(t, engine, http.MethodGet,
		fmt.Sprintf("/api/v2/admin/users/%d/posts?limit=100", actors.Author.User.ID),
		nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	var evidence service.AdminPostList
	decodeData(t, response, &evidence)
	require.True(t, adminPostPresent(evidence.Posts, postIDs[0]), "取证列表必须包含软删除帖子")
	require.True(t, adminPostPresent(evidence.Posts, postIDs[1]), "取证列表必须包含未通过帖子")

	status, response, _ = performJSON(t, engine, http.MethodGet,
		fmt.Sprintf("/api/v2/users/%d/posts?limit=100", actors.Author.User.ID),
		nil, actors.Ordinary.Token)
	require.Equal(t, http.StatusOK, status)
	var public service.PostList
	decodeData(t, response, &public)
	publicIDs := make([]uint64, 0, len(public.Posts))
	for _, post := range public.Posts {
		publicIDs = append(publicIDs, post.ID)
	}
	require.NotContains(t, publicIDs, postIDs[0])
	require.NotContains(t, publicIDs, postIDs[1])
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
		Super: register("admin-super", "超级管理员"), Admin: register("admin-normal", "内容管理员"),
		Dict:     register("admin-dict", "词表管理员"),
		Multi:    register("admin-multi", "复合角色管理员"),
		Ordinary: register("admin-ordinary", "普通用户"), Permanent: register("admin-permanent", "永久封禁用户"),
		Timed: register("admin-timed", "限时封禁用户"), Illegal: register("admin-illegal", "非法组合用户"),
		Author: register("admin-author", "内容作者"), Commenter: register("admin-commenter", "评论作者"),
		SelfAdmin: register("admin-self-ban", "自封管理员"),
		SuperUser: register("admin-super-target", "被封超级管理员"),
	}
	bindings := []model.UserRoleBinding{
		{UserID: actors.Super.User.ID, Role: model.UserRoleSuperAdmin, GrantedAt: time.Now().UTC()},
		{UserID: actors.Admin.User.ID, Role: model.UserRoleModerator, GrantedAt: time.Now().UTC()},
		{UserID: actors.Dict.User.ID, Role: model.UserRoleDictReviewer, GrantedAt: time.Now().UTC()},
		{UserID: actors.Multi.User.ID, Role: model.UserRoleDictReviewer, GrantedAt: time.Now().UTC()},
		{UserID: actors.Multi.User.ID, Role: model.UserRoleModerator, GrantedAt: time.Now().UTC()},
		{UserID: actors.SelfAdmin.User.ID, Role: model.UserRoleModerator, GrantedAt: time.Now().UTC()},
		{UserID: actors.SuperUser.User.ID, Role: model.UserRoleSuperAdmin, GrantedAt: time.Now().UTC()},
	}
	require.NoError(t, gdb.Create(&bindings).Error)
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
