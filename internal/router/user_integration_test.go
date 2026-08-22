package router_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	dbinfra "github.com/jingyijun/danshi_backend_go/internal/infra/db"
	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

type captureUserModerationAlerter struct {
	mu     sync.Mutex
	alerts []service.UserModerationAlert
}

func (a *captureUserModerationAlerter) AlertUserContent(
	_ context.Context,
	alert service.UserModerationAlert,
) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.alerts = append(a.alerts, alert)
}

func (a *captureUserModerationAlerter) all() []service.UserModerationAlert {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]service.UserModerationAlert(nil), a.alerts...)
}

func TestUserDomainAgainstPostgres(t *testing.T) {
	gdb, database := openAuthPostgres(t)
	cfg := authTestConfig()
	sender := newCaptureEmailSender()
	engine := authTestEngine(cfg, database, sender)
	owner := registerPostTestUser(t, engine, sender, "user-owner@fdueat.com", "资料主人")
	viewer := registerPostTestUser(t, engine, sender, "user-viewer@fdueat.com", "资料访客")
	fixture := loadPostFixture(t, gdb)

	t.Run("user route inventory", func(t *testing.T) {
		testUserRouteInventory(t, engine)
	})

	t.Run("profile privacy and follow actions", func(t *testing.T) {
		testUserProfileAndFollows(t, engine, gdb, owner, viewer)
	})

	t.Run("partial update avatar lifecycle and moderation", func(t *testing.T) {
		testUserProfileUpdate(t, engine, gdb, owner, viewer)
	})

	t.Run("review and block keep user fields and record evidence", func(t *testing.T) {
		testUserModerationSemantics(t, gdb, database, owner)
	})

	t.Run("posts and favorites visibility", func(t *testing.T) {
		testUserPostLists(t, engine, gdb, owner, viewer, fixture)
	})
}

func testUserRouteInventory(t *testing.T, engine *server.Hertz) {
	t.Helper()
	operations := make([]string, 0)
	for _, route := range engine.Routes() {
		if strings.HasPrefix(route.Path, "/api/v2/users") {
			operations = append(operations, route.Method+" "+route.Path)
		}
	}
	require.ElementsMatch(t, []string{
		"GET /api/v2/users/:user_id",
		"PUT /api/v2/users/:user_id",
		"GET /api/v2/users/:user_id/posts",
		"GET /api/v2/users/:user_id/favorites",
		"POST /api/v2/users/:user_id/follow",
		"DELETE /api/v2/users/:user_id/follow",
		"GET /api/v2/users/:user_id/following",
		"GET /api/v2/users/:user_id/followers",
	}, operations)
}

func testUserProfileAndFollows(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	owner service.AuthResult,
	viewer service.AuthResult,
) {
	t.Helper()
	path := userPath(owner.User.ID)
	status, response, _ := performJSON(t, engine, http.MethodGet, path, nil, viewer.Token)
	require.Equal(t, http.StatusOK, status)
	var public map[string]any
	decodeData(t, response, &public)
	require.NotContains(t, public, "email")
	require.NotContains(t, public, "role")
	require.NotContains(t, public, "hometown")

	status, response, _ = performJSON(t, engine, http.MethodGet, path, nil, owner.Token)
	require.Equal(t, http.StatusOK, status)
	var own map[string]any
	decodeData(t, response, &own)
	require.Equal(t, owner.User.Email, own["email"])
	require.Equal(t, string(owner.User.Role), own["role"])
	require.NotContains(t, own, "hometown")

	followPath := path + "/follow"
	for range 2 {
		status, response, _ = performJSON(t, engine, http.MethodPost, followPath, nil, viewer.Token)
		require.Equal(t, http.StatusOK, status)
		var result service.FollowActionResult
		decodeData(t, response, &result)
		require.True(t, result.IsFollowing)
		require.EqualValues(t, 1, result.FollowerCount)
	}
	var followCount, notificationCount int64
	require.NoError(t, gdb.Model(&model.Follow{}).Where(
		"follower_id = ? AND following_id = ?", viewer.User.ID, owner.User.ID,
	).Count(&followCount).Error)
	require.EqualValues(t, 1, followCount)
	require.NoError(t, gdb.Model(&model.Notification{}).Where(
		"sender_id = ? AND recipient_id = ? AND type = ?",
		viewer.User.ID, owner.User.ID, model.NotificationTypeFollow,
	).Count(&notificationCount).Error)
	require.EqualValues(t, 1, notificationCount, "重复关注不得产生重复通知")

	status, response, _ = performJSON(t, engine, http.MethodGet,
		userPath(viewer.User.ID)+"/following", nil, viewer.Token)
	require.Equal(t, http.StatusOK, status)
	var following service.UserFollowList
	decodeData(t, response, &following)
	require.Len(t, following.Users, 1)
	require.Equal(t, owner.User.ID, following.Users[0].ID)
	require.True(t, following.Users[0].IsFollowing)

	status, response, _ = performJSON(t, engine, http.MethodGet,
		userPath(owner.User.ID)+"/followers", nil, owner.Token)
	require.Equal(t, http.StatusOK, status)
	var followers service.UserFollowList
	decodeData(t, response, &followers)
	require.Len(t, followers.Users, 1)
	require.Equal(t, viewer.User.ID, followers.Users[0].ID)

	for range 2 {
		status, response, _ = performJSON(t, engine, http.MethodDelete, followPath, nil, viewer.Token)
		require.Equal(t, http.StatusOK, status)
		var result service.FollowActionResult
		decodeData(t, response, &result)
		require.False(t, result.IsFollowing)
		require.Zero(t, result.FollowerCount)
	}
	status, response, _ = performJSON(t, engine, http.MethodPost, followPath, nil, viewer.Token)
	require.Equal(t, http.StatusOK, status)
	var followedAgain service.FollowActionResult
	decodeData(t, response, &followedAgain)
	require.True(t, followedAgain.IsFollowing, "取消后必须可以再次关注")

	status, response, _ = performJSON(t, engine, http.MethodPost,
		userPath(viewer.User.ID)+"/follow", nil, viewer.Token)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "cannot_follow_self", string(response.ErrorCode))
}

func testUserProfileUpdate(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	owner service.AuthResult,
	viewer service.AuthResult,
) {
	t.Helper()
	oldAvatar := createAvatarAsset(t, gdb, owner.User.ID, "user-old-avatar")
	newAvatar := createAvatarAsset(t, gdb, owner.User.ID, "user-new-avatar")
	require.NoError(t, gdb.Model(&model.User{}).Where("id = ?", owner.User.ID).
		Update("avatar_image_asset_id", oldAvatar.ID).Error)

	status, response, _ := performJSON(t, engine, http.MethodPut, userPath(owner.User.ID), map[string]any{
		"name": "更新昵称", "bio": "更新简介", "gender": model.GenderOther,
		"avatar_url": newAvatar.PublicURL, "hometown": "已删除字段",
	}, owner.Token)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
	var result service.UserUpdateResult
	decodeData(t, response, &result)
	require.Equal(t, "更新昵称", result.User.Name)
	require.Equal(t, "更新简介", *result.User.Bio)
	require.Equal(t, newAvatar.PublicURL, *result.User.AvatarURL)
	require.Equal(t, owner.User.Email, *result.User.Email)

	var stored model.User
	require.NoError(t, gdb.First(&stored, owner.User.ID).Error)
	require.Equal(t, newAvatar.ID, *stored.AvatarImageAssetID)
	require.NoError(t, gdb.First(&oldAvatar, oldAvatar.ID).Error)
	require.NoError(t, gdb.First(&newAvatar, newAvatar.ID).Error)
	require.Equal(t, model.ImageStatusRetired, oldAvatar.Status, "换绑后旧头像资产必须退役")
	require.Equal(t, model.ImageStatusReady, newAvatar.Status, "新头像资产必须保持 ready")

	var fields []model.ModerationField
	require.NoError(t, gdb.Model(&model.ModerationRecord{}).
		Where("user_id = ?", owner.User.ID).Order("id").Pluck("field", &fields).Error)
	require.ElementsMatch(t, []model.ModerationField{
		model.ModerationFieldName, model.ModerationFieldBio,
	}, fields)

	status, response, _ = performJSON(t, engine, http.MethodPut, userPath(owner.User.ID), map[string]any{
		"bio": nil,
	}, owner.Token)
	require.Equal(t, http.StatusOK, status)
	decodeData(t, response, &result)
	require.Nil(t, result.User.Bio)
	require.Equal(t, "更新昵称", result.User.Name, "未提交字段必须保持原值")
	require.Equal(t, newAvatar.PublicURL, *result.User.AvatarURL)

	status, response, _ = performJSON(t, engine, http.MethodPut, userPath(owner.User.ID), map[string]any{
		"name": "越权更新",
	}, viewer.Token)
	require.Equal(t, http.StatusForbidden, status)
}

func testUserModerationSemantics(
	t *testing.T,
	gdb *gorm.DB,
	database *dbinfra.DB,
	owner service.AuthResult,
) {
	t.Helper()
	alerter := &captureUserModerationAlerter{}
	reviewService := service.NewUserService(
		fixedVerdictModerator(model.ModerationVerdictReview), alerter,
	)
	reviewName := "需人工复核昵称"
	err := database.RunInTx(context.Background(), func(ctx context.Context) error {
		_, updateErr := reviewService.Update(ctx, owner.User.ID, owner.User.ID, service.UpdateUserInput{
			Name: &reviewName, NameSet: true,
		})
		return updateErr
	})
	require.NoError(t, err)
	var stored model.User
	require.NoError(t, gdb.First(&stored, owner.User.ID).Error)
	require.Equal(t, reviewName, stored.Name, "review 必须保持新昵称")
	var reviewCount int64
	require.NoError(t, gdb.Model(&model.ModerationRecord{}).Where(
		"user_id = ? AND field = ? AND verdict = ?",
		owner.User.ID, model.ModerationFieldName, model.ModerationVerdictReview,
	).Count(&reviewCount).Error)
	require.EqualValues(t, 1, reviewCount)
	require.Empty(t, alerter.all())

	blockService := service.NewUserService(
		fixedVerdictModerator(model.ModerationVerdictBlock), alerter,
	)
	blockBio := "机审违规但等待管理员处置"
	err = database.RunInTx(context.Background(), func(ctx context.Context) error {
		_, updateErr := blockService.Update(ctx, owner.User.ID, owner.User.ID, service.UpdateUserInput{
			Bio: &blockBio, BioSet: true,
		})
		return updateErr
	})
	require.NoError(t, err)
	require.NoError(t, gdb.First(&stored, owner.User.ID).Error)
	require.Equal(t, blockBio, *stored.Bio, "block 不得自行重置简介或封禁用户")
	var blockCount int64
	require.NoError(t, gdb.Model(&model.ModerationRecord{}).Where(
		"user_id = ? AND field = ? AND verdict = ?",
		owner.User.ID, model.ModerationFieldBio, model.ModerationVerdictBlock,
	).Count(&blockCount).Error)
	require.EqualValues(t, 1, blockCount)
	require.Equal(t, []service.UserModerationAlert{{
		UserID: owner.User.ID, Field: model.ModerationFieldBio,
		Verdict: model.ModerationVerdictBlock, Labels: []string{},
	}}, alerter.all())
}

func testUserPostLists(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	owner service.AuthResult,
	viewer service.AuthResult,
	fixture postFixture,
) {
	t.Helper()
	approved := createPost(t, engine, owner.Token,
		sharePostPayload(fixture, "用户公开帖子", []string{"用户公开"}))
	draftPayload := sharePostPayload(fixture, "用户草稿", []string{"用户草稿"})
	draftPayload["status"] = model.PostStatusDraft
	draft := createPost(t, engine, owner.Token, draftPayload)

	status, response, _ := performJSON(t, engine, http.MethodGet,
		userPath(owner.User.ID)+"/posts", nil, owner.Token)
	require.Equal(t, http.StatusOK, status)
	var own service.PostList
	decodeData(t, response, &own)
	require.ElementsMatch(t, []uint64{approved.ID, draft.ID}, postIDs(own.Posts))

	status, response, _ = performJSON(t, engine, http.MethodGet,
		userPath(owner.User.ID)+"/posts", nil, viewer.Token)
	require.Equal(t, http.StatusOK, status)
	var public service.PostList
	decodeData(t, response, &public)
	require.Equal(t, []uint64{approved.ID}, postIDs(public.Posts))

	status, _, _ = performJSON(t, engine, http.MethodGet,
		userPath(owner.User.ID)+"/posts?status=draft", nil, viewer.Token)
	require.Equal(t, http.StatusForbidden, status)

	status, _, _ = performJSON(t, engine, http.MethodPost,
		postPath(approved.ID)+"/favorite", nil, viewer.Token)
	require.Equal(t, http.StatusOK, status)
	status, response, _ = performJSON(t, engine, http.MethodGet,
		userPath(viewer.User.ID)+"/favorites", nil, viewer.Token)
	require.Equal(t, http.StatusOK, status)
	var favorites service.PostList
	decodeData(t, response, &favorites)
	require.Equal(t, []uint64{approved.ID}, postIDs(favorites.Posts))
	require.True(t, favorites.Posts[0].IsFavorited)

	status, _, _ = performJSON(t, engine, http.MethodGet,
		userPath(viewer.User.ID)+"/favorites", nil, owner.Token)
	require.Equal(t, http.StatusForbidden, status)

	var favoriteRows int64
	require.NoError(t, gdb.Model(&model.Favorite{}).Where(
		"user_id = ? AND post_id = ?", viewer.User.ID, approved.ID,
	).Count(&favoriteRows).Error)
	require.EqualValues(t, 1, favoriteRows)
}

func createAvatarAsset(t *testing.T, gdb *gorm.DB, userID uint64, suffix string) model.ImageAsset {
	t.Helper()
	size := int64(1024)
	asset := model.ImageAsset{
		UploaderID: &userID, Purpose: model.ImagePurposeAvatar,
		ObjectKey: "avatar-test/" + suffix, PublicURL: "https://img.example.test/" + suffix + ".jpg",
		ContentType: "image/jpeg", Size: &size, Status: model.ImageStatusReady,
		Moderation: model.ModerationStatusPass,
	}
	require.NoError(t, gdb.Create(&asset).Error)
	return asset
}

func userPath(userID uint64) string { return fmt.Sprintf("/api/v2/users/%d", userID) }

func postIDs(posts []service.PostListItem) []uint64 {
	ids := make([]uint64, 0, len(posts))
	for _, post := range posts {
		ids = append(ids, post.ID)
	}
	return ids
}
