package router_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	dbinfra "github.com/jingyijun/danshi_backend_go/internal/infra/db"
	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/money"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

type postFixture struct {
	Canteen model.Canteen
	Window  model.CanteenWindow
	Cuisine model.Cuisine
	Flavors []model.Flavor
}

type failingModerator struct{}

func (failingModerator) Review(
	context.Context,
	service.ModerationRequest,
) (service.ModerationResult, error) {
	return service.ModerationResult{}, errors.New("forced moderation failure")
}

func TestPostDomainAgainstPostgres(t *testing.T) {
	gdb, database := openAuthPostgres(t)
	cfg := authTestConfig()
	sender := newCaptureEmailSender()
	engine := authTestEngine(cfg, database, sender)
	author := registerPostTestUser(t, engine, sender, "post-author@fdueat.com", "帖子作者")
	other := registerPostTestUser(t, engine, sender, "post-reader@fdueat.com", "其他用户")
	fixture := loadPostFixture(t, gdb)

	t.Run("post route inventory", func(t *testing.T) {
		testPostRouteInventory(t, engine)
	})

	t.Run("create revision one and full contract", func(t *testing.T) {
		testPostCreateContract(t, engine, gdb, author, fixture)
	})

	t.Run("validation and dictionary errors", func(t *testing.T) {
		testPostValidationErrors(t, engine, gdb, author, fixture)
	})

	t.Run("edit main associations snapshot and moderation", func(t *testing.T) {
		testPostEditVersion(t, engine, gdb, author, fixture)
	})

	t.Run("tag canonical case remains editable", func(t *testing.T) {
		testTagCanonicalCaseRemainsEditable(t, engine, author, fixture)
	})

	t.Run("concurrent revisions are two and three", func(t *testing.T) {
		testConcurrentPostEdits(t, engine, gdb, database, author, fixture)
	})

	t.Run("bypassed main update is detected", func(t *testing.T) {
		testBypassedHistoryDetected(t, engine, gdb, database, author, fixture)
	})

	t.Run("history failure rolls back main and associations", func(t *testing.T) {
		testHistoryFailureRollback(t, engine, gdb, author, fixture)
	})

	t.Run("moderation failure rolls back whole create", func(t *testing.T) {
		testModerationFailureRollback(t, gdb, database, author, fixture)
	})

	t.Run("like and favorite are idempotent actions", func(t *testing.T) {
		testPostActions(t, engine, gdb, author, other, fixture)
	})

	t.Run("soft delete retires unreferenced image", func(t *testing.T) {
		testPostSoftDelete(t, engine, gdb, author, fixture)
	})

	t.Run("concurrent image delete and reference preserves invariant", func(t *testing.T) {
		testConcurrentImageReferences(t, engine, gdb, database, author, fixture)
	})

	t.Run("list select count is independent of page size", func(t *testing.T) {
		testPostListQueryCount(t, engine, gdb, author, fixture)
	})
}

func testPostRouteInventory(t *testing.T, engine *server.Hertz) {
	t.Helper()
	operations := make([]string, 0)
	for _, route := range engine.Routes() {
		if strings.HasPrefix(route.Path, "/api/v2/posts") && !strings.Contains(route.Path, "/comments") {
			operations = append(operations, route.Method+" "+route.Path)
		}
	}
	require.ElementsMatch(t, []string{
		"GET /api/v2/posts",
		"GET /api/v2/posts/:post_id",
		"POST /api/v2/posts",
		"PUT /api/v2/posts/:post_id",
		"DELETE /api/v2/posts/:post_id",
		"GET /api/v2/posts/:post_id/history",
		"POST /api/v2/posts/:post_id/like",
		"DELETE /api/v2/posts/:post_id/like",
		"POST /api/v2/posts/:post_id/favorite",
		"DELETE /api/v2/posts/:post_id/favorite",
	}, operations)
}

func testPostCreateContract(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	author service.AuthResult,
	fixture postFixture,
) {
	t.Helper()
	payload := sharePostPayload(fixture, "第一版标题", []string{"性价比", "Spicy"})
	post := createPost(t, engine, author.Token, payload)
	require.Equal(t, model.PostStatusApproved, post.Status)

	var stored model.Post
	require.NoError(t, gdb.First(&stored, post.ID).Error)
	require.Equal(t, "18.50", stored.Price.StringFixed(2))
	require.Zero(t, stored.LikeCount)
	require.Zero(t, stored.FavoriteCount)

	var histories []model.PostHistory
	require.NoError(t, gdb.Where("post_id = ?", post.ID).Order("revision").Find(&histories).Error)
	require.Len(t, histories, 1)
	require.EqualValues(t, 1, histories[0].Revision)
	assertSnapshotMatchesPost(t, gdb, stored, histories[0].Snapshot)

	var moderation model.ModerationRecord
	require.NoError(t, gdb.Where("post_id = ?", post.ID).First(&moderation).Error)
	require.Equal(t, histories[0].ID, *moderation.PostHistoryID)
	require.Equal(t, model.ModerationVerdictPass, moderation.Verdict)
	require.Equal(t, model.ModerationProvider("dev_allow"), moderation.Provider)

	beforeUpdatedAt := stored.UpdatedAt
	status, response, _ := performJSON(t, engine, http.MethodGet, postPath(post.ID), nil, author.Token)
	require.Equal(t, http.StatusOK, status)
	var detail map[string]any
	decodeData(t, response, &detail)
	require.Equal(t, "18.50", detail["price"])
	require.IsType(t, float64(0), detail["id"], "整数主键应作为 JSON number 输出")
	require.Equal(t, fixture.Canteen.Code, detail["canteen"].(map[string]any)["code"])
	require.EqualValues(t, fixture.Window.ID, detail["canteen_window"].(map[string]any)["id"])
	require.Equal(t, false, detail["is_edited"])

	listPath := "/api/v2/posts?canteen_code=" + url.QueryEscape(fixture.Canteen.Code) +
		"&flavors=" + url.QueryEscape(fixture.Flavors[0].Name) + "&tags=spicy&min_price=18.50&max_price=18.50"
	status, response, _ = performJSON(t, engine, http.MethodGet, listPath, nil, author.Token)
	require.Equal(t, http.StatusOK, status)
	var list service.PostList
	decodeData(t, response, &list)
	require.Len(t, list.Posts, 1)
	require.Equal(t, post.ID, list.Posts[0].ID)
	require.ElementsMatch(t, []string{"Spicy", "性价比"}, list.Posts[0].Tags)
	require.Equal(t, fixture.Flavors[0].Name, list.Posts[0].Flavors[0])

	require.NoError(t, gdb.First(&stored, post.ID).Error)
	require.EqualValues(t, 1, stored.ViewCount)
	require.True(t, stored.UpdatedAt.Equal(beforeUpdatedAt), "浏览不能改写内容更新时间")

	status, response, _ = performJSON(
		t, engine, http.MethodGet, fmt.Sprintf("%s/history", postPath(post.ID)), nil, author.Token,
	)
	require.Equal(t, http.StatusOK, status)
	var historyResponse service.PostHistoryList
	decodeData(t, response, &historyResponse)
	require.Len(t, historyResponse.Histories, 1)
	require.EqualValues(t, 1, historyResponse.Histories[0].Revision)
}

func testPostValidationErrors(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	author service.AuthResult,
	fixture postFixture,
) {
	t.Helper()
	otherCanteen := model.Canteen{
		Code: "post-test-other-canteen", Name: "测试第二餐厅", Campus: "测试校区", IsActive: true,
	}
	require.NoError(t, gdb.Create(&otherCanteen).Error)
	payload := sharePostPayload(fixture, "窗口错配", []string{"窗口"})
	payload["canteen_code"] = otherCanteen.Code
	status, response, _ := performJSON(t, engine, http.MethodPost, "/api/v2/posts", payload, author.Token)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, apierr.BizWindowNotInCanteen, response.ErrorCode)

	payload = sharePostPayload(fixture, "标签超限", []string{
		"一", "二", "三", "四", "五", "六", "七", "八", "九", "十", "十一",
	})
	status, response, _ = performJSON(t, engine, http.MethodPost, "/api/v2/posts", payload, author.Token)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, apierr.BizTagLimitExceeded, response.ErrorCode)

	payload = sharePostPayload(fixture, "价格数字", []string{"价格"})
	payload["price"] = 18.5
	status, response, _ = performJSON(t, engine, http.MethodPost, "/api/v2/posts", payload, author.Token)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	var fields struct {
		Errors []apierr.FieldError `json:"errors"`
	}
	decodeData(t, response, &fields)
	require.Equal(t, "price", fields.Errors[0].Field)
	require.Equal(t, apierr.FieldInvalidFormat, fields.Errors[0].Code)
}

func testPostEditVersion(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	author service.AuthResult,
	fixture postFixture,
) {
	t.Helper()
	post := createPost(t, engine, author.Token, sharePostPayload(fixture, "待编辑", []string{"旧标签"}))
	payload := sharePostPayload(fixture, "编辑后的标题", []string{"新标签"})
	payload["edit_reason"] = "修正文案"
	status, response, _ := performJSON(t, engine, http.MethodPut, postPath(post.ID), payload, author.Token)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)

	var stored model.Post
	require.NoError(t, gdb.First(&stored, post.ID).Error)
	require.Equal(t, "编辑后的标题", stored.Title)
	require.Equal(t, model.PostStatusApproved, stored.Status)
	var histories []model.PostHistory
	require.NoError(t, gdb.Where("post_id = ?", post.ID).Order("revision").Find(&histories).Error)
	require.Len(t, histories, 2)
	require.EqualValues(t, []int32{1, 2}, []int32{histories[0].Revision, histories[1].Revision})
	require.Equal(t, "修正文案", *histories[1].EditReason)
	assertSnapshotMatchesPost(t, gdb, stored, histories[1].Snapshot)

	var moderationCount int64
	require.NoError(t, gdb.Model(&model.ModerationRecord{}).
		Where("post_id = ?", post.ID).Count(&moderationCount).Error)
	require.EqualValues(t, 2, moderationCount, "每次编辑必须重新送审")
	var tagNames []string
	require.NoError(t, gdb.Table("post_tags AS pt").Select("t.name").
		Joins("JOIN tags AS t ON t.id = pt.tag_id").Where("pt.post_id = ?", post.ID).
		Order("t.name").Scan(&tagNames).Error)
	require.Equal(t, []string{"新标签"}, tagNames)
}

func testTagCanonicalCaseRemainsEditable(
	t *testing.T,
	engine *server.Hertz,
	author service.AuthResult,
	fixture postFixture,
) {
	t.Helper()
	createPost(t, engine, author.Token, sharePostPayload(fixture, "占位", []string{"ramen"}))
	post := createPost(t, engine, author.Token, sharePostPayload(fixture, "探针帖", []string{"ramen"}))

	payload := sharePostPayload(fixture, "探针帖", []string{"Ramen"})
	status, response, _ := performJSON(t, engine, http.MethodPut, postPath(post.ID), payload, author.Token)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)

	payload = sharePostPayload(fixture, "再改一次", []string{"Ramen"})
	status, response, _ = performJSON(t, engine, http.MethodPut, postPath(post.ID), payload, author.Token)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
}

func testConcurrentPostEdits(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	database *dbinfra.DB,
	author service.AuthResult,
	fixture postFixture,
) {
	t.Helper()
	post := createPost(t, engine, author.Token, sharePostPayload(fixture, "并发初始", []string{"并发"}))
	postService := service.NewPostService(service.DirectPassContentModerator{})
	inputs := []service.UpdatePostInput{
		postUpdateInput(t, fixture, "并发编辑 A", []string{"并发A"}),
		postUpdateInput(t, fixture, "并发编辑 B", []string{"并发B"}),
	}
	results := make(chan error, len(inputs))
	var wait sync.WaitGroup
	for _, input := range inputs {
		wait.Add(1)
		go func(value service.UpdatePostInput) {
			defer wait.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			results <- database.RunInTx(ctx, func(txCtx context.Context) error {
				_, err := postService.Update(txCtx, post.ID, value, author.User.ID)
				return err
			})
		}(input)
	}
	wait.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}

	var histories []model.PostHistory
	require.NoError(t, gdb.Where("post_id = ?", post.ID).Order("revision").Find(&histories).Error)
	revisions := make([]int32, 0, len(histories))
	for _, history := range histories {
		revisions = append(revisions, history.Revision)
	}
	require.Equal(t, []int32{1, 2, 3}, revisions, "并发编辑不得重复或跳号")
	var stored model.Post
	require.NoError(t, gdb.First(&stored, post.ID).Error)
	assertSnapshotMatchesPost(t, gdb, stored, histories[len(histories)-1].Snapshot)
}

func testBypassedHistoryDetected(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	database *dbinfra.DB,
	author service.AuthResult,
	fixture postFixture,
) {
	t.Helper()
	post := createPost(t, engine, author.Token, sharePostPayload(fixture, "绕过前", []string{"历史"}))
	require.NoError(t, gdb.Model(&model.Post{}).Where("id = ?", post.ID).
		UpdateColumn("title", "绕过历史直接改主表").Error)
	postService := service.NewPostService(service.DirectPassContentModerator{})
	err := database.RunInTx(context.Background(), func(ctx context.Context) error {
		_, updateErr := postService.Update(
			ctx, post.ID, postUpdateInput(t, fixture, "服务层编辑", []string{"历史"}), author.User.ID,
		)
		return updateErr
	})
	require.Error(t, err)
	require.Equal(t, http.StatusConflict, apierr.As(err).Status)
	require.Equal(t, apierr.BizConflict, apierr.As(err).Code)
	var historyCount int64
	require.NoError(t, gdb.Model(&model.PostHistory{}).Where("post_id = ?", post.ID).Count(&historyCount).Error)
	require.EqualValues(t, 1, historyCount)
}

func testHistoryFailureRollback(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	author service.AuthResult,
	fixture postFixture,
) {
	t.Helper()
	post := createPost(t, engine, author.Token, sharePostPayload(fixture, "回滚前", []string{"回滚旧"}))
	installHistoryFailureTrigger(t, gdb)
	payload := sharePostPayload(fixture, "不应提交", []string{"回滚新"})
	status, response, _ := performJSON(t, engine, http.MethodPut, postPath(post.ID), payload, author.Token)
	require.Equal(t, http.StatusInternalServerError, status)
	require.Equal(t, apierr.BizInternal, response.ErrorCode)
	removeHistoryFailureTrigger(t, gdb)

	var stored model.Post
	require.NoError(t, gdb.First(&stored, post.ID).Error)
	require.Equal(t, "回滚前", stored.Title)
	var historyCount int64
	require.NoError(t, gdb.Model(&model.PostHistory{}).Where("post_id = ?", post.ID).Count(&historyCount).Error)
	require.EqualValues(t, 1, historyCount)
	var tagNames []string
	require.NoError(t, gdb.Table("post_tags AS pt").Select("t.name").
		Joins("JOIN tags AS t ON t.id = pt.tag_id").Where("pt.post_id = ?", post.ID).Scan(&tagNames).Error)
	require.Equal(t, []string{"回滚旧"}, tagNames)
}

func testModerationFailureRollback(
	t *testing.T,
	gdb *gorm.DB,
	database *dbinfra.DB,
	author service.AuthResult,
	fixture postFixture,
) {
	t.Helper()
	postService := service.NewPostService(failingModerator{})
	input := createPostInput(t, fixture, "审核失败整事务回滚", []string{"审核回滚"}, nil)
	err := database.RunInTx(context.Background(), func(ctx context.Context) error {
		_, createErr := postService.Create(ctx, input, author.User.ID)
		return createErr
	})
	require.Error(t, err)
	var postCount int64
	require.NoError(t, gdb.Model(&model.Post{}).Where("title = ?", "审核失败整事务回滚").Count(&postCount).Error)
	require.Zero(t, postCount)
	var tagCount int64
	require.NoError(t, gdb.Model(&model.Tag{}).Where("name = ?", "审核回滚").Count(&tagCount).Error)
	require.Zero(t, tagCount, "审核失败时标签、主体、历史必须全部回滚")
}

func testPostActions(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	author service.AuthResult,
	other service.AuthResult,
	fixture postFixture,
) {
	t.Helper()
	post := createPost(t, engine, author.Token, sharePostPayload(fixture, "互动帖子", []string{"互动"}))
	var stored model.Post
	require.NoError(t, gdb.First(&stored, post.ID).Error)
	updatedAt := stored.UpdatedAt
	likePath := postPath(post.ID) + "/like"
	for range 2 {
		status, response, _ := performJSON(t, engine, http.MethodPost, likePath, nil, other.Token)
		require.Equal(t, http.StatusOK, status)
		var result service.PostLikeResult
		decodeData(t, response, &result)
		require.EqualValues(t, 1, result.LikeCount)
	}
	for range 2 {
		status, _, _ := performJSON(t, engine, http.MethodDelete, likePath, nil, other.Token)
		require.Equal(t, http.StatusOK, status)
	}
	status, response, _ := performJSON(t, engine, http.MethodPost, likePath, nil, other.Token)
	require.Equal(t, http.StatusOK, status)
	var likeResult service.PostLikeResult
	decodeData(t, response, &likeResult)
	require.EqualValues(t, 1, likeResult.LikeCount)

	favoritePath := postPath(post.ID) + "/favorite"
	for range 2 {
		status, _, _ = performJSON(t, engine, http.MethodPost, favoritePath, nil, other.Token)
		require.Equal(t, http.StatusOK, status)
	}
	status, _, _ = performJSON(t, engine, http.MethodDelete, favoritePath, nil, other.Token)
	require.Equal(t, http.StatusOK, status)
	status, _, _ = performJSON(t, engine, http.MethodPost, favoritePath, nil, other.Token)
	require.Equal(t, http.StatusOK, status)

	require.NoError(t, gdb.First(&stored, post.ID).Error)
	require.EqualValues(t, 1, stored.LikeCount)
	require.EqualValues(t, 1, stored.FavoriteCount)
	require.True(t, stored.UpdatedAt.Equal(updatedAt), "点赞收藏不得改写内容更新时间")
}

func testPostSoftDelete(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	author service.AuthResult,
	fixture postFixture,
) {
	t.Helper()
	asset := createPostAsset(t, gdb, author.User.ID, "soft-delete")
	payload := sharePostPayload(fixture, "软删除帖子", []string{"删除"})
	payload["images"] = []string{asset.PublicURL}
	post := createPost(t, engine, author.Token, payload)
	require.NoError(t, gdb.First(&asset, asset.ID).Error)
	require.Equal(t, model.ImageStatusReady, asset.Status)

	status, _, _ := performJSON(t, engine, http.MethodDelete, postPath(post.ID), nil, author.Token)
	require.Equal(t, http.StatusOK, status)
	var stored model.Post
	require.NoError(t, gdb.First(&stored, post.ID).Error)
	require.NotNil(t, stored.DeletedAt)
	require.Equal(t, model.DeleteReasonAuthor, *stored.DeletedReason)
	require.NoError(t, gdb.First(&asset, asset.ID).Error)
	require.Equal(t, model.ImageStatusRetired, asset.Status)

	status, response, _ := performJSON(t, engine, http.MethodGet, postPath(post.ID), nil, author.Token)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizPostDeleted, response.ErrorCode)
	status, response, _ = performJSON(t, engine, http.MethodDelete, postPath(post.ID), nil, author.Token)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizPostDeleted, response.ErrorCode)
}

func testConcurrentImageReferences(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	database *dbinfra.DB,
	author service.AuthResult,
	fixture postFixture,
) {
	t.Helper()
	asset := createPostAsset(t, gdb, author.User.ID, "concurrent-reference")
	payload := sharePostPayload(fixture, "图片原帖子", []string{"图片锁"})
	payload["images"] = []string{asset.PublicURL}
	original := createPost(t, engine, author.Token, payload)
	postService := service.NewPostService(service.DirectPassContentModerator{})
	createInput := createPostInput(t, fixture, "图片新帖子", []string{"图片新"}, []string{asset.PublicURL})

	errorsCh := make(chan error, 2)
	createdCh := make(chan uint64, 1)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		errorsCh <- database.RunInTx(context.Background(), func(ctx context.Context) error {
			return postService.Delete(ctx, original.ID, author.User.ID)
		})
	}()
	go func() {
		defer wait.Done()
		errorsCh <- database.RunInTx(context.Background(), func(ctx context.Context) error {
			created, err := postService.Create(ctx, createInput, author.User.ID)
			if err == nil {
				createdCh <- created.ID
			}
			return err
		})
	}()
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		require.NoError(t, err)
	}
	newPostID := <-createdCh

	var referenceCount int64
	require.NoError(t, gdb.Model(&model.PostImage{}).
		Where("image_asset_id = ?", asset.ID).Count(&referenceCount).Error)
	require.EqualValues(t, 1, referenceCount)
	var relation model.PostImage
	require.NoError(t, gdb.Where("image_asset_id = ?", asset.ID).First(&relation).Error)
	require.Equal(t, newPostID, relation.PostID)
	require.NoError(t, gdb.First(&asset, asset.ID).Error)
	require.Equal(t, model.ImageStatusReady, asset.Status, "存在引用时资产必须为 ready")
}

func testPostListQueryCount(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	author service.AuthResult,
	fixture postFixture,
) {
	t.Helper()
	for index := range 6 {
		createPost(t, engine, author.Token, sharePostPayload(
			fixture, fmt.Sprintf("查询数帖子 %d", index), []string{fmt.Sprintf("查询%d", index)},
		))
	}
	var enabled atomic.Bool
	var count atomic.Int64
	registerQueryCounter(t, gdb, &enabled, &count)
	measure := func(limit int) int64 {
		count.Store(0)
		enabled.Store(true)
		status, _, _ := performJSON(t, engine, http.MethodGet,
			fmt.Sprintf("/api/v2/posts?page=1&limit=%d", limit), nil, author.Token)
		enabled.Store(false)
		require.Equal(t, http.StatusOK, status)
		return count.Load()
	}
	one := measure(1)
	six := measure(6)
	require.Positive(t, one)
	require.Equal(t, one, six, "列表 SELECT 数不得随 page_size 增长")
}

func loadPostFixture(t *testing.T, gdb *gorm.DB) postFixture {
	t.Helper()
	var fixture postFixture
	require.NoError(t, gdb.Where("is_active").Order("id").First(&fixture.Canteen).Error)
	require.NoError(t, gdb.Where("is_active").Order("id").First(&fixture.Cuisine).Error)
	require.NoError(t, gdb.Where("is_active").Order("id").Limit(2).Find(&fixture.Flavors).Error)
	require.Len(t, fixture.Flavors, 2)
	floor := "1F"
	fixture.Window = model.CanteenWindow{
		CanteenID: fixture.Canteen.ID, Name: "Post 集成测试窗口", Floor: &floor, IsActive: true,
	}
	require.NoError(t, gdb.Create(&fixture.Window).Error)
	return fixture
}

func registerPostTestUser(
	t *testing.T,
	engine *server.Hertz,
	sender *captureEmailSender,
	email string,
	name string,
) service.AuthResult {
	t.Helper()
	sendCode(t, engine, email)
	status, response, _ := performJSON(t, engine, http.MethodPost, "/api/v2/auth/register", map[string]any{
		"email": email, "password": "password-123", "verification_code": sender.code(email),
		"name": name, "device_label": "Post 集成测试",
	}, "")
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
	var result service.AuthResult
	decodeData(t, response, &result)
	return result
}

func sharePostPayload(fixture postFixture, title string, tags []string) map[string]any {
	return map[string]any{
		"post_type": model.PostTypeShare, "share_type": model.ShareTypeRecommend,
		"status": model.PostStatusApproved, "title": title, "content": "集成测试正文",
		"category": model.PostCategoryFood, "canteen_code": fixture.Canteen.Code,
		"canteen_window_id": fixture.Window.ID, "cuisine": fixture.Cuisine.Name,
		"price": "18.5", "flavors": []string{fixture.Flavors[0].Name},
		"tags": tags, "images": []string{},
	}
}

func createPost(
	t *testing.T,
	engine *server.Hertz,
	token string,
	payload map[string]any,
) service.PostCreateResult {
	t.Helper()
	status, response, _ := performJSON(t, engine, http.MethodPost, "/api/v2/posts", payload, token)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
	var result service.PostCreateResult
	decodeData(t, response, &result)
	require.NotZero(t, result.ID)
	return result
}

func postPath(postID uint64) string { return fmt.Sprintf("/api/v2/posts/%d", postID) }

func postUpdateInput(
	t *testing.T,
	fixture postFixture,
	title string,
	tags []string,
) service.UpdatePostInput {
	t.Helper()
	amount, err := money.Parse("19.80")
	require.NoError(t, err)
	shareType := string(model.ShareTypeRecommend)
	status := string(model.PostStatusApproved)
	return service.UpdatePostInput{
		PostPayload: service.PostPayload{
			Title: title, Content: "并发编辑后的完整正文", Category: string(model.PostCategoryFood),
			ShareType: &shareType, CanteenCode: &fixture.Canteen.Code,
			CanteenWindowID: &fixture.Window.ID, Cuisine: &fixture.Cuisine.Name,
			Price: &amount, Flavors: []string{fixture.Flavors[1].Name}, Tags: tags, Images: []string{},
		},
		Status: &status,
	}
}

func createPostInput(
	t *testing.T,
	fixture postFixture,
	title string,
	tags []string,
	images []string,
) service.CreatePostInput {
	t.Helper()
	amount, err := money.Parse("20.00")
	require.NoError(t, err)
	shareType := string(model.ShareTypeRecommend)
	status := string(model.PostStatusApproved)
	return service.CreatePostInput{
		PostPayload: service.PostPayload{
			Title: title, Content: "事务协议测试正文", Category: string(model.PostCategoryFood),
			ShareType: &shareType, CanteenCode: &fixture.Canteen.Code,
			CanteenWindowID: &fixture.Window.ID, Cuisine: &fixture.Cuisine.Name,
			Price: &amount, Flavors: []string{fixture.Flavors[0].Name}, Tags: tags, Images: images,
		},
		PostType: string(model.PostTypeShare), Status: &status,
	}
}

func createPostAsset(t *testing.T, gdb *gorm.DB, authorID uint64, suffix string) model.ImageAsset {
	t.Helper()
	size := int64(1024)
	asset := model.ImageAsset{
		UploaderID: &authorID, Purpose: model.ImagePurposePost,
		ObjectKey: "post-test/" + suffix, PublicURL: "https://img.example.test/" + suffix + ".jpg",
		ContentType: "image/jpeg", Size: &size, Status: model.ImageStatusPending,
		Moderation: model.ModerationStatusPass,
	}
	require.NoError(t, gdb.Create(&asset).Error)
	return asset
}

func assertSnapshotMatchesPost(
	t *testing.T,
	gdb *gorm.DB,
	post model.Post,
	raw json.RawMessage,
) {
	t.Helper()
	var snapshot struct {
		PostType        model.PostType     `json:"post_type"`
		ShareType       *model.ShareType   `json:"share_type"`
		Title           string             `json:"title"`
		Content         string             `json:"content"`
		Category        model.PostCategory `json:"category"`
		CanteenID       *uint64            `json:"canteen_id"`
		CanteenWindowID *uint64            `json:"canteen_window_id"`
		CuisineID       *uint64            `json:"cuisine_id"`
		Price           *string            `json:"price"`
		BudgetMin       *int32             `json:"budget_min"`
		BudgetMax       *int32             `json:"budget_max"`
		Tags            []string           `json:"tags"`
		Images          []string           `json:"images"`
	}
	require.NoError(t, json.Unmarshal(raw, &snapshot))
	require.Equal(t, post.PostType, snapshot.PostType)
	require.Equal(t, post.ShareType, snapshot.ShareType)
	require.Equal(t, post.Title, snapshot.Title)
	require.Equal(t, post.Content, snapshot.Content)
	require.Equal(t, post.Category, snapshot.Category)
	require.Equal(t, post.CanteenID, snapshot.CanteenID)
	require.Equal(t, post.CanteenWindowID, snapshot.CanteenWindowID)
	require.Equal(t, post.CuisineID, snapshot.CuisineID)
	if post.Price == nil {
		require.Nil(t, snapshot.Price)
	} else {
		require.Equal(t, post.Price.StringFixed(2), *snapshot.Price)
	}
	require.Equal(t, post.BudgetMin, snapshot.BudgetMin)
	require.Equal(t, post.BudgetMax, snapshot.BudgetMax)

	var tags []string
	require.NoError(t, gdb.Table("post_tags AS pt").Select("t.name").
		Joins("JOIN tags AS t ON t.id = pt.tag_id").Where("pt.post_id = ?", post.ID).Scan(&tags).Error)
	sort.Slice(tags, func(i, j int) bool { return strings.ToLower(tags[i]) < strings.ToLower(tags[j]) })
	require.Equal(t, tags, snapshot.Tags)
	var images []string
	require.NoError(t, gdb.Table("post_images AS pi").Select("a.public_url").
		Joins("JOIN image_assets AS a ON a.id = pi.image_asset_id").Where("pi.post_id = ?", post.ID).
		Order("pi.position").Scan(&images).Error)
	if images == nil {
		images = []string{}
	}
	require.Equal(t, images, snapshot.Images)
}

func installHistoryFailureTrigger(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	require.NoError(t, gdb.Exec(`
		CREATE OR REPLACE FUNCTION post_test_fail_history() RETURNS trigger
		LANGUAGE plpgsql AS $func$
		BEGIN
			RAISE EXCEPTION 'forced post history failure';
		END;
		$func$;
		CREATE TRIGGER trg_post_test_fail_history
		BEFORE INSERT ON post_histories
		FOR EACH ROW EXECUTE FUNCTION post_test_fail_history();
	`).Error)
	t.Cleanup(func() { removeHistoryFailureTrigger(t, gdb) })
}

func removeHistoryFailureTrigger(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	require.NoError(t, gdb.Exec(`
		DROP TRIGGER IF EXISTS trg_post_test_fail_history ON post_histories;
		DROP FUNCTION IF EXISTS post_test_fail_history();
	`).Error)
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
		Register("post_test_count_query", callback))
	require.NoError(t, gdb.Callback().Raw().Before("gorm:raw").
		Register("post_test_count_raw", callback))
}
