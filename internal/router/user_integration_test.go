package router_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	appconfig "github.com/jingyijun/danshi_backend_go/internal/config"
	dbinfra "github.com/jingyijun/danshi_backend_go/internal/infra/db"
	"github.com/jingyijun/danshi_backend_go/internal/model"
	appRouter "github.com/jingyijun/danshi_backend_go/internal/router"
	"github.com/jingyijun/danshi_backend_go/internal/service"
	"github.com/jingyijun/danshi_backend_go/internal/testutil"
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
	fixtures := testutil.NewFixtures(t, gdb)

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

	t.Run("profile unicode boundaries and moderation rollback", func(t *testing.T) {
		testUserProfileBoundaries(t, cfg, database, sender, gdb, fixtures)
	})

	t.Run("avatar ownership states and concurrent rebinding", func(t *testing.T) {
		testUserAvatarSafety(t, cfg, database, sender, gdb, fixtures)
	})

	t.Run("concurrent follow and deleted target semantics", func(t *testing.T) {
		testUserFollowConcurrencyAndDeletion(t, cfg, engine, gdb, fixtures)
	})

	t.Run("favorites pagination boundaries", func(t *testing.T) {
		testUserFavoritesPagination(t, cfg, engine, gdb, fixtures)
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
	require.NotContains(t, public, "roles")
	require.NotContains(t, public, "hometown")

	status, response, _ = performJSON(t, engine, http.MethodGet, path, nil, owner.Token)
	require.Equal(t, http.StatusOK, status)
	var own map[string]any
	decodeData(t, response, &own)
	require.Equal(t, owner.User.Email, own["email"])
	require.Equal(t, []any{}, own["roles"])
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

func testUserProfileBoundaries(
	t *testing.T,
	cfg appconfig.Config,
	database *dbinfra.DB,
	sender *captureEmailSender,
	gdb *gorm.DB,
	fixtures *testutil.Fixtures,
) {
	t.Helper()
	actor := fixtures.CreateActor(cfg)
	moderation := testutil.NewMockModeration()
	engine := newUserTestEngine(t, cfg, database, sender, moderation)

	maxName := strings.Repeat("界", 100)
	maxBio := strings.Repeat("文", 500)
	status, response, _ := performJSON(t, engine, http.MethodPut, userPath(actor.User.ID), map[string]any{
		"name": maxName, "bio": maxBio, "gender": model.GenderOther,
	}, actor.Token)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
	var updated service.UserUpdateResult
	decodeData(t, response, &updated)
	require.Equal(t, maxName, updated.User.Name)
	require.Equal(t, maxBio, *updated.User.Bio)
	require.Equal(t, model.GenderOther, *updated.User.Gender)
	moderation.RequireContentCalls(t, 2)
	calls := moderation.ContentCalls()
	require.Equal(t, service.ModerationTargetUser, calls[0].Target)
	require.NotNil(t, calls[0].Field)
	require.Equal(t, model.ModerationFieldName, *calls[0].Field)
	require.Equal(t, maxName, calls[0].Text)
	require.NotNil(t, calls[1].Field)
	require.Equal(t, model.ModerationFieldBio, *calls[1].Field)
	require.Equal(t, maxBio, calls[1].Text)
	var moderationRows int64
	require.NoError(t, gdb.Model(&model.ModerationRecord{}).Where("user_id = ?", actor.User.ID).
		Count(&moderationRows).Error)
	require.EqualValues(t, 2, moderationRows)

	status, response, _ = performJSON(t, engine, http.MethodPut, userPath(actor.User.ID), map[string]any{
		"bio": strings.Repeat("文", 501),
	}, actor.Token)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	requireAuthFieldError(t, response, "bio", apierr.FieldTooLong)
	moderation.RequireContentCalls(t, 2)

	status, response, _ = performJSON(t, engine, http.MethodPut, userPath(actor.User.ID), map[string]any{
		"name": "   ",
	}, actor.Token)
	require.Equal(t, http.StatusOK, status)
	decodeData(t, response, &updated)
	require.Empty(t, updated.User.Name, "昵称空白输入按现有契约归一为空字符串")
	moderation.RequireContentCalls(t, 2)

	status, response, _ = performJSON(t, engine, http.MethodPut, userPath(actor.User.ID), map[string]any{
		"gender": "unknown",
	}, actor.Token)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	requireAuthFieldError(t, response, "gender", apierr.FieldInvalidEnum)

	failureActor := fixtures.CreateActor(cfg)
	failureModeration := testutil.NewMockModeration()
	failureModeration.ProgramContent(testutil.ContentModerationRule{
		Target: service.ModerationTargetUser, Contains: "第二字段失败",
		Outcome: testutil.ContentHTTPFailure(http.StatusServiceUnavailable),
	})
	failureEngine := newUserTestEngine(t, cfg, database, sender, failureModeration)
	status, response, _ = performJSON(t, failureEngine, http.MethodPut,
		userPath(failureActor.User.ID), map[string]any{
			"name": "第一字段已通过", "bio": "第二字段失败",
		}, failureActor.Token)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, apierr.BizServiceUnavailable, response.ErrorCode)
	failureModeration.RequireContentCalls(t, 2)
	var stored model.User
	require.NoError(t, gdb.First(&stored, failureActor.User.ID).Error)
	require.Equal(t, failureActor.User.Name, stored.Name,
		"第二个审核调用失败时，用户字段更新必须整体回滚")
	require.Nil(t, stored.Bio)
	require.NoError(t, gdb.Model(&model.ModerationRecord{}).
		Where("user_id = ?", failureActor.User.ID).Count(&moderationRows).Error)
	require.Zero(t, moderationRows, "第一字段已经写入的审核流水也必须随事务回滚")
}

func testUserAvatarSafety(
	t *testing.T,
	cfg appconfig.Config,
	database *dbinfra.DB,
	sender *captureEmailSender,
	gdb *gorm.DB,
	fixtures *testutil.Fixtures,
) {
	t.Helper()
	owner := fixtures.CreateActor(cfg)
	other := fixtures.CreateActor(cfg)
	avatarImage := func(image *model.ImageAsset) { image.Purpose = model.ImagePurposeAvatar }
	initial := fixtures.CreateImage(owner.User.ID, avatarImage)
	foreign := fixtures.CreateImage(other.User.ID, avatarImage)
	blocked := fixtures.CreateImage(owner.User.ID, avatarImage, func(image *model.ImageAsset) {
		image.Moderation = model.ModerationStatusBlock
	})
	retired := fixtures.CreateImage(owner.User.ID, avatarImage, func(image *model.ImageAsset) {
		image.Status = model.ImageStatusRetired
	})
	require.NoError(t, gdb.Model(&model.User{}).Where("id = ?", owner.User.ID).
		Update("avatar_image_asset_id", initial.ID).Error)
	engine := authTestEngine(cfg, database, sender)

	cases := []struct {
		name      string
		asset     model.ImageAsset
		status    int
		errorCode apierr.BizCode
	}{
		{name: "foreign avatar", asset: foreign, status: http.StatusForbidden, errorCode: apierr.BizImageNotOwned},
		{name: "blocked avatar", asset: blocked, status: http.StatusConflict, errorCode: apierr.BizImageNotApproved},
		{name: "retired avatar", asset: retired, status: http.StatusConflict, errorCode: apierr.BizImageNotApproved},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, response, _ := performJSON(t, engine, http.MethodPut, userPath(owner.User.ID),
				map[string]any{"avatar_url": testCase.asset.PublicURL}, owner.Token)
			require.Equal(t, testCase.status, status)
			require.Equal(t, testCase.errorCode, response.ErrorCode)
			var stored model.User
			require.NoError(t, gdb.First(&stored, owner.User.ID).Error)
			require.Equal(t, initial.ID, *stored.AvatarImageAssetID,
				"失败换绑不得修改当前头像")
		})
	}

	first := fixtures.CreateImage(owner.User.ID, avatarImage)
	second := fixtures.CreateImage(owner.User.ID, avatarImage)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	moderation := testutil.NewMockModeration()
	outcome := testutil.ContentVerdict(model.ModerationVerdictPass, nil, nil)
	outcome.Release = release
	moderation.SetDefaultContent(outcome)
	concurrentEngine := newUserTestEngine(t, cfg, database, sender, moderation)
	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	results := make(chan asyncRequestResult, 2)
	assets := []model.ImageAsset{first, second}
	for index, asset := range assets {
		go func() {
			ready <- struct{}{}
			<-start
			status, response, raw, err := performJSONRequest(
				concurrentEngine, http.MethodPut, userPath(owner.User.ID), map[string]any{
					"name": fmt.Sprintf("并发头像昵称 %d", index), "avatar_url": asset.PublicURL,
				}, owner.Token,
			)
			results <- asyncRequestResult{status: status, response: response, raw: raw, err: err}
		}()
	}
	<-ready
	<-ready
	close(start)
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.True(t, moderation.WaitForContentCalls(waitCtx, 1),
		"首个头像事务未到达可控审核阻塞点")
	unblock()
	for range 2 {
		select {
		case result := <-results:
			require.NoError(t, result.err)
			require.Equal(t, http.StatusOK, result.status,
				"error_code=%s message=%s", result.response.ErrorCode, result.response.Message)
		case <-waitCtx.Done():
			require.FailNow(t, "并发头像换绑未在期限内完成")
		}
	}
	moderation.RequireContentCalls(t, 2)

	var stored model.User
	require.NoError(t, gdb.First(&stored, owner.User.ID).Error)
	require.NotNil(t, stored.AvatarImageAssetID)
	require.Contains(t, []uint64{first.ID, second.ID}, *stored.AvatarImageAssetID)
	for _, asset := range []model.ImageAsset{initial, first, second} {
		var current model.ImageAsset
		require.NoError(t, gdb.First(&current, asset.ID).Error)
		if current.ID == *stored.AvatarImageAssetID {
			require.Equal(t, model.ImageStatusReady, current.Status,
				"最终头像资产必须保持 ready")
		} else {
			require.Equal(t, model.ImageStatusRetired, current.Status,
				"并发换绑后所有失去引用的旧头像都必须退役")
		}
	}
}

func testUserFollowConcurrencyAndDeletion(
	t *testing.T,
	cfg appconfig.Config,
	engine *server.Hertz,
	gdb *gorm.DB,
	fixtures *testutil.Fixtures,
) {
	t.Helper()
	follower := fixtures.CreateActor(cfg)
	target := fixtures.CreateActor(cfg)
	path := userPath(target.User.ID) + "/follow"
	const requests = 8
	start := make(chan struct{})
	ready := make(chan struct{}, requests)
	results := make(chan asyncRequestResult, requests)
	for range requests {
		go func() {
			ready <- struct{}{}
			<-start
			status, response, raw, err := performJSONRequest(
				engine, http.MethodPost, path, nil, follower.Token,
			)
			results <- asyncRequestResult{status: status, response: response, raw: raw, err: err}
		}()
	}
	for range requests {
		<-ready
	}
	close(start)
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for range requests {
		select {
		case result := <-results:
			require.NoError(t, result.err)
			require.Equal(t, http.StatusOK, result.status)
			var action service.FollowActionResult
			decodeData(t, result.response, &action)
			require.True(t, action.IsFollowing)
			require.EqualValues(t, 1, action.FollowerCount)
		case <-waitCtx.Done():
			require.FailNow(t, "并发关注未在期限内完成")
		}
	}
	var followRows, notificationRows int64
	require.NoError(t, gdb.Model(&model.Follow{}).Where(
		"follower_id = ? AND following_id = ?", follower.User.ID, target.User.ID,
	).Count(&followRows).Error)
	require.EqualValues(t, 1, followRows)
	require.NoError(t, gdb.Model(&model.Notification{}).Where(
		"sender_id = ? AND recipient_id = ? AND type = ?",
		follower.User.ID, target.User.ID, model.NotificationTypeFollow,
	).Count(&notificationRows).Error)
	require.EqualValues(t, 1, notificationRows, "并发重复关注只能产生一条通知")

	status, response, _ := performJSON(t, engine, http.MethodGet,
		userPath(target.User.ID), nil, follower.Token)
	require.Equal(t, http.StatusOK, status)
	var targetProfile service.UserProfile
	decodeData(t, response, &targetProfile)
	require.EqualValues(t, 1, targetProfile.Stats.FollowerCount)
	status, response, _ = performJSON(t, engine, http.MethodGet,
		userPath(follower.User.ID)+"/following", nil, follower.Token)
	require.Equal(t, http.StatusOK, status)
	var following service.UserFollowList
	decodeData(t, response, &following)
	require.Len(t, following.Users, 1)
	require.EqualValues(t, 1, following.Pagination.Total,
		"活跃用户的关注统计与列表总数必须一致")

	historicalFollower := fixtures.CreateActor(cfg)
	deletedTarget := fixtures.CreateActor(cfg)
	lateFollower := fixtures.CreateActor(cfg)
	status, _, _ = performJSON(t, engine, http.MethodPost,
		userPath(deletedTarget.User.ID)+"/follow", nil, historicalFollower.Token)
	require.Equal(t, http.StatusOK, status)
	deletedAt := time.Now().UTC()
	require.NoError(t, gdb.Model(&model.User{}).Where("id = ?", deletedTarget.User.ID).
		Update("deleted_at", deletedAt).Error)
	status, response, _ = performJSON(t, engine, http.MethodGet,
		userPath(historicalFollower.User.ID)+"/following", nil, historicalFollower.Token)
	require.Equal(t, http.StatusOK, status)
	decodeData(t, response, &following)
	require.Empty(t, following.Users)
	require.Zero(t, following.Pagination.Total, "关注列表必须隐藏已注销目标")
	status, response, _ = performJSON(t, engine, http.MethodGet,
		userPath(historicalFollower.User.ID), nil, historicalFollower.Token)
	require.Equal(t, http.StatusOK, status)
	var historicalProfile service.UserProfile
	decodeData(t, response, &historicalProfile)
	require.EqualValues(t, 1, historicalProfile.Stats.FollowingCount,
		"软注销保留关注审计行，因此资料统计保留历史关注数")
	require.NoError(t, gdb.Model(&model.Follow{}).Where(
		"follower_id = ? AND following_id = ?", historicalFollower.User.ID, deletedTarget.User.ID,
	).Count(&followRows).Error)
	require.EqualValues(t, 1, followRows)

	status, response, _ = performJSON(t, engine, http.MethodPost,
		userPath(deletedTarget.User.ID)+"/follow", nil, lateFollower.Token)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizNotFound, response.ErrorCode)
	require.NoError(t, gdb.Model(&model.Follow{}).Where(
		"follower_id = ? AND following_id = ?", lateFollower.User.ID, deletedTarget.User.ID,
	).Count(&followRows).Error)
	require.Zero(t, followRows, "不得新关注已注销用户")
}

func testUserFavoritesPagination(
	t *testing.T,
	cfg appconfig.Config,
	engine *server.Hertz,
	gdb *gorm.DB,
	fixtures *testutil.Fixtures,
) {
	t.Helper()
	author := fixtures.CreateActor(cfg)
	collector := fixtures.CreateActor(cfg)
	posts := []testutil.PostFixture{
		fixtures.CreatePost(author.User.ID, testutil.WithPostTitle("收藏分页一")),
		fixtures.CreatePost(author.User.ID, testutil.WithPostTitle("收藏分页二")),
		fixtures.CreatePost(author.User.ID, testutil.WithPostTitle("收藏分页三")),
	}
	base := time.Now().UTC().Add(-time.Hour)
	for index, post := range posts {
		require.NoError(t, gdb.Create(&model.Favorite{
			UserID: collector.User.ID, PostID: post.Post.ID,
			CreatedAt: base.Add(time.Duration(index) * time.Minute),
		}).Error)
	}

	path := userPath(collector.User.ID) + "/favorites"
	status, response, _ := performJSON(t, engine, http.MethodGet,
		path+"?page=2&limit=2", nil, collector.Token)
	require.Equal(t, http.StatusOK, status)
	var page service.PostList
	decodeData(t, response, &page)
	require.Equal(t, []uint64{posts[0].Post.ID}, postIDs(page.Posts))
	require.Equal(t, 2, page.Pagination.Page)
	require.Equal(t, 2, page.Pagination.Limit)
	require.EqualValues(t, 3, page.Pagination.Total)
	require.Equal(t, 2, page.Pagination.TotalPages)

	status, response, _ = performJSON(t, engine, http.MethodGet,
		path+"?page=99&limit=2", nil, collector.Token)
	require.Equal(t, http.StatusOK, status)
	decodeData(t, response, &page)
	require.Empty(t, page.Posts)
	require.Equal(t, 99, page.Pagination.Page)
	require.EqualValues(t, 3, page.Pagination.Total)

	status, response, _ = performJSON(t, engine, http.MethodGet,
		path+"?limit=101", nil, collector.Token)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	requireAuthFieldError(t, response, "limit", apierr.FieldOutOfRange)
	status, response, _ = performJSON(t, engine, http.MethodGet, path, nil, author.Token)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, apierr.BizPermissionDenied, response.ErrorCode)
}

func newUserTestEngine(
	t *testing.T,
	cfg appconfig.Config,
	database *dbinfra.DB,
	sender *captureEmailSender,
	moderation *testutil.MockModeration,
) *server.Hertz {
	t.Helper()
	return testutil.NewEngine(t, appRouter.Deps{
		Config: cfg, DB: database, EmailSender: sender, ContentModerator: moderation,
	})
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
