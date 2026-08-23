package router_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/jwtx"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

// concurrencyWidth is deliberately larger than the connection-level races seen in two-way tests.
// Sixteen contenders are still small enough to keep the PostgreSQL race suite deterministic in CI.
const concurrencyWidth = 16

func TestConcurrencyInvariantsAgainstPostgres(t *testing.T) {
	harness := testutil.NewHarness(t)
	fixture := loadPostFixture(t, harness.Database.GORM)

	t.Run("counters and relation idempotency", func(t *testing.T) {
		testConcurrentCountersAndRelations(t, harness, fixture)
	})
	t.Run("post and comment revisions", func(t *testing.T) {
		testConcurrentRevisionSequences(t, harness, fixture)
	})
	t.Run("image reference lifecycle", func(t *testing.T) {
		testConcurrentImageLifecycleInvariant(t, harness, fixture)
	})
	t.Run("session refresh and revocation", func(t *testing.T) {
		testConcurrentSessionRevocation(t, harness)
	})
	t.Run("verification send and consume", func(t *testing.T) {
		testConcurrentVerificationUse(t, harness)
	})
	t.Run("dictionary approvals", func(t *testing.T) {
		testConcurrentDictionaryApprovals(t, harness)
	})
	t.Run("case insensitive tag uniqueness", func(t *testing.T) {
		testConcurrentTagCreation(t, harness, fixture)
	})
}

func testConcurrentCountersAndRelations(
	t *testing.T,
	harness *testutil.Harness,
	fixture postFixture,
) {
	t.Helper()
	author := harness.Fixtures.CreateActor(harness.Config)
	actors := make([]testutil.Actor, concurrencyWidth)
	for index := range actors {
		actors[index] = harness.Fixtures.CreateActor(harness.Config)
	}
	post := createPost(t, harness.Engine, author.Token,
		sharePostPayload(fixture, "并发计数器帖子", []string{"并发计数"}))
	root := createComment(t, harness.Engine, actors[0].Token, post.ID,
		map[string]any{"content": "并发回复锚点"})

	likePath := postPath(post.ID) + "/like"
	favoritePath := postPath(post.ID) + "/favorite"
	commentLikePath := commentPath(root.Comment.ID) + "/like"
	for _, action := range []struct {
		name string
		path string
	}{
		{name: "post likes", path: likePath},
		{name: "post favorites", path: favoritePath},
	} {
		outcomes := runHTTPBarrier(t, harness.Engine, concurrencyWidth, func(index int) (string, string, any, string) {
			return http.MethodPost, action.path, nil, actors[index].Token
		})
		requireAllHTTPOK(t, outcomes, action.name)
	}
	commentLikes := runHTTPBarrier(t, harness.Engine, concurrencyWidth,
		func(_ int) (string, string, any, string) {
			return http.MethodPost, commentLikePath, nil, actors[0].Token
		})
	requireAllHTTPOK(t, commentLikes, "idempotent comment likes")

	rootComments := runHTTPBarrier(t, harness.Engine, concurrencyWidth, func(index int) (string, string, any, string) {
		return http.MethodPost, fmt.Sprintf("/api/v2/posts/%d/comments", post.ID), map[string]any{
			"content": fmt.Sprintf("并发楼主评论 %02d", index),
		}, actors[index].Token
	})
	requireAllHTTPOK(t, rootComments, "root comments")
	replies := runHTTPBarrier(t, harness.Engine, concurrencyWidth, func(index int) (string, string, any, string) {
		return http.MethodPost, fmt.Sprintf("/api/v2/posts/%d/comments", post.ID), map[string]any{
			"content": fmt.Sprintf("并发回复 %02d", index), "parent_id": root.Comment.ID,
		}, actors[index].Token
	})
	requireAllHTTPOK(t, replies, "comment replies")

	assertCounterTablesAgree(t, harness, post.ID, root.Comment.ID)

	for _, action := range []struct {
		name string
		path string
	}{
		{name: "post unlikes", path: likePath},
		{name: "post unfavorites", path: favoritePath},
	} {
		outcomes := runHTTPBarrier(t, harness.Engine, concurrencyWidth, func(index int) (string, string, any, string) {
			return http.MethodDelete, action.path, nil, actors[index].Token
		})
		requireAllHTTPOK(t, outcomes, action.name)
	}
	commentUnlikes := runHTTPBarrier(t, harness.Engine, concurrencyWidth,
		func(_ int) (string, string, any, string) {
			return http.MethodDelete, commentLikePath, nil, actors[0].Token
		})
	requireAllHTTPOK(t, commentUnlikes, "idempotent comment unlikes")
	assertCounterTablesAgree(t, harness, post.ID, root.Comment.ID)

	// The same actor deliberately races inserts and deletes. The terminal state is nondeterministic,
	// but the derived counter must equal the single possible relation row after every interleaving.
	togglePost := createPost(t, harness.Engine, author.Token,
		sharePostPayload(fixture, "并发交错点赞", []string{"交错点赞"}))
	togglePath := postPath(togglePost.ID) + "/like"
	toggled := runHTTPBarrier(t, harness.Engine, concurrencyWidth, func(index int) (string, string, any, string) {
		method := http.MethodPost
		if index%2 == 1 {
			method = http.MethodDelete
		}
		return method, togglePath, nil, actors[0].Token
	})
	requireAllHTTPOK(t, toggled, "interleaved like and unlike")
	assertPostLikeCounter(t, harness, togglePost.ID)

	status, _, _ := performJSON(t, harness.Engine, http.MethodDelete, togglePath, nil, actors[0].Token)
	require.Equal(t, http.StatusOK, status)
	rebuilt := runHTTPBarrier(t, harness.Engine, concurrencyWidth, func(_ int) (string, string, any, string) {
		return http.MethodPost, togglePath, nil, actors[0].Token
	})
	requireAllHTTPOK(t, rebuilt, "concurrent rebuild after unlike")
	assertPostLikeCounter(t, harness, togglePost.ID)
	var rebuiltRows int64
	require.NoError(t, harness.Database.GORM.Model(&model.PostLike{}).
		Where("post_id = ?", togglePost.ID).Count(&rebuiltRows).Error)
	require.EqualValues(t, 1, rebuiltRows, "取消后的并发重建只能留下一个关系行")
}

func assertCounterTablesAgree(
	t *testing.T,
	harness *testutil.Harness,
	postID uint64,
	rootCommentID uint64,
) {
	t.Helper()
	gdb := harness.Database.GORM
	var postRow model.Post
	require.NoError(t, gdb.First(&postRow, postID).Error)
	var postLikes, favorites, activeComments int64
	require.NoError(t, gdb.Model(&model.PostLike{}).Where("post_id = ?", postID).Count(&postLikes).Error)
	require.NoError(t, gdb.Model(&model.Favorite{}).Where("post_id = ?", postID).Count(&favorites).Error)
	require.NoError(t, gdb.Model(&model.Comment{}).
		Where("post_id = ? AND deleted_at IS NULL", postID).Count(&activeComments).Error)
	require.EqualValues(t, postLikes, postRow.LikeCount)
	require.EqualValues(t, favorites, postRow.FavoriteCount)
	require.EqualValues(t, activeComments, postRow.CommentCount)

	var root model.Comment
	require.NoError(t, gdb.First(&root, rootCommentID).Error)
	var commentLikes, activeReplies int64
	require.NoError(t, gdb.Model(&model.CommentLike{}).
		Where("comment_id = ?", rootCommentID).Count(&commentLikes).Error)
	require.NoError(t, gdb.Model(&model.Comment{}).
		Where("root_id = ? AND deleted_at IS NULL", rootCommentID).Count(&activeReplies).Error)
	require.EqualValues(t, commentLikes, root.LikeCount)
	require.EqualValues(t, activeReplies, root.ReplyCount)
}

func assertPostLikeCounter(t *testing.T, harness *testutil.Harness, postID uint64) {
	t.Helper()
	var post model.Post
	require.NoError(t, harness.Database.GORM.First(&post, postID).Error)
	var rows int64
	require.NoError(t, harness.Database.GORM.Model(&model.PostLike{}).
		Where("post_id = ?", postID).Count(&rows).Error)
	require.EqualValues(t, rows, post.LikeCount)
}

func testConcurrentRevisionSequences(
	t *testing.T,
	harness *testutil.Harness,
	fixture postFixture,
) {
	t.Helper()
	author := harness.Fixtures.CreateActor(harness.Config)
	post := createPost(t, harness.Engine, author.Token,
		sharePostPayload(fixture, "十六路帖子编辑", []string{"版本并发"}))
	postInputs := make([]service.UpdatePostInput, concurrencyWidth)
	for index := range postInputs {
		postInputs[index] = postUpdateInput(t, fixture,
			fmt.Sprintf("十六路帖子编辑 %02d", index), []string{fmt.Sprintf("版本%02d", index)})
	}
	postService := service.NewPostService(service.DirectPassContentModerator{})
	postErrors := runBarrier(t, concurrencyWidth, func(index int) error {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		return harness.Database.DB.RunInTx(ctx, func(txCtx context.Context) error {
			_, err := postService.Update(txCtx, post.ID, postInputs[index], author.User.ID)
			return err
		})
	})
	for _, err := range postErrors {
		require.NoError(t, err)
	}
	var postHistories []model.PostHistory
	require.NoError(t, harness.Database.GORM.Where("post_id = ?", post.ID).
		Order("revision").Find(&postHistories).Error)
	require.Equal(t, continuousRevisions(concurrencyWidth+1), postRevisions(postHistories))
	var storedPost model.Post
	require.NoError(t, harness.Database.GORM.First(&storedPost, post.ID).Error)
	assertSnapshotMatchesPost(t, harness.Database.GORM, storedPost,
		postHistories[len(postHistories)-1].Snapshot)

	comment := createComment(t, harness.Engine, author.Token, post.ID,
		map[string]any{"content": "十六路评论编辑"})
	commentService := service.NewCommentService(service.DirectPassContentModerator{})
	commentErrors := runBarrier(t, concurrencyWidth, func(index int) error {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		return harness.Database.DB.RunInTx(ctx, func(txCtx context.Context) error {
			_, err := commentService.Update(txCtx, comment.Comment.ID, service.UpdateCommentInput{
				Content: fmt.Sprintf("十六路评论编辑 %02d", index),
			}, author.User.ID)
			return err
		})
	})
	for _, err := range commentErrors {
		require.NoError(t, err)
	}
	var commentHistories []model.CommentHistory
	require.NoError(t, harness.Database.GORM.Where("comment_id = ?", comment.Comment.ID).
		Order("revision").Find(&commentHistories).Error)
	require.Equal(t, continuousRevisions(concurrencyWidth+1), commentRevisions(commentHistories))
	var storedComment model.Comment
	require.NoError(t, harness.Database.GORM.First(&storedComment, comment.Comment.ID).Error)
	require.Equal(t, commentHistories[len(commentHistories)-1].Content, storedComment.Content)
}

func continuousRevisions(count int) []int32 {
	values := make([]int32, count)
	for index := range values {
		values[index] = int32(index + 1)
	}
	return values
}

func testConcurrentImageLifecycleInvariant(
	t *testing.T,
	harness *testutil.Harness,
	fixture postFixture,
) {
	t.Helper()
	author := harness.Fixtures.CreateActor(harness.Config)
	asset := createPostAsset(t, harness.Database.GORM, author.User.ID, "sixteen-way-lifecycle")
	const removals = concurrencyWidth / 2
	originals := make([]service.PostCreateResult, removals)
	for index := range originals {
		payload := sharePostPayload(fixture, fmt.Sprintf("图片旧引用 %02d", index),
			[]string{fmt.Sprintf("图片旧%02d", index)})
		payload["images"] = []string{asset.PublicURL}
		originals[index] = createPost(t, harness.Engine, author.Token, payload)
	}

	postService := service.NewPostService(service.DirectPassContentModerator{})
	newInputs := make([]service.CreatePostInput, concurrencyWidth-removals)
	for index := removals; index < concurrencyWidth; index++ {
		newInputs[index-removals] = createPostInput(
			t, fixture, fmt.Sprintf("图片新引用 %02d", index),
			[]string{fmt.Sprintf("图片新%02d", index)}, []string{asset.PublicURL},
		)
	}
	type imageMutation struct {
		createdID uint64
		err       error
	}
	mutations := runBarrier(t, concurrencyWidth, func(index int) imageMutation {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		var createdID uint64
		err := harness.Database.DB.RunInTx(ctx, func(txCtx context.Context) error {
			if index < removals {
				return postService.Delete(txCtx, originals[index].ID, author.User.ID)
			}
			created, createErr := postService.Create(
				txCtx, newInputs[index-removals], author.User.ID,
			)
			if createErr == nil {
				createdID = created.ID
			}
			return createErr
		})
		return imageMutation{createdID: createdID, err: err}
	})
	createdIDs := make(map[uint64]struct{}, removals)
	for _, mutation := range mutations {
		require.NoError(t, mutation.err)
		if mutation.createdID != 0 {
			createdIDs[mutation.createdID] = struct{}{}
		}
	}
	require.Len(t, createdIDs, removals)

	var references int64
	require.NoError(t, harness.Database.GORM.Model(&model.PostImage{}).
		Where("image_asset_id = ?", asset.ID).Count(&references).Error)
	require.EqualValues(t, removals, references)
	require.NoError(t, harness.Database.GORM.First(&asset, asset.ID).Error)
	require.Equal(t, model.ImageStatusReady, asset.Status)
	require.Equal(t, references > 0, asset.Status == model.ImageStatusReady,
		"EXISTS(引用) 必须与 status='ready' 等价")
}

func testConcurrentSessionRevocation(t *testing.T, harness *testutil.Harness) {
	t.Helper()
	refreshUser := registerPostTestUser(t, harness.Engine, harness.Email,
		"concurrent-refresh@fdueat.com", "并发刷新用户")
	refreshed := runHTTPBarrier(t, harness.Engine, concurrencyWidth, func(_ int) (string, string, any, string) {
		return http.MethodPost, "/api/v2/auth/refresh",
			map[string]any{"refresh_token": refreshUser.RefreshToken}, ""
	})
	requireAllHTTPOK(t, refreshed, "concurrent refresh")
	claims, err := jwtx.NewCodec(harness.Config.JWTSecretKey).Parse(refreshUser.RefreshToken, jwtx.TypeRefresh)
	require.NoError(t, err)
	var refreshSession model.UserSession
	require.NoError(t, harness.Database.GORM.First(&refreshSession, uint64(claims.SessionID)).Error)
	require.Nil(t, refreshSession.RevokedAt)
	require.Equal(t, jwtx.Digest(refreshUser.RefreshToken), refreshSession.RefreshTokenDigest)

	logoutUser := registerPostTestUser(t, harness.Engine, harness.Email,
		"concurrent-logout-all@fdueat.com", "并发全端退出用户")
	logoutWave := runHTTPBarrier(t, harness.Engine, concurrencyWidth, func(index int) (string, string, any, string) {
		if index == 0 {
			return http.MethodPost, "/api/v2/auth/logout-all", nil, logoutUser.Token
		}
		return http.MethodPost, "/api/v2/auth/refresh",
			map[string]any{"refresh_token": logoutUser.RefreshToken}, ""
	})
	require.NoError(t, logoutWave[0].err)
	require.Equal(t, http.StatusOK, logoutWave[0].status)
	for _, outcome := range logoutWave[1:] {
		require.NoError(t, outcome.err)
		require.Contains(t, []int{http.StatusOK, http.StatusUnauthorized}, outcome.status)
	}
	assertNoActiveSessions(t, harness, logoutUser.User.ID)
	status, response, _ := performJSON(t, harness.Engine, http.MethodPost,
		"/api/v2/auth/refresh", map[string]any{"refresh_token": logoutUser.RefreshToken}, "")
	require.Equal(t, http.StatusUnauthorized, status)
	require.Equal(t, apierr.BizUnauthorized, response.ErrorCode)

	victim := registerPostTestUser(t, harness.Engine, harness.Email,
		"concurrent-ban-refresh@fdueat.com", "并发封禁用户")
	admin := harness.Fixtures.CreateActor(harness.Config, testutil.WithUserRole(model.UserRoleModerator))
	banWave := runHTTPBarrier(t, harness.Engine, concurrencyWidth, func(index int) (string, string, any, string) {
		if index == 0 {
			return http.MethodPut, fmt.Sprintf("/api/v2/admin/users/%d/status", victim.User.ID),
				map[string]any{"ban_is_permanent": true, "ban_reason": "并发封禁刷新测试"}, admin.Token
		}
		return http.MethodPost, "/api/v2/auth/refresh",
			map[string]any{"refresh_token": victim.RefreshToken}, ""
	})
	require.NoError(t, banWave[0].err)
	require.Equal(t, http.StatusOK, banWave[0].status)
	for _, outcome := range banWave[1:] {
		require.NoError(t, outcome.err)
		require.Contains(t, []int{http.StatusOK, http.StatusUnauthorized, http.StatusForbidden}, outcome.status)
	}
	var victimRow model.User
	require.NoError(t, harness.Database.GORM.First(&victimRow, victim.User.ID).Error)
	require.True(t, victimRow.BanIsPermanent)
	assertNoActiveSessions(t, harness, victim.User.ID)
}

func assertNoActiveSessions(t *testing.T, harness *testutil.Harness, userID uint64) {
	t.Helper()
	var active int64
	require.NoError(t, harness.Database.GORM.Model(&model.UserSession{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).Count(&active).Error)
	require.Zero(t, active)
}

func testConcurrentVerificationUse(t *testing.T, harness *testutil.Harness) {
	t.Helper()
	email := "concurrent-verification@fdueat.com"
	sends := runHTTPBarrier(t, harness.Engine, concurrencyWidth, func(_ int) (string, string, any, string) {
		return http.MethodPost, "/api/v2/auth/email-verification-codes",
			map[string]any{"email": email}, ""
	})
	successes := 0
	for _, outcome := range sends {
		require.NoError(t, outcome.err)
		switch outcome.status {
		case http.StatusOK:
			successes++
		case http.StatusTooManyRequests:
			require.Contains(t, []apierr.BizCode{
				apierr.BizVerifyCodeTooMany, apierr.BizVerifyCodeBusy,
			}, outcome.response.ErrorCode)
		default:
			require.Failf(t, "unexpected verification response", "status=%d response=%+v",
				outcome.status, outcome.response)
		}
	}
	require.Equal(t, 1, successes)
	harness.Email.RequireDeliveryCount(t, email, 1)
	var challenge model.EmailVerificationCode
	require.NoError(t, harness.Database.GORM.Where("email = ?", email).First(&challenge).Error)
	require.EqualValues(t, 1, challenge.SendCount, "并发发码不得突破数据库发送计数")

	code, ok := harness.Email.LastCode(email)
	require.True(t, ok)
	registrations := runHTTPBarrier(t, harness.Engine, concurrencyWidth, func(index int) (string, string, any, string) {
		return http.MethodPost, "/api/v2/auth/register", map[string]any{
			"email": email, "password": "password-123", "verification_code": code,
			"name": fmt.Sprintf("并发消费验证码 %02d", index),
		}, ""
	})
	registered, rejected := 0, 0
	for _, outcome := range registrations {
		require.NoError(t, outcome.err)
		switch outcome.status {
		case http.StatusOK:
			registered++
		case http.StatusBadRequest:
			require.Equal(t, apierr.BizVerifyCodeInvalid, outcome.response.ErrorCode)
			rejected++
		case http.StatusConflict:
			require.Equal(t, apierr.BizEmailTaken, outcome.response.ErrorCode)
			rejected++
		default:
			require.Failf(t, "unexpected registration response", "status=%d response=%+v",
				outcome.status, outcome.response)
		}
	}
	require.Equal(t, 1, registered)
	require.Equal(t, concurrencyWidth-1, rejected)
	var user model.User
	require.NoError(t, harness.Database.GORM.Where("email = ?", email).First(&user).Error)
	var users, sessions int64
	require.NoError(t, harness.Database.GORM.Model(&model.User{}).Where("email = ?", email).Count(&users).Error)
	require.NoError(t, harness.Database.GORM.Model(&model.UserSession{}).
		Where("user_id = ?", user.ID).Count(&sessions).Error)
	require.EqualValues(t, 1, users)
	require.EqualValues(t, 1, sessions)
	require.NoError(t, harness.Database.GORM.Where("email = ?", email).First(&challenge).Error)
	require.NotNil(t, challenge.ConsumedAt)
}

func testConcurrentDictionaryApprovals(t *testing.T, harness *testutil.Harness) {
	t.Helper()
	proposer := harness.Fixtures.CreateActor(harness.Config)
	reviewer := harness.Fixtures.CreateActor(harness.Config, testutil.WithUserRole(model.UserRoleDictReviewer))
	suggestion := createSuggestion(t, harness.Engine, proposer.Token, map[string]any{
		"kind": model.SuggestionKindCuisine, "proposed_name": "十六路审批菜系",
	})
	approvals := runHTTPBarrier(t, harness.Engine, concurrencyWidth, func(_ int) (string, string, any, string) {
		return http.MethodPost, suggestionApprovePath(suggestion.ID), map[string]any{}, reviewer.Token
	})
	approved, closed := 0, 0
	for _, outcome := range approvals {
		require.NoError(t, outcome.err)
		switch outcome.status {
		case http.StatusOK:
			approved++
		case http.StatusConflict:
			require.Equal(t, apierr.BizSuggestionClosed, outcome.response.ErrorCode)
			closed++
		default:
			require.Failf(t, "unexpected approval response", "status=%d response=%+v",
				outcome.status, outcome.response)
		}
	}
	require.Equal(t, 1, approved)
	require.Equal(t, concurrencyWidth-1, closed)
	var cuisines int64
	require.NoError(t, harness.Database.GORM.Model(&model.Cuisine{}).
		Where("name = ?", "十六路审批菜系").Count(&cuisines).Error)
	require.EqualValues(t, 1, cuisines)

	parent := createSuggestion(t, harness.Engine, proposer.Token, map[string]any{
		"kind": model.SuggestionKindCanteen, "proposed_name": "并发父餐厅",
	})
	child := createSuggestion(t, harness.Engine, proposer.Token, map[string]any{
		"kind": model.SuggestionKindCanteenWindow, "proposed_name": "并发子窗口",
		"parent_suggestion_id": parent.ID,
	})
	// This dependency has exactly two mutable rows, so the meaningful race is one parent approval
	// against one child approval. The same-suggestion race above supplies the 16-way contention.
	parentChild := runHTTPBarrier(t, harness.Engine, 2, func(index int) (string, string, any, string) {
		if index == 0 {
			return http.MethodPost, suggestionApprovePath(parent.ID), map[string]any{
				"code": "concurrent-parent-canteen", "campus": "并发校区",
			}, reviewer.Token
		}
		return http.MethodPost, suggestionApprovePath(child.ID), map[string]any{"floor": "2F"}, reviewer.Token
	})
	require.NoError(t, parentChild[0].err)
	require.Equal(t, http.StatusOK, parentChild[0].status)
	require.NoError(t, parentChild[1].err)
	if parentChild[1].status == http.StatusConflict {
		require.Equal(t, apierr.BizSuggestionParentPending, parentChild[1].response.ErrorCode)
		status, response, _ := performJSON(t, harness.Engine, http.MethodPost,
			suggestionApprovePath(child.ID), map[string]any{"floor": "2F"}, reviewer.Token)
		require.Equal(t, http.StatusOK, status,
			"error_code=%s message=%s", response.ErrorCode, response.Message)
	} else {
		require.Equal(t, http.StatusOK, parentChild[1].status)
	}
	var storedParent, storedChild model.DictionarySuggestion
	require.NoError(t, harness.Database.GORM.First(&storedParent, parent.ID).Error)
	require.NoError(t, harness.Database.GORM.First(&storedChild, child.ID).Error)
	require.Equal(t, model.SuggestionStatusApproved, storedParent.Status)
	require.Equal(t, model.SuggestionStatusApproved, storedChild.Status)
	require.NotNil(t, storedParent.ResultingCanteenID)
	require.Equal(t, storedParent.ResultingCanteenID, storedChild.ParentCanteenID)
	require.NotNil(t, storedChild.ResultingWindowID)
	var window model.CanteenWindow
	require.NoError(t, harness.Database.GORM.First(&window, *storedChild.ResultingWindowID).Error)
	require.Equal(t, *storedParent.ResultingCanteenID, window.CanteenID)
}

func testConcurrentTagCreation(t *testing.T, harness *testutil.Harness, fixture postFixture) {
	t.Helper()
	postRepository := repository.PostRepository{}
	type tagResult struct {
		id      uint64
		created bool
		err     error
	}
	results := runBarrier(t, concurrencyWidth, func(index int) tagResult {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var result tagResult
		result.err = harness.Database.DB.RunInTx(ctx, func(txCtx context.Context) error {
			name := "ConcurTag"
			if index%2 == 1 {
				name = "concurtag"
			}
			tag, created, err := postRepository.FindOrCreateTag(txCtx, name)
			if err == nil {
				result.id, result.created = tag.ID, created
			}
			return err
		})
		return result
	})
	created := 0
	var canonicalID uint64
	for _, result := range results {
		require.NoError(t, result.err)
		require.NotZero(t, result.id)
		if canonicalID == 0 {
			canonicalID = result.id
		}
		require.Equal(t, canonicalID, result.id)
		if result.created {
			created++
		}
	}
	require.Equal(t, 1, created, "同名标签只能有一个物理创建者")
	var rows int64
	require.NoError(t, harness.Database.GORM.Model(&model.Tag{}).
		Where("lower(name) = lower(?)", "ConcurTag").Count(&rows).Error)
	require.EqualValues(t, 1, rows)

	actors := make([]testutil.Actor, concurrencyWidth)
	payloads := make([]map[string]any, concurrencyWidth)
	for index := range actors {
		actors[index] = harness.Fixtures.CreateActor(harness.Config)
		payloads[index] = sharePostPayload(
			fixture, fmt.Sprintf("同名标签帖子 %02d", index), []string{"同名标签"},
		)
	}
	posts := runHTTPBarrier(t, harness.Engine, concurrencyWidth,
		func(index int) (string, string, any, string) {
			return http.MethodPost, "/api/v2/posts", payloads[index], actors[index].Token
		})
	requireAllHTTPOK(t, posts, "concurrent posts sharing one tag")
	require.NoError(t, harness.Database.GORM.Model(&model.Tag{}).
		Where("lower(name) = lower(?)", "同名标签").Count(&rows).Error)
	require.EqualValues(t, 1, rows, "并发帖子只能物理创建一个同名标签")
	var relations int64
	require.NoError(t, harness.Database.GORM.Table("post_tags AS pt").
		Joins("JOIN tags AS tag ON tag.id = pt.tag_id").
		Where("lower(tag.name) = lower(?)", "同名标签").Count(&relations).Error)
	require.EqualValues(t, concurrencyWidth, relations, "每个成功帖子都必须复用同一标签")
}

type barrierResult[T any] struct {
	index int
	value T
}

func runBarrier[T any](t *testing.T, count int, action func(index int) T) []T {
	t.Helper()
	ready := make(chan struct{}, count)
	start := make(chan struct{})
	completed := make(chan barrierResult[T], count)
	for index := range count {
		go func() {
			ready <- struct{}{}
			<-start
			completed <- barrierResult[T]{index: index, value: action(index)}
		}()
	}
	for range count {
		<-ready
	}
	close(start)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	results := make([]T, count)
	for range count {
		select {
		case result := <-completed:
			results[result.index] = result.value
		case <-ctx.Done():
			t.Fatalf("%d 路屏障并发未在期限内收敛: %v", count, ctx.Err())
		}
	}
	return results
}

func runHTTPBarrier(
	t *testing.T,
	engine *server.Hertz,
	count int,
	request func(index int) (method string, path string, payload any, token string),
) []asyncRequestResult {
	t.Helper()
	return runBarrier(t, count, func(index int) asyncRequestResult {
		method, path, payload, token := request(index)
		status, response, raw, err := performJSONRequest(engine, method, path, payload, token)
		return asyncRequestResult{status: status, response: response, raw: raw, err: err}
	})
}

func requireAllHTTPOK(
	t *testing.T,
	outcomes []asyncRequestResult,
	operation string,
) {
	t.Helper()
	for _, outcome := range outcomes {
		require.NoError(t, outcome.err, operation)
		require.Equal(t, http.StatusOK, outcome.status, "%s: response=%+v", operation, outcome.response)
	}
}
