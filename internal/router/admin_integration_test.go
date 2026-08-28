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
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	appconfig "github.com/Foodan-Dev/danshi-backend/internal/config"
	dbinfra "github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/ptime"
	"github.com/Foodan-Dev/danshi-backend/internal/router"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
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
	blockEngine := adminTestEngine(
		cfg, database, sender, fixedVerdictModerator(model.ModerationVerdictBlock),
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

	t.Run("edited content hides stale pending reviews", func(t *testing.T) {
		testEditedContentHidesStalePendingReviews(t, engine, reviewEngine, gdb, actors, fixture)
	})

	t.Run("block queue priority and manual review", func(t *testing.T) {
		testBlockQueuePriorityAndManualReview(t, engine, blockEngine, gdb, actors, fixture)
	})

	t.Run("moderators read version moderation while authors see redacted status", func(t *testing.T) {
		testHistoryModerationVisibility(t, cfg, database, sender, engine, actors, fixture)
	})

	t.Run("generic queue review restore and counter refill", func(t *testing.T) {
		testAdminCommentReviewAndRestore(t, engine, reviewEngine, gdb, actors, fixture)
	})

	t.Run("admin content lists deleted rows and fixed query budget", func(t *testing.T) {
		testAdminListsAndDeletion(t, engine, gdb, actors, fixture)
	})

	t.Run("admin post deletion reconciles unshared image access", func(t *testing.T) {
		testAdminPostDeleteImageAccess(t, engine, gdb, actors, fixture)
	})

	t.Run("author admin deleting own post uses author identity", func(t *testing.T) {
		testAuthorAdminDeleteUsesAuthorIdentity(t, engine, gdb, actors, fixture)
	})

	t.Run("pending post reference keeps shared image public", func(t *testing.T) {
		testAdminPostDeleteKeepsImageReferencedByPendingPost(t, engine, gdb, actors, fixture)
	})

	t.Run("post restore audits and rejects unapproved images", func(t *testing.T) {
		testAdminPostRestore(t, engine, gdb, actors, fixture)
	})
}

func testAdminRouteInventory(t *testing.T, engine *server.Hertz) {
	t.Helper()
	require.Len(t, engine.Routes(), 99, "应注册 97 条业务路由与 2 条 runtime 路由")
	operations := make([]string, 0, 24)
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
		"GET /api/v2/admin/images/:image_asset_id",
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
		"GET /api/v2/admin/tags",
		"GET /api/v2/admin/tags/hot",
		"PATCH /api/v2/admin/tags/:tag_id",
		"POST /api/v2/admin/tags/:tag_id/merge",
		"DELETE /api/v2/admin/tags/:tag_id",
		"POST /api/v2/admin/tags/:tag_id/restore",
		"GET /api/v2/admin/moderation-records/pending",
		"PUT /api/v2/admin/moderation-records/:moderation_record_id/review",
	}, operations)
}

func isAdminDomainPath(path string) bool {
	return strings.HasPrefix(path, "/api/v2/admin/posts") ||
		strings.HasPrefix(path, "/api/v2/admin/images") ||
		strings.HasPrefix(path, "/api/v2/admin/users") ||
		path == "/api/v2/admin/admins" || path == "/api/v2/admin/super-admins" ||
		strings.HasPrefix(path, "/api/v2/admin/comments") ||
		strings.HasPrefix(path, "/api/v2/admin/tags") ||
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
		{name: "image detail", method: http.MethodGet, path: "/api/v2/admin/images/" + missingID, moderator: true, allowedStatus: http.StatusNotFound, allowedCode: apierr.BizUploadNotFound},
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
		"banned_until": ptime.Format(until), "ban_reason": "限时封禁集成测试",
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
		"ban_reason": "缺少封禁时长",
	}, actors.Admin.Token)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, apierr.BizValidation, response.ErrorCode)
	require.Equal(t, "封禁时必须设置 banned_until 或将 ban_is_permanent 设为 true", response.Message)
	requireAdminFieldError(t, response, "ban_is_permanent", apierr.FieldRequired)

	status, response, _ = performJSON(t, engine, http.MethodPut, illegalPath, map[string]any{
		"ban_is_permanent": true, "ban_reason": " \t\n ",
	}, actors.Admin.Token)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, apierr.BizValidation, response.ErrorCode)
	require.Equal(t, "ban_reason 必填且不能为空", response.Message)
	requireAdminFieldError(t, response, "ban_reason", apierr.FieldRequired)

	past := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	status, response, _ = performJSON(t, engine, http.MethodPut, illegalPath, map[string]any{
		"banned_until": ptime.Format(past), "ban_reason": "已经失效的限时封禁",
	}, actors.Admin.Token)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, apierr.BizValidation, response.ErrorCode)
	require.Equal(t, "banned_until 必须晚于当前时间", response.Message)
	requireAdminFieldError(t, response, "banned_until", apierr.FieldOutOfRange)

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
	require.Equal(t, machine.ContentRevision, manual.ContentRevision)
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
	filterTags := []model.Tag{
		{Name: "筛选标签甲", Moderation: model.ModerationStatusReview},
		{Name: "筛选标签乙", Moderation: model.ModerationStatusReview},
	}
	require.NoError(t, gdb.Create(&filterTags).Error)
	filterRecords := []model.ModerationRecord{
		{
			TagID: &filterTags[0].ID, Scene: model.ModerationSceneText,
			Provider: "queue_filter_test", Verdict: model.ModerationVerdictReview,
			Labels: pq.StringArray{"abuse"},
		},
		{
			TagID: &filterTags[1].ID, Scene: model.ModerationSceneText,
			Provider: "queue_filter_test", Verdict: model.ModerationVerdictReview,
			Labels: pq.StringArray{"spam"},
		},
	}
	require.NoError(t, gdb.Create(&filterRecords).Error)
	status, response, _ := performJSON(t, engine, http.MethodGet,
		"/api/v2/admin/moderation-records/pending", nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	var queue service.AdminModerationList
	decodeData(t, response, &queue)
	require.True(t, moderationRecordPresent(queue.Records, machine.ID))
	require.True(t, moderationRecordPresent(queue.Records, filterRecords[0].ID))
	require.True(t, moderationRecordPresent(queue.Records, filterRecords[1].ID),
		"不传 label 时必须保持既有未筛选行为")
	status, response, _ = performJSON(t, engine, http.MethodGet,
		"/api/v2/admin/moderation-records/pending?label=abuse", nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	var filtered service.AdminModerationList
	decodeData(t, response, &filtered)
	require.True(t, moderationRecordPresent(filtered.Records, filterRecords[0].ID))
	require.False(t, moderationRecordPresent(filtered.Records, filterRecords[1].ID))
	require.False(t, moderationRecordPresent(filtered.Records, machine.ID),
		"审核标签筛选必须精确收窄结果")

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
	require.Equal(t, model.ModerationStatusBlock, deleted.Moderation)
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
	require.Equal(t, model.ModerationStatusPass, restored.Moderation)
	require.NoError(t, gdb.First(&postAfterDelete, post.ID).Error)
	require.EqualValues(t, 1, postAfterDelete.CommentCount, "恢复由可见性触发器回补计数")
	var audit model.ModerationRecord
	require.NoError(t, gdb.First(&audit, restoredResult.ModerationRecordID).Error)
	require.Equal(t, model.ModerationProvider("admin_restore"), audit.Provider)
	require.Equal(t, actors.Admin.User.ID, *audit.ReviewerID)
	require.NotNil(t, audit.ReviewedAt)
	var auditData struct {
		Action string `json:"action"`
	}
	require.NoError(t, json.Unmarshal(audit.RawResponse, &auditData))
	require.Equal(t, "restore", auditData.Action)
	var queriedByReviewer model.ModerationRecord
	require.NoError(t, gdb.Where(
		"id = ? AND reviewer_id = ?", restoredResult.ModerationRecordID, actors.Admin.User.ID,
	).First(&queriedByReviewer).Error)
	require.Equal(t, restoredResult.ModerationRecordID, queriedByReviewer.ID,
		"恢复流水必须能按操作人列查询")

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

func testEditedContentHidesStalePendingReviews(
	t *testing.T,
	engine *server.Hertz,
	reviewEngine *server.Hertz,
	gdb *gorm.DB,
	actors adminActors,
	fixture postFixture,
) {
	t.Helper()
	post := createPost(t, reviewEngine, actors.Author.Token,
		sharePostPayload(fixture, "编辑前待复核帖子", []string{"旧待复核"}))
	status, response, _ := performJSON(t, reviewEngine, http.MethodPut, postPath(post.ID),
		sharePostPayload(fixture, "编辑后待复核帖子", []string{"新待复核"}), actors.Author.Token)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
	var postReviews []model.ModerationRecord
	require.NoError(t, gdb.Where("post_id = ? AND verdict = ?", post.ID, model.ModerationVerdictReview).
		Order("id").Find(&postReviews).Error)
	require.Len(t, postReviews, 2, "帖子编辑后必须重新送审并追加一条审核记录")

	approvedPost := createPost(t, engine, actors.Author.Token,
		sharePostPayload(fixture, "评论待复核父帖", []string{"评论待复核"}))
	comment := createComment(t, reviewEngine, actors.Commenter.Token, approvedPost.ID,
		map[string]any{"content": "编辑前待复核评论"})
	status, response, _ = performJSON(t, reviewEngine, http.MethodPut, commentPath(comment.Comment.ID),
		map[string]any{"content": "编辑后待复核评论"}, actors.Commenter.Token)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
	var commentReviews []model.ModerationRecord
	require.NoError(t, gdb.Where("comment_id = ? AND verdict = ?",
		comment.Comment.ID, model.ModerationVerdictReview).Order("id").Find(&commentReviews).Error)
	require.Len(t, commentReviews, 2, "评论编辑后必须重新送审并追加一条审核记录")

	status, response, _ = performJSON(t, engine, http.MethodGet,
		"/api/v2/admin/moderation-records/pending?limit=100", nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	var queue service.AdminModerationList
	decodeData(t, response, &queue)
	require.False(t, moderationRecordPresent(queue.Records, postReviews[0].ID),
		"帖子编辑前的旧待复核记录不得继续展示")
	require.True(t, moderationRecordPresent(queue.Records, postReviews[1].ID),
		"帖子编辑后的最新待复核记录必须展示")
	require.False(t, moderationRecordPresent(queue.Records, commentReviews[0].ID),
		"评论编辑前的旧待复核记录不得继续展示")
	require.True(t, moderationRecordPresent(queue.Records, commentReviews[1].ID),
		"评论编辑后的最新待复核记录必须展示")
}

func testBlockQueuePriorityAndManualReview(
	t *testing.T,
	engine *server.Hertz,
	blockEngine *server.Hertz,
	gdb *gorm.DB,
	actors adminActors,
	fixture postFixture,
) {
	t.Helper()
	field := model.ModerationFieldName
	now := time.Now().UTC()
	priorityRecords := []model.ModerationRecord{
		{
			UserID: &actors.Ordinary.User.ID, Field: &field, Scene: model.ModerationSceneText,
			Provider: "queue_priority_test", Verdict: model.ModerationVerdictReview,
			Labels: pq.StringArray{"queue_priority"}, CreatedAt: now.Add(-time.Hour),
		},
		{
			UserID: &actors.Permanent.User.ID, Field: &field, Scene: model.ModerationSceneText,
			Provider: "queue_priority_test", Verdict: model.ModerationVerdictBlock,
			Labels: pq.StringArray{"queue_priority"}, CreatedAt: now,
		},
	}
	require.NoError(t, gdb.Create(&priorityRecords).Error)
	status, response, _ := performJSON(t, engine, http.MethodGet,
		"/api/v2/admin/moderation-records/pending?label=queue_priority&limit=100", nil,
		actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	var queue service.AdminModerationList
	decodeData(t, response, &queue)
	require.Len(t, queue.Records, 2)
	require.Equal(t, priorityRecords[1].ID, queue.Records[0].ID,
		"违规 block 必须排在更早创建的需复核 review 之前")
	require.Equal(t, priorityRecords[0].ID, queue.Records[1].ID)

	post := createPost(t, engine, actors.Author.Token,
		sharePostPayload(fixture, "机审违规评论父帖", []string{"违规复核"}))
	comment := createComment(t, blockEngine, actors.Commenter.Token, post.ID,
		map[string]any{"content": "机审违规也要人工复核"})
	var machine model.ModerationRecord
	require.NoError(t, gdb.Where("comment_id = ? AND verdict = ?",
		comment.Comment.ID, model.ModerationVerdictBlock).First(&machine).Error)
	status, response, _ = performJSON(t, engine, http.MethodPut,
		fmt.Sprintf("/api/v2/admin/moderation-records/%d/review", machine.ID),
		map[string]any{"verdict": model.ModerationVerdictPass}, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
	var manual model.ModerationRecord
	require.NoError(t, gdb.Where("supersedes_id = ?", machine.ID).First(&manual).Error)
	require.Equal(t, machine.ContentRevision, manual.ContentRevision)
	var restored model.Comment
	require.NoError(t, gdb.First(&restored, comment.Comment.ID).Error)
	require.Equal(t, model.ModerationStatusPass, restored.Moderation)
	require.Nil(t, restored.DeletedAt)
}

func testHistoryModerationVisibility(
	t *testing.T,
	cfg appconfig.Config,
	database *dbinfra.DB,
	sender service.VerificationEmailSender,
	passEngine *server.Hertz,
	actors adminActors,
	fixture postFixture,
) {
	t.Helper()
	postScore := decimal.RequireFromString("96.50")
	commentScore := decimal.RequireFromString("88.25")
	moderation := testutil.NewMockModeration()
	moderation.ProgramContent(
		testutil.ContentModerationRule{
			Target: service.ModerationTargetPost, Contains: "历史审核帖子第一版",
			Outcome: testutil.ContentVerdict(model.ModerationVerdictBlock, []string{"history_block"}, &postScore),
		},
		testutil.ContentModerationRule{
			Target: service.ModerationTargetPost, Contains: "历史审核帖子第二版",
			Outcome: testutil.ContentVerdict(model.ModerationVerdictReview, []string{"history_review"}, nil),
		},
		testutil.ContentModerationRule{
			Target: service.ModerationTargetComment, Contains: "历史审核评论第一版",
			Outcome: testutil.ContentVerdict(model.ModerationVerdictBlock, []string{"history_block"}, &commentScore),
		},
		testutil.ContentModerationRule{
			Target: service.ModerationTargetComment, Contains: "历史审核评论第二版",
			Outcome: testutil.ContentVerdict(model.ModerationVerdictReview, []string{"history_review"}, nil),
		},
	)
	historyEngine := adminTestEngine(cfg, database, sender, moderation)

	post := createPost(t, historyEngine, actors.Author.Token,
		sharePostPayload(fixture, "历史审核帖子第一版", []string{"版本审核"}))
	status, response, _ := performJSON(t, historyEngine, http.MethodPut, postPath(post.ID),
		sharePostPayload(fixture, "历史审核帖子第二版", []string{"版本审核"}), actors.Author.Token)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)

	path := postPath(post.ID) + "/history"
	status, response, raw := performJSON(t, historyEngine, http.MethodGet, path, nil, actors.Author.Token)
	require.Equal(t, http.StatusOK, status)
	var authorPostHistory service.PostHistoryList
	decodeData(t, response, &authorPostHistory)
	require.Len(t, authorPostHistory.Histories, 2)
	require.True(t, authorPostHistory.Histories[0].IsCurrent)
	require.EqualValues(t, 2, authorPostHistory.Histories[0].Revision)
	require.Equal(t, service.HistoryModerationMachineFailed,
		authorPostHistory.Histories[1].Moderation.Status)
	require.Nil(t, authorPostHistory.Histories[1].Moderation.Verdict)
	require.Nil(t, authorPostHistory.Histories[1].Moderation.Score)
	require.NotContains(t, string(raw.Result().Body()), "\"verdict\"")
	require.NotContains(t, string(raw.Result().Body()), "\"score\"")

	status, response, _ = performJSON(t, historyEngine, http.MethodGet, path, nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	var adminPostHistory service.PostHistoryList
	decodeData(t, response, &adminPostHistory)
	require.Equal(t, model.ModerationVerdictBlock, *adminPostHistory.Histories[1].Moderation.Verdict)
	require.True(t, postScore.Equal(*adminPostHistory.Histories[1].Moderation.Score))
	status, _, _ = performJSON(t, historyEngine, http.MethodGet, path, nil, actors.Super.Token)
	require.Equal(t, http.StatusOK, status)
	status, response, _ = performJSON(t, historyEngine, http.MethodGet, path, nil, actors.Commenter.Token)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, apierr.BizNotOwner, response.ErrorCode)

	parent := createPost(t, passEngine, actors.Author.Token,
		sharePostPayload(fixture, "历史审核评论父帖", []string{"评论版本"}))
	comment := createComment(t, historyEngine, actors.Commenter.Token, parent.ID,
		map[string]any{"content": "历史审核评论第一版"})
	status, response, _ = performJSON(t, historyEngine, http.MethodPut, commentPath(comment.Comment.ID),
		map[string]any{"content": "历史审核评论第二版"}, actors.Commenter.Token)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)

	commentHistoryPath := commentPath(comment.Comment.ID) + "/history"
	status, response, raw = performJSON(t, historyEngine, http.MethodGet, commentHistoryPath, nil,
		actors.Commenter.Token)
	require.Equal(t, http.StatusOK, status)
	var authorCommentHistory service.CommentHistoryList
	decodeData(t, response, &authorCommentHistory)
	require.Len(t, authorCommentHistory.Histories, 2)
	require.True(t, authorCommentHistory.Histories[0].IsCurrent)
	require.EqualValues(t, 2, authorCommentHistory.Histories[0].Revision)
	require.Equal(t, service.HistoryModerationMachineFailed,
		authorCommentHistory.Histories[1].Moderation.Status)
	require.Nil(t, authorCommentHistory.Histories[1].Moderation.Verdict)
	require.Nil(t, authorCommentHistory.Histories[1].Moderation.Score)
	require.NotContains(t, string(raw.Result().Body()), "\"verdict\"")
	require.NotContains(t, string(raw.Result().Body()), "\"score\"")

	status, response, _ = performJSON(t, historyEngine, http.MethodGet, commentHistoryPath, nil,
		actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	var adminCommentHistory service.CommentHistoryList
	decodeData(t, response, &adminCommentHistory)
	require.Equal(t, model.ModerationVerdictBlock,
		*adminCommentHistory.Histories[1].Moderation.Verdict)
	require.True(t, commentScore.Equal(*adminCommentHistory.Histories[1].Moderation.Score))
	status, _, _ = performJSON(t, historyEngine, http.MethodGet, commentHistoryPath, nil,
		actors.Author.Token)
	require.Equal(t, http.StatusForbidden, status)
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

func testAdminPostDeleteImageAccess(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	actors adminActors,
	fixture postFixture,
) {
	t.Helper()
	unique := createPostAsset(t, gdb, actors.Author.User.ID, "admin-delete-unique")
	sharedVisible := createPostAsset(t, gdb, actors.Author.User.ID, "admin-delete-visible")
	sharedDeleted := createPostAsset(t, gdb, actors.Author.User.ID, "admin-delete-deleted")

	targetPayload := sharePostPayload(fixture, "管理员下架图片混合场景", []string{"图片下架"})
	targetPayload["images"] = []string{unique.PublicURL, sharedVisible.PublicURL, sharedDeleted.PublicURL}
	target := createPost(t, engine, actors.Author.Token, targetPayload)

	visiblePayload := sharePostPayload(fixture, "共享图片仍公开", []string{"共享公开"})
	visiblePayload["images"] = []string{sharedVisible.PublicURL}
	visible := createPost(t, engine, actors.Author.Token, visiblePayload)

	deletedPayload := sharePostPayload(fixture, "共享图片引用已删除", []string{"共享已删"})
	deletedPayload["images"] = []string{sharedDeleted.PublicURL}
	deleted := createPost(t, engine, actors.Author.Token, deletedPayload)
	status, response, _ := performJSON(t, engine, http.MethodDelete,
		fmt.Sprintf("/api/v2/admin/posts/%d", deleted.ID), nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
	require.Zero(t, adminPostDeleteImageRecordCount(t, gdb, sharedDeleted.ID),
		"目标帖仍公开时，先删除另一篇共享帖不得收紧图片")

	status, response, _ = performJSON(t, engine, http.MethodDelete,
		fmt.Sprintf("/api/v2/admin/posts/%d", target.ID), nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
	var targetHistoryCount int64
	require.NoError(t, gdb.Model(&model.PostHistory{}).
		Where("post_id = ?", target.ID).Count(&targetHistoryCount).Error)
	require.EqualValues(t, 1, targetHistoryCount, "管理员下架也必须保存删除前当前版本")
	assertAdminPostDeleteImageAccess(t, gdb, unique.ID, target.ID, actors.Admin.User.ID)
	assertAdminPostDeleteImageAccess(t, gdb, sharedDeleted.ID, target.ID, actors.Admin.User.ID)

	var stillPublic model.ImageAsset
	require.NoError(t, gdb.First(&stillPublic, sharedVisible.ID).Error)
	require.Equal(t, model.ModerationStatusPass, stillPublic.Moderation)
	require.Zero(t, adminPostDeleteImageRecordCount(t, gdb, sharedVisible.ID),
		"仍被公开帖子引用的共享图片不得新增 block 流水")
	var sharedDeliveryCount int64
	require.NoError(t, gdb.Model(&model.ImageAccessDelivery{}).
		Where("image_asset_id = ?", sharedVisible.ID).Count(&sharedDeliveryCount).Error)
	require.Zero(t, sharedDeliveryCount, "仍公开的共享图片不得生成转私有交付")

	status, response, _ = performJSON(t, engine, http.MethodDelete,
		fmt.Sprintf("/api/v2/admin/posts/%d", target.ID), nil, actors.Admin.Token)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizPostDeleted, response.ErrorCode)
	require.EqualValues(t, 1, adminPostDeleteImageRecordCount(t, gdb, unique.ID))
	require.EqualValues(t, 1, adminPostDeleteImageRecordCount(t, gdb, sharedDeleted.ID))

	status, response, _ = performJSON(t, engine, http.MethodPut,
		fmt.Sprintf("/api/v2/admin/posts/%d/restore", target.ID), nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
	assertAdminPostRestoreImageAccess(t, gdb, unique.ID, actors.Admin.User.ID)
	assertAdminPostRestoreImageAccess(t, gdb, sharedDeleted.ID, actors.Admin.User.ID)

	status, response, _ = performJSON(t, engine, http.MethodDelete,
		fmt.Sprintf("/api/v2/admin/posts/%d", visible.ID), nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
	var sharedAfterVisibleDelete model.ImageAsset
	require.NoError(t, gdb.First(&sharedAfterVisibleDelete, sharedVisible.ID).Error)
	require.Equal(t, model.ModerationStatusPass, sharedAfterVisibleDelete.Moderation,
		"恢复后的目标帖仍引用图片时不得转私有")
	status, response, _ = performJSON(t, engine, http.MethodDelete,
		fmt.Sprintf("/api/v2/admin/posts/%d", target.ID), nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
	assertAdminPostDeleteImageAccess(t, gdb, sharedVisible.ID, target.ID, actors.Admin.User.ID)

	restoredAsset := createPostAsset(t, gdb, actors.Author.User.ID, "admin-delete-after-restore")
	restoredPayload := sharePostPayload(fixture, "机审恢复后管理员下架", []string{"恢复后下架"})
	restoredPayload["images"] = []string{restoredAsset.PublicURL}
	restored := createPost(t, engine, actors.Author.Token, restoredPayload)
	markPostModerationDeleted(t, gdb, restored.ID)
	status, response, _ = performJSON(t, engine, http.MethodPut,
		fmt.Sprintf("/api/v2/admin/posts/%d/restore", restored.ID), nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
	status, response, _ = performJSON(t, engine, http.MethodDelete,
		fmt.Sprintf("/api/v2/admin/posts/%d", restored.ID), nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
	assertAdminPostDeleteImageAccess(t, gdb, restoredAsset.ID, restored.ID, actors.Admin.User.ID)
	require.EqualValues(t, 1, adminPostDeleteImageRecordCount(t, gdb, restoredAsset.ID),
		"恢复后再删除只应产生本次下架的一条图片流水")
}

func testAdminPostDeleteKeepsImageReferencedByPendingPost(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	actors adminActors,
	fixture postFixture,
) {
	t.Helper()
	shared := createPostAsset(t, gdb, actors.Author.User.ID, "admin-delete-pending-reference")

	approvedPayload := sharePostPayload(fixture, "待审引用共享图的已发布帖", []string{"已发布引用"})
	approvedPayload["images"] = []string{shared.PublicURL}
	approved := createPost(t, engine, actors.Author.Token, approvedPayload)

	pendingPayload := sharePostPayload(fixture, "共享图仍有待审引用", []string{"待审引用"})
	pendingPayload["images"] = []string{shared.PublicURL}
	pending := createPost(t, engine, actors.Author.Token, pendingPayload)
	require.NoError(t, gdb.Model(&model.Post{}).Where("id = ?", pending.ID).
		UpdateColumn("status", model.PostStatusPending).Error)
	assertStoredPostStatus(t, gdb, approved.ID, model.PostStatusApproved)
	assertStoredPostStatus(t, gdb, pending.ID, model.PostStatusPending)

	status, response, _ := performJSON(t, engine, http.MethodDelete,
		fmt.Sprintf("/api/v2/admin/posts/%d", approved.ID), nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)

	var asset model.ImageAsset
	require.NoError(t, gdb.First(&asset, shared.ID).Error)
	require.Equal(t, model.ModerationStatusPass, asset.Moderation,
		"仍被待审核帖子引用的图片必须保持公开")
	require.Zero(t, adminPostDeleteImageRecordCount(t, gdb, shared.ID),
		"待审核引用仍有效时不得追加 block 流水")
	var intentCount int64
	require.NoError(t, gdb.Model(&model.ImageAccessIntent{}).
		Where("image_asset_id = ? AND desired_public = ?", shared.ID, false).
		Count(&intentCount).Error)
	require.Zero(t, intentCount, "待审核引用仍有效时不得生成转私有意图")
	var deliveryCount int64
	require.NoError(t, gdb.Model(&model.ImageAccessDelivery{}).
		Where("image_asset_id = ?", shared.ID).Count(&deliveryCount).Error)
	require.Zero(t, deliveryCount, "待审核引用仍有效时不得生成转私有交付")
}

func assertAdminPostDeleteImageAccess(
	t *testing.T,
	gdb *gorm.DB,
	imageAssetID uint64,
	postID uint64,
	reviewerID uint64,
) {
	t.Helper()
	var asset model.ImageAsset
	require.NoError(t, gdb.First(&asset, imageAssetID).Error)
	require.Equal(t, model.ModerationStatusBlock, asset.Moderation)

	var record model.ModerationRecord
	require.NoError(t, gdb.Where(
		"image_asset_id = ? AND provider = ?", imageAssetID, "admin_post_delete",
	).First(&record).Error)
	require.Equal(t, model.ModerationSceneImage, record.Scene)
	require.Equal(t, model.ModerationVerdictBlock, record.Verdict)
	require.Equal(t, reviewerID, *record.ReviewerID)
	require.NotNil(t, record.ReviewedAt)
	require.Nil(t, record.SupersedesID, "管理员主动下架不是对机器 review 的人工复核")
	var raw struct {
		Action string `json:"action"`
		PostID uint64 `json:"post_id"`
	}
	require.NoError(t, json.Unmarshal(record.RawResponse, &raw))
	require.Equal(t, "admin_delete_post", raw.Action)
	require.Equal(t, postID, raw.PostID)

	var intent model.ImageAccessIntent
	require.NoError(t, gdb.Where("source_moderation_record_id = ?", record.ID).First(&intent).Error)
	require.Equal(t, imageAssetID, intent.ImageAssetID)
	require.False(t, intent.DesiredPublic)
	var delivery model.ImageAccessDelivery
	require.NoError(t, gdb.First(&delivery, imageAssetID).Error)
	require.Equal(t, intent.ID, delivery.DesiredIntentID)
	require.False(t, delivery.DesiredPublic)
	require.True(t, delivery.PurgeRequired)
	require.Equal(t, model.ImageAccessPendingACL, delivery.State)
}

func adminPostDeleteImageRecordCount(t *testing.T, gdb *gorm.DB, imageAssetID uint64) int64 {
	t.Helper()
	var count int64
	require.NoError(t, gdb.Model(&model.ModerationRecord{}).Where(
		"image_asset_id = ? AND provider = ?", imageAssetID, "admin_post_delete",
	).Count(&count).Error)
	return count
}

func assertAdminPostRestoreImageAccess(
	t *testing.T,
	gdb *gorm.DB,
	imageAssetID uint64,
	reviewerID uint64,
) {
	t.Helper()
	var asset model.ImageAsset
	require.NoError(t, gdb.First(&asset, imageAssetID).Error)
	require.Equal(t, model.ModerationStatusPass, asset.Moderation)
	var record model.ModerationRecord
	require.NoError(t, gdb.Where(
		"image_asset_id = ? AND provider = ?", imageAssetID, "admin_restore",
	).Order("created_at DESC, id DESC").First(&record).Error)
	require.Equal(t, model.ModerationSceneImage, record.Scene)
	require.Equal(t, model.ModerationVerdictPass, record.Verdict)
	require.Equal(t, reviewerID, *record.ReviewerID)
	var intent model.ImageAccessIntent
	require.NoError(t, gdb.Where("source_moderation_record_id = ?", record.ID).First(&intent).Error)
	require.True(t, intent.DesiredPublic)
	var delivery model.ImageAccessDelivery
	require.NoError(t, gdb.First(&delivery, imageAssetID).Error)
	require.True(t, delivery.DesiredPublic)
	require.True(t, delivery.PurgeRequired, "previous=block 转公开必须刷新 CDN")
}

func testAuthorAdminDeleteUsesAuthorIdentity(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	actors adminActors,
	fixture postFixture,
) {
	t.Helper()
	post := createPost(t, engine, actors.Multi.Token,
		sharePostPayload(fixture, "作者兼管理员自删", []string{"最低身份"}))
	status, response, _ := performJSON(t, engine, http.MethodDelete,
		fmt.Sprintf("/api/v2/admin/posts/%d", post.ID), nil, actors.Multi.Token)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
	var stored model.Post
	require.NoError(t, gdb.First(&stored, post.ID).Error)
	require.Equal(t, model.DeleteReasonAuthor, *stored.DeletedReason)
	require.Equal(t, actors.Multi.User.ID, *stored.DeletedBy)
	var historyCount int64
	require.NoError(t, gdb.Model(&model.PostHistory{}).
		Where("post_id = ?", post.ID).Count(&historyCount).Error)
	require.EqualValues(t, 1, historyCount)
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
	require.Equal(t, actors.Admin.User.ID, *audit.ReviewerID)
	require.NotNil(t, audit.ReviewedAt)
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
		sharePostPayload(fixture, "管理员主动下架可恢复", []string{"主动下架恢复"}))
	status, response, _ = performJSON(t, engine, http.MethodDelete,
		fmt.Sprintf("/api/v2/admin/posts/%d", nonModeration.ID), nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status)
	status, response, _ = performJSON(t, engine, http.MethodPut,
		fmt.Sprintf("/api/v2/admin/posts/%d/restore", nonModeration.ID), nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)

	authorDeleted := createPost(t, engine, actors.Author.Token,
		sharePostPayload(fixture, "作者自删可由管理员恢复", []string{"作者删除恢复"}))
	status, response, _ = performJSON(t, engine, http.MethodDelete,
		postPath(authorDeleted.ID), nil, actors.Author.Token)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
	status, response, _ = performJSON(t, engine, http.MethodPut,
		fmt.Sprintf("/api/v2/admin/posts/%d/restore", authorDeleted.ID), nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)

	safeAsset := createPostAsset(t, gdb, actors.Author.User.ID, "admin-safe-restore-rollback")
	asset := createPostAsset(t, gdb, actors.Author.User.ID, "admin-unsafe-restore")
	payload := sharePostPayload(fixture, "违规图片不可恢复", []string{"图片恢复"})
	payload["images"] = []string{safeAsset.PublicURL, asset.PublicURL}
	unsafePost := createPost(t, engine, actors.Author.Token, payload)
	realBlock := model.ModerationRecord{
		ImageAssetID: &asset.ID, Scene: model.ModerationSceneImage,
		Provider: "real_content_violation", Verdict: model.ModerationVerdictBlock,
		Labels: pq.StringArray{"violation"}, CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, gdb.Create(&realBlock).Error)
	require.NoError(t, gdb.Model(&model.ImageAsset{}).Where("id = ?", asset.ID).
		UpdateColumn("moderation", model.ModerationStatusBlock).Error)
	status, response, _ = performJSON(t, engine, http.MethodDelete,
		fmt.Sprintf("/api/v2/admin/posts/%d", unsafePost.ID), nil, actors.Admin.Token)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
	status, response, _ = performJSON(t, engine, http.MethodPut,
		fmt.Sprintf("/api/v2/admin/posts/%d/restore", unsafePost.ID), nil, actors.Admin.Token)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, "image_not_approved", string(response.ErrorCode))
	var unsafeStored model.Post
	require.NoError(t, gdb.First(&unsafeStored, unsafePost.ID).Error)
	require.NotNil(t, unsafeStored.DeletedAt)
	var unsafeAsset model.ImageAsset
	require.NoError(t, gdb.First(&unsafeAsset, asset.ID).Error)
	require.Equal(t, model.ModerationStatusBlock, unsafeAsset.Moderation)
	var rolledBackSafeAsset model.ImageAsset
	require.NoError(t, gdb.First(&rolledBackSafeAsset, safeAsset.ID).Error)
	require.Equal(t, model.ModerationStatusBlock, rolledBackSafeAsset.Moderation,
		"应先尝试逆转删除封禁，但后续守卫失败必须让整个 UoW 回滚")
	var unsafeRestoreCount int64
	require.NoError(t, gdb.Model(&model.ModerationRecord{}).Where(
		"image_asset_id IN ? AND provider = ?", []uint64{safeAsset.ID, asset.ID}, "admin_restore",
	).Count(&unsafeRestoreCount).Error)
	require.Zero(t, unsafeRestoreCount, "真实违规守卫失败必须回滚此前追加的图片恢复流水")
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
