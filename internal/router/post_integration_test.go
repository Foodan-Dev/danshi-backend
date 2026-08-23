package router_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	dbinfra "github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/money"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

type postFixture struct {
	Canteen model.Canteen
	Window  model.CanteenWindow
	Cuisine model.Cuisine
	Flavors []model.Flavor
}

func failingModerator() *testutil.MockModeration {
	mock := testutil.NewMockModeration()
	mock.SetDefaultContent(testutil.ContentHTTPFailure(http.StatusInternalServerError))
	return mock
}

func TestPostDomainAgainstPostgres(t *testing.T) {
	moderation := testutil.NewMockModeration()
	moderation.ProgramContent(
		testutil.ContentModerationRule{
			Target: service.ModerationTargetPost, Contains: "需人工审核帖子",
			Outcome: testutil.ContentVerdict(model.ModerationVerdictReview, []string{"manual_review"}, nil),
		},
		testutil.ContentModerationRule{
			Target: service.ModerationTargetPost, Contains: "违规拦截帖子",
			Outcome: testutil.ContentVerdict(model.ModerationVerdictBlock, []string{"blocked"}, nil),
		},
		testutil.ContentModerationRule{
			Target: service.ModerationTargetPost, Contains: "编辑后违规标题",
			Outcome: testutil.ContentVerdict(model.ModerationVerdictBlock, []string{"edited_block"}, nil),
		},
	)
	moderation.ProgramImage(
		testutil.ImageModerationRule{Call: 1, Outcome: testutil.ImagePending("post-image-pass")},
		testutil.ImageModerationRule{Call: 2, Outcome: testutil.ImagePending("post-image-block")},
		testutil.ImageModerationRule{Call: 3, Outcome: testutil.ImagePending("post-image-early")},
		testutil.ImageModerationRule{Call: 4, Outcome: testutil.ImagePending("post-image-duplicate")},
	)
	h := testutil.NewHarness(t, testutil.WithModerationMock(moderation))
	gdb, database := h.Database.GORM, h.Database.DB
	sender, engine := h.Email, h.Engine
	author := registerPostTestUser(t, engine, sender, "post-author@fdueat.com", "帖子作者")
	other := registerPostTestUser(t, engine, sender, "post-reader@fdueat.com", "其他用户")
	dictionaries := h.Fixtures.CreateDictionaries()
	fixture := postFixture{
		Canteen: dictionaries.Canteen, Window: dictionaries.Window,
		Cuisine: dictionaries.Cuisine, Flavors: dictionaries.Flavors,
	}

	t.Run("post route inventory", func(t *testing.T) {
		testPostRouteInventory(t, engine)
	})

	t.Run("create revision one and full contract", func(t *testing.T) {
		testPostCreateContract(t, engine, gdb, author, fixture)
	})

	t.Run("validation and dictionary errors", func(t *testing.T) {
		testPostValidationErrors(t, engine, gdb, author, fixture)
	})

	t.Run("share seeking and unicode boundary matrix", func(t *testing.T) {
		testPostTypeAndUnicodeBoundaries(t, engine, author, fixture)
	})

	t.Run("price budget tag and image boundary matrix", func(t *testing.T) {
		testPostPayloadBoundaries(t, engine, gdb, h.Fixtures, author, other, fixture)
	})

	t.Run("edit main associations snapshot and moderation", func(t *testing.T) {
		testPostEditVersion(t, engine, gdb, author, fixture)
	})

	t.Run("tag canonical case remains editable", func(t *testing.T) {
		testTagCanonicalCaseRemainsEditable(t, engine, author, fixture)
	})

	t.Run("edit authorization deleted no-op and clear optionals", func(t *testing.T) {
		testPostEditEdges(t, engine, gdb, author, other, fixture)
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

	t.Run("list filters pagination and stable sorting", func(t *testing.T) {
		testPostListMatrix(t, engine, gdb, h.Fixtures, author, fixture)
	})

	t.Run("text moderation visibility and edit transitions", func(t *testing.T) {
		testPostTextModeration(t, engine, gdb, author, other, fixture)
	})

	t.Run("image callbacks cover pass block early and duplicate delivery", func(t *testing.T) {
		testPostImageModeration(t, h, author, fixture)
	})

	t.Run("soft delete retires unreferenced image", func(t *testing.T) {
		testPostSoftDelete(t, engine, gdb, author, fixture)
	})

	t.Run("concurrent image delete and reference preserves invariant", func(t *testing.T) {
		testConcurrentImageReferences(t, engine, gdb, database, author, fixture)
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
	require.Equal(t, testutil.MockModerationProvider, moderation.Provider)

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
	var list service.PostFeedList
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

func testPostTypeAndUnicodeBoundaries(
	t *testing.T,
	engine *server.Hertz,
	author service.AuthResult,
	fixture postFixture,
) {
	t.Helper()
	boundary := sharePostPayload(fixture, strings.Repeat("界", 200), []string{strings.Repeat("味", 10)})
	boundary["content"] = strings.Repeat("文", 5000)
	created := createPost(t, engine, author.Token, boundary)
	require.Equal(t, model.PostStatusApproved, created.Status)

	seeking := seekingPostPayload(fixture, "求推荐合法边界")
	created = createPost(t, engine, author.Token, seeking)
	require.Equal(t, model.PostTypeSeeking, created.PostType)

	tests := []struct {
		name   string
		field  string
		code   apierr.FieldCode
		mutate func(map[string]any)
	}{
		{
			name: "empty title", field: "title", code: apierr.FieldRequired,
			mutate: func(payload map[string]any) { payload["title"] = "　 " },
		},
		{
			name: "title over rune limit", field: "title", code: apierr.FieldTooLong,
			mutate: func(payload map[string]any) { payload["title"] = strings.Repeat("界", 201) },
		},
		{
			name: "empty content", field: "content", code: apierr.FieldRequired,
			mutate: func(payload map[string]any) { payload["content"] = "\n\t" },
		},
		{
			name: "content over rune limit", field: "content", code: apierr.FieldTooLong,
			mutate: func(payload map[string]any) { payload["content"] = strings.Repeat("文", 5001) },
		},
		{
			name: "invalid post type", field: "post_type", code: apierr.FieldInvalidEnum,
			mutate: func(payload map[string]any) { payload["post_type"] = "poll" },
		},
		{
			name: "invalid category", field: "category", code: apierr.FieldInvalidEnum,
			mutate: func(payload map[string]any) { payload["category"] = "drink" },
		},
		{
			name: "invalid status", field: "status", code: apierr.FieldInvalidEnum,
			mutate: func(payload map[string]any) { payload["status"] = model.PostStatusRejected },
		},
		{
			name: "published share requires share type", field: "share_type", code: apierr.FieldRequired,
			mutate: func(payload map[string]any) { delete(payload, "share_type") },
		},
		{
			name: "share rejects seeking budget", field: "budget_range", code: apierr.FieldConflict,
			mutate: func(payload map[string]any) {
				payload["budget_range"] = map[string]any{"min": 10, "max": 20}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := sharePostPayload(fixture, "类型与字符边界", []string{})
			test.mutate(payload)
			status, response, _ := performJSON(
				t, engine, http.MethodPost, "/api/v2/posts", payload, author.Token,
			)
			requireFieldError(t, status, response, test.field, test.code)
		})
	}

	seekingTests := []struct {
		name   string
		field  string
		mutate func(map[string]any)
	}{
		{
			name: "seeking rejects share type", field: "share_type",
			mutate: func(payload map[string]any) { payload["share_type"] = model.ShareTypeRecommend },
		},
		{
			name: "seeking rejects price", field: "price",
			mutate: func(payload map[string]any) { payload["price"] = "10.00" },
		},
		{
			name: "seeking rejects share flavors", field: "flavors",
			mutate: func(payload map[string]any) { payload["flavors"] = []string{fixture.Flavors[0].Name} },
		},
		{
			name: "seeking rejects overlapping preferences", field: "preferences",
			mutate: func(payload map[string]any) {
				payload["preferences"] = map[string]any{
					"prefer_flavors": []string{fixture.Flavors[0].Name},
					"avoid_flavors":  []string{fixture.Flavors[0].Name},
				}
			},
		},
		{
			name: "seeking rejects reversed budget", field: "budget_range.max",
			mutate: func(payload map[string]any) {
				payload["budget_range"] = map[string]any{"min": 30, "max": 20}
			},
		},
	}
	for _, test := range seekingTests {
		t.Run(test.name, func(t *testing.T) {
			payload := seekingPostPayload(fixture, "提问帖字段互斥")
			test.mutate(payload)
			status, response, _ := performJSON(
				t, engine, http.MethodPost, "/api/v2/posts", payload, author.Token,
			)
			requireFieldError(t, status, response, test.field, apierr.FieldConflict)
		})
	}
}

func testPostPayloadBoundaries(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	fixtures *testutil.Fixtures,
	author service.AuthResult,
	other service.AuthResult,
	fixture postFixture,
) {
	t.Helper()
	prices := []struct {
		name  string
		value any
	}{
		{name: "number", value: 18.5},
		{name: "scientific notation", value: "1e2"},
		{name: "negative", value: "-1.00"},
		{name: "three decimals", value: "1.001"},
		{name: "more than eight integer digits", value: "123456789.00"},
	}
	for _, test := range prices {
		t.Run("price "+test.name, func(t *testing.T) {
			payload := sharePostPayload(fixture, "价格边界", []string{})
			payload["price"] = test.value
			status, response, _ := performJSON(
				t, engine, http.MethodPost, "/api/v2/posts", payload, author.Token,
			)
			requireFieldError(t, status, response, "price", apierr.FieldInvalidFormat)
		})
	}

	longTag := sharePostPayload(fixture, "标签长度边界", []string{strings.Repeat("标", 11)})
	status, response, _ := performJSON(t, engine, http.MethodPost, "/api/v2/posts", longTag, author.Token)
	requireFieldError(t, status, response, "tags", apierr.FieldTooLong)

	tenTags := make([]string, 10)
	for index := range tenTags {
		tenTags[index] = fmt.Sprintf("边界%d", index)
	}
	created := createPost(t, engine, author.Token, sharePostPayload(fixture, "十个标签合法", tenTags))
	require.NotZero(t, created.ID)

	inactive := fixtures.CreateDictionaries(func(spec *testutil.DictionarySpec) {
		spec.IsActive = false
	})
	inactivePayload := sharePostPayload(fixture, "停用词表", []string{})
	inactivePayload["canteen_code"] = inactive.Canteen.Code
	inactivePayload["canteen_window_id"] = nil
	status, response, _ = performJSON(
		t, engine, http.MethodPost, "/api/v2/posts", inactivePayload, author.Token,
	)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizDictItemNotFound, response.ErrorCode)

	otherImage := fixtures.CreateImage(other.User.ID)
	otherImagePayload := sharePostPayload(fixture, "引用他人图片", []string{})
	otherImagePayload["images"] = []string{otherImage.PublicURL}
	status, response, _ = performJSON(
		t, engine, http.MethodPost, "/api/v2/posts", otherImagePayload, author.Token,
	)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, apierr.BizImageNotOwned, response.ErrorCode)

	blockedImage := fixtures.CreateImage(author.User.ID, func(asset *model.ImageAsset) {
		asset.Moderation = model.ModerationStatusBlock
	})
	blockedImagePayload := sharePostPayload(fixture, "引用拦截图片", []string{})
	blockedImagePayload["images"] = []string{blockedImage.PublicURL}
	status, response, _ = performJSON(
		t, engine, http.MethodPost, "/api/v2/posts", blockedImagePayload, author.Token,
	)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, apierr.BizImageNotApproved, response.ErrorCode)

	purgedImage := fixtures.CreateImage(author.User.ID)
	purgedImage.PublicURL = model.PurgedImageURL(purgedImage.ID)
	purgedImage.Status = model.ImageStatusRetired
	require.NoError(t, gdb.Model(&model.ImageAsset{}).Where("id = ?", purgedImage.ID).Updates(map[string]any{
		"public_url": purgedImage.PublicURL, "status": purgedImage.Status,
	}).Error)
	purgedImagePayload := sharePostPayload(fixture, "引用已回收图片", []string{})
	purgedImagePayload["images"] = []string{purgedImage.PublicURL}
	status, response, _ = performJSON(
		t, engine, http.MethodPost, "/api/v2/posts", purgedImagePayload, author.Token,
	)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizImageNotFound, response.ErrorCode)

	images := make([]model.ImageAsset, 9)
	imageURLs := make([]string, 9)
	for index := range images {
		images[index] = fixtures.CreateImage(author.User.ID)
		imageURLs[index] = images[index].PublicURL
	}
	nineImagePayload := sharePostPayload(fixture, "九张图片合法", []string{})
	nineImagePayload["images"] = imageURLs
	created = createPost(t, engine, author.Token, nineImagePayload)
	var imageCount int64
	require.NoError(t, gdb.Model(&model.PostImage{}).Where("post_id = ?", created.ID).Count(&imageCount).Error)
	require.EqualValues(t, 9, imageCount)

	tenImagePayload := sharePostPayload(fixture, "十张图片越界", []string{})
	tenImagePayload["images"] = append(append([]string{}, imageURLs...), "https://image.example.test/tenth.jpg")
	status, response, _ = performJSON(
		t, engine, http.MethodPost, "/api/v2/posts", tenImagePayload, author.Token,
	)
	requireFieldError(t, status, response, "images", apierr.FieldOutOfRange)

	duplicateImagePayload := sharePostPayload(fixture, "重复图片", []string{})
	duplicateImagePayload["images"] = []string{imageURLs[0], imageURLs[0]}
	status, response, _ = performJSON(
		t, engine, http.MethodPost, "/api/v2/posts", duplicateImagePayload, author.Token,
	)
	requireFieldError(t, status, response, "images", apierr.FieldConflict)
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

func testPostEditEdges(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	author service.AuthResult,
	other service.AuthResult,
	fixture postFixture,
) {
	t.Helper()
	originalPayload := sharePostPayload(fixture, "编辑边界原帖", []string{"编辑边界"})
	post := createPost(t, engine, author.Token, originalPayload)

	status, response, _ := performJSON(
		t, engine, http.MethodPut, postPath(post.ID), originalPayload, other.Token,
	)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, apierr.BizNotOwner, response.ErrorCode)
	status, response, _ = performJSON(
		t, engine, http.MethodGet, postPath(post.ID)+"/history", nil, other.Token,
	)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, apierr.BizNotOwner, response.ErrorCode)

	status, response, _ = performJSON(
		t, engine, http.MethodPut, postPath(post.ID), originalPayload, author.Token,
	)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
	var histories []model.PostHistory
	require.NoError(t, gdb.Where("post_id = ?", post.ID).Order("revision").Find(&histories).Error)
	require.Equal(t, []int32{1, 2}, postRevisions(histories),
		"全量 PUT 即使内容相同也必须形成可追溯的新版本")

	clearPayload := map[string]any{
		"post_type": model.PostTypeShare, "share_type": model.ShareTypeRecommend,
		"status": model.PostStatusApproved, "title": "清空可选字段后",
		"content": "保留必填正文", "category": model.PostCategoryFood,
		"canteen_code": nil, "canteen_window_id": nil, "cuisine": nil, "price": nil,
		"flavors": []string{}, "tags": []string{}, "images": []string{},
	}
	status, response, _ = performJSON(
		t, engine, http.MethodPut, postPath(post.ID), clearPayload, author.Token,
	)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
	var stored model.Post
	require.NoError(t, gdb.First(&stored, post.ID).Error)
	require.Nil(t, stored.CanteenID)
	require.Nil(t, stored.CanteenWindowID)
	require.Nil(t, stored.CuisineID)
	require.Nil(t, stored.Price)
	for _, target := range []any{&model.PostTag{}, &model.PostFlavor{}, &model.PostImage{}} {
		var count int64
		require.NoError(t, gdb.Model(target).Where("post_id = ?", post.ID).Count(&count).Error)
		require.Zero(t, count)
	}

	deleted := createPost(t, engine, author.Token,
		sharePostPayload(fixture, "已删除不可编辑", []string{"删除编辑"}))
	status, response, _ = performJSON(t, engine, http.MethodDelete, postPath(deleted.ID), nil, other.Token)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, apierr.BizNotOwner, response.ErrorCode)
	status, _, _ = performJSON(t, engine, http.MethodDelete, postPath(deleted.ID), nil, author.Token)
	require.Equal(t, http.StatusOK, status)
	status, response, _ = performJSON(
		t, engine, http.MethodPut, postPath(deleted.ID), originalPayload, author.Token,
	)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizPostDeleted, response.ErrorCode)
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
	postService := service.NewPostService(failingModerator())
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

	timeoutModerator := testutil.NewMockModeration()
	timeoutModerator.SetDefaultContent(testutil.ContentTimeout())
	timeoutService := service.NewPostService(timeoutModerator)
	timeoutCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err = database.RunInTx(timeoutCtx, func(ctx context.Context) error {
		_, createErr := timeoutService.Create(
			ctx, createPostInput(t, fixture, "审核超时整事务回滚", []string{}, nil), author.User.ID,
		)
		return createErr
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	timeoutModerator.RequireContentCalls(t, 1)
	require.NoError(t, gdb.Model(&model.Post{}).
		Where("title = ?", "审核超时整事务回滚").Count(&postCount).Error)
	require.Zero(t, postCount)
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

	status, _, _ = performJSON(t, engine, http.MethodDelete, likePath, nil, other.Token)
	require.Equal(t, http.StatusOK, status)
	const concurrentLikes = 8
	ready := make(chan struct{}, concurrentLikes)
	start := make(chan struct{})
	results := make(chan asyncRequestResult, concurrentLikes)
	for range concurrentLikes {
		go func() {
			ready <- struct{}{}
			<-start
			requestStatus, requestResponse, raw, requestErr := performJSONRequest(
				engine, http.MethodPost, likePath, nil, other.Token,
			)
			results <- asyncRequestResult{
				status: requestStatus, response: requestResponse, raw: raw, err: requestErr,
			}
		}()
	}
	for range concurrentLikes {
		<-ready
	}
	close(start)
	for range concurrentLikes {
		result := <-results
		require.NoError(t, result.err)
		require.Equal(t, http.StatusOK, result.status)
		var like service.PostLikeResult
		decodeData(t, result.response, &like)
		require.EqualValues(t, 1, like.LikeCount)
	}
	require.NoError(t, gdb.First(&stored, post.ID).Error)
	require.EqualValues(t, 1, stored.LikeCount, "并发重复点赞只能形成一条关系")
}

func testPostListMatrix(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	fixtures *testutil.Fixtures,
	author service.AuthResult,
	fixture postFixture,
) {
	t.Helper()
	groupTag := "筛选同组"
	firstPayload := sharePostPayload(fixture, "筛选甲", []string{groupTag, "筛选甲"})
	firstPayload["price"] = "10.00"
	first := createPost(t, engine, author.Token, firstPayload)

	secondPayload := sharePostPayload(fixture, "筛选乙", []string{groupTag, "筛选乙"})
	secondPayload["share_type"] = model.ShareTypeWarning
	secondPayload["category"] = model.PostCategoryRecipe
	secondPayload["price"] = "30.00"
	secondPayload["flavors"] = []string{fixture.Flavors[1].Name}
	second := createPost(t, engine, author.Token, secondPayload)

	thirdPayload := seekingPostPayload(fixture, "筛选丙")
	thirdPayload["tags"] = []string{groupTag, "筛选丙"}
	third := createPost(t, engine, author.Token, thirdPayload)

	fixedCreatedAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, gdb.Model(&model.Post{}).Where("id IN ?", []uint64{first.ID, second.ID, third.ID}).
		UpdateColumn("created_at", fixedCreatedAt).Error)

	assertFilter := func(values url.Values, included []uint64, excluded []uint64) {
		t.Helper()
		values.Set("limit", "100")
		status, response, _ := performJSON(
			t, engine, http.MethodGet, "/api/v2/posts?"+values.Encode(), nil, author.Token,
		)
		require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
		var result service.PostFeedList
		decodeData(t, response, &result)
		ids := postListIDs(result.Posts)
		for _, id := range included {
			require.Contains(t, ids, id)
		}
		for _, id := range excluded {
			require.NotContains(t, ids, id)
		}
	}
	assertFilter(url.Values{"post_type": {string(model.PostTypeShare)}},
		[]uint64{first.ID, second.ID}, []uint64{third.ID})
	assertFilter(url.Values{"share_type": {string(model.ShareTypeWarning)}},
		[]uint64{second.ID}, []uint64{first.ID, third.ID})
	assertFilter(url.Values{"category": {string(model.PostCategoryRecipe)}},
		[]uint64{second.ID}, []uint64{first.ID, third.ID})
	assertFilter(url.Values{"canteen_code": {fixture.Canteen.Code}},
		[]uint64{first.ID, second.ID, third.ID}, nil)
	assertFilter(url.Values{"cuisine": {fixture.Cuisine.Name}},
		[]uint64{first.ID, second.ID, third.ID}, nil)
	assertFilter(url.Values{"flavors": {fixture.Flavors[1].Name}},
		[]uint64{second.ID, third.ID}, []uint64{first.ID})
	assertFilter(url.Values{"tags": {"筛选甲"}}, []uint64{first.ID}, []uint64{second.ID, third.ID})
	assertFilter(url.Values{"min_price": {"10.01"}}, []uint64{second.ID}, []uint64{first.ID, third.ID})
	assertFilter(url.Values{"max_price": {"10.00"}}, []uint64{first.ID}, []uint64{second.ID, third.ID})
	assertFilter(url.Values{"min_price": {"9.99"}, "max_price": {"10.00"}},
		[]uint64{first.ID}, []uint64{second.ID, third.ID})
	assertFilter(url.Values{
		"post_type": {string(model.PostTypeShare)}, "share_type": {string(model.ShareTypeRecommend)},
		"category": {string(model.PostCategoryFood)}, "canteen_code": {fixture.Canteen.Code},
		"cuisine": {fixture.Cuisine.Name}, "flavors": {fixture.Flavors[0].Name},
		"tags": {"筛选甲"}, "min_price": {"10.00"}, "max_price": {"10.00"},
	}, []uint64{first.ID}, []uint64{second.ID, third.ID})

	values := url.Values{"tags": {groupTag}, "sort_by": {"latest"}, "limit": {"100"}}
	status, response, _ := performJSON(
		t, engine, http.MethodGet, "/api/v2/posts?"+values.Encode(), nil, author.Token,
	)
	require.Equal(t, http.StatusOK, status)
	var latest service.PostFeedList
	decodeData(t, response, &latest)
	require.Equal(t, []uint64{third.ID, second.ID, first.ID}, postListIDs(latest.Posts),
		"created_at 相同时必须用 id DESC 稳定排序")

	values.Set("sort_by", "price")
	status, response, _ = performJSON(t, engine, http.MethodGet, "/api/v2/posts?"+values.Encode(), nil, author.Token)
	require.Equal(t, http.StatusOK, status)
	var byPrice service.PostFeedList
	decodeData(t, response, &byPrice)
	require.Equal(t, []uint64{first.ID, second.ID, third.ID}, postListIDs(byPrice.Posts),
		"价格排序应升序且空价格稳定置后")

	for _, sortBy := range []string{"hot", "trending"} {
		values.Set("sort_by", sortBy)
		status, response, _ = performJSON(
			t, engine, http.MethodGet, "/api/v2/posts?"+values.Encode(), nil, author.Token,
		)
		require.Equal(t, http.StatusOK, status)
		var sorted service.PostFeedList
		decodeData(t, response, &sorted)
		require.Equal(t, []uint64{third.ID, second.ID, first.ID}, postListIDs(sorted.Posts),
			"sort_by=%s 在分数与时间相同时必须按 id DESC 稳定排序", sortBy)
	}

	values = url.Values{"tags": {groupTag}, "sort_by": {"hot"}, "page": {"2"}, "limit": {"2"}}
	status, response, _ = performJSON(t, engine, http.MethodGet, "/api/v2/posts?"+values.Encode(), nil, author.Token)
	require.Equal(t, http.StatusOK, status)
	var secondPage offsetPostFeed
	decodeData(t, response, &secondPage)
	require.Equal(t, []uint64{first.ID}, postListIDs(secondPage.Posts))
	require.EqualValues(t, 3, secondPage.Pagination.Total)
	require.Equal(t, 2, secondPage.Pagination.TotalPages)

	for _, path := range []string{
		"/api/v2/posts?sort_by=unknown",
		"/api/v2/posts?page=0",
		"/api/v2/posts?limit=101",
		"/api/v2/posts?min_price=20.00&max_price=10.00",
	} {
		status, response, _ = performJSON(t, engine, http.MethodGet, path, nil, author.Token)
		require.Equal(t, http.StatusUnprocessableEntity, status, "path=%s", path)
		require.Equal(t, apierr.BizValidation, response.ErrorCode)
	}

	departed := fixtures.CreateUser()
	departedPost := fixtures.CreatePost(departed.ID,
		testutil.WithPostTitle("已注销作者帖子"), testutil.WithPostTags("注销作者帖"))
	deletedAt := time.Now().UTC()
	require.NoError(t, gdb.Model(&model.User{}).Where("id = ?", departed.ID).
		UpdateColumn("deleted_at", deletedAt).Error)
	status, response, _ = performJSON(t, engine, http.MethodGet,
		"/api/v2/posts?tags="+url.QueryEscape("注销作者帖"), nil, author.Token)
	require.Equal(t, http.StatusOK, status)
	var departedList service.PostFeedList
	decodeData(t, response, &departedList)
	require.Len(t, departedList.Posts, 1)
	require.Equal(t, departedPost.Post.ID, departedList.Posts[0].ID)
	require.Equal(t, "已注销用户", departedList.Posts[0].Author.Name)
	require.Nil(t, departedList.Posts[0].Author.AvatarURL)
}

func testPostTextModeration(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	author service.AuthResult,
	other service.AuthResult,
	fixture postFixture,
) {
	t.Helper()
	draftPayload := sharePostPayload(fixture, "作者草稿可见", []string{"草稿可见"})
	draftPayload["status"] = model.PostStatusDraft
	delete(draftPayload, "share_type")
	draft := createPost(t, engine, author.Token, draftPayload)
	require.Equal(t, model.PostStatusDraft, draft.Status)
	assertPrivatePostVisibility(t, engine, draft.ID, author.Token, other.Token)
	assertPostAbsentFromList(t, engine, author.Token, "草稿可见", draft.ID)

	review := createPost(t, engine, author.Token,
		sharePostPayload(fixture, "需人工审核帖子", []string{"待审可见"}))
	require.Equal(t, model.PostStatusPending, review.Status)
	assertPrivatePostVisibility(t, engine, review.ID, author.Token, other.Token)
	assertPostAbsentFromList(t, engine, author.Token, "待审可见", review.ID)
	assertPostModeration(t, gdb, review.ID, model.ModerationVerdictReview, []string{"manual_review"})

	blocked := createPost(t, engine, author.Token,
		sharePostPayload(fixture, "违规拦截帖子", []string{"驳回可见"}))
	require.Equal(t, model.PostStatusRejected, blocked.Status)
	assertPrivatePostVisibility(t, engine, blocked.ID, author.Token, other.Token)
	assertPostAbsentFromList(t, engine, author.Token, "驳回可见", blocked.ID)
	assertPostModeration(t, gdb, blocked.ID, model.ModerationVerdictBlock, []string{"blocked"})

	edited := createPost(t, engine, author.Token,
		sharePostPayload(fixture, "编辑前已发布帖子", []string{"编辑送审"}))
	payload := sharePostPayload(fixture, "编辑后违规标题", []string{"编辑送审"})
	payload["edit_reason"] = "验证编辑重新送审"
	status, response, _ := performJSON(
		t, engine, http.MethodPut, postPath(edited.ID), payload, author.Token,
	)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
	var result service.PostCreateResult
	decodeData(t, response, &result)
	require.Equal(t, model.PostStatusRejected, result.Status)
	var stored model.Post
	require.NoError(t, gdb.First(&stored, edited.ID).Error)
	require.Equal(t, model.PostStatusRejected, stored.Status)
	require.Equal(t, "编辑后违规标题", stored.Title)
	var histories []model.PostHistory
	require.NoError(t, gdb.Where("post_id = ?", edited.ID).Order("revision").Find(&histories).Error)
	require.Equal(t, []int32{1, 2}, postRevisions(histories))
	var records []model.ModerationRecord
	require.NoError(t, gdb.Where("post_id = ?", edited.ID).Order("id").Find(&records).Error)
	require.Len(t, records, 2)
	require.Equal(t, histories[1].ID, *records[1].PostHistoryID)
	require.Equal(t, model.ModerationVerdictBlock, records[1].Verdict)
	require.Equal(t, []string{"edited_block"}, []string(records[1].Labels))
	assertPrivatePostVisibility(t, engine, edited.ID, author.Token, other.Token)
}

func testPostImageModeration(
	t *testing.T,
	h *testutil.Harness,
	author service.AuthResult,
	fixture postFixture,
) {
	t.Helper()
	moderationService := service.NewModerationService(
		service.DiscardModerationAlerter{}, service.DiscardImageAccessController{},
	)

	passImage := completePostImage(t, h, author.Token, 2048)
	passPayload := sharePostPayload(fixture, "图片待审后通过", []string{"图片通过"})
	passPayload["images"] = []string{passImage.PublicURL}
	passPost := createPost(t, h.Engine, author.Token, passPayload)
	require.Equal(t, model.PostStatusPending, passPost.Status)
	passResult := triggerPostImageCallback(
		t, h, moderationService, "post-image-pass", model.ModerationVerdictPass,
	)
	require.False(t, passResult.Duplicate)
	require.EqualValues(t, 1, passResult.ApprovedPosts)
	assertStoredPostStatus(t, h.Database.GORM, passPost.ID, model.PostStatusApproved)

	blockImage := completePostImage(t, h, author.Token, 3072)
	blockPayload := sharePostPayload(fixture, "图片待审后拦截", []string{"图片拦截"})
	blockPayload["images"] = []string{blockImage.PublicURL}
	blockPost := createPost(t, h.Engine, author.Token, blockPayload)
	require.Equal(t, model.PostStatusPending, blockPost.Status)
	blockResult := triggerPostImageCallback(
		t, h, moderationService, "post-image-block", model.ModerationVerdictBlock,
	)
	require.False(t, blockResult.Duplicate)
	require.Zero(t, blockResult.ApprovedPosts)
	assertStoredPostStatus(t, h.Database.GORM, blockPost.ID, model.PostStatusPending)
	var storedImage model.ImageAsset
	require.NoError(t, h.Database.GORM.First(&storedImage, blockImage.UploadID).Error)
	require.Equal(t, model.ModerationStatusBlock, storedImage.Moderation)

	earlyImage := completePostImage(t, h, author.Token, 4096)
	earlyResult := triggerPostImageCallback(
		t, h, moderationService, "post-image-early", model.ModerationVerdictPass,
	)
	require.Zero(t, earlyResult.ApprovedPosts)
	earlyPayload := sharePostPayload(fixture, "图片回调早于发帖", []string{"图片早回调"})
	earlyPayload["images"] = []string{earlyImage.PublicURL}
	earlyPost := createPost(t, h.Engine, author.Token, earlyPayload)
	require.Equal(t, model.PostStatusApproved, earlyPost.Status)

	duplicateImage := completePostImage(t, h, author.Token, 5120)
	duplicatePayload := sharePostPayload(fixture, "图片回调并发去重", []string{"图片去重"})
	duplicatePayload["images"] = []string{duplicateImage.PublicURL}
	duplicatePost := createPost(t, h.Engine, author.Token, duplicatePayload)
	require.Equal(t, model.PostStatusPending, duplicatePost.Status)

	type callbackResult struct {
		result *service.ImageModerationApplyResult
		err    error
	}
	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	results := make(chan callbackResult, 2)
	for range 2 {
		go func() {
			ready <- struct{}{}
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var applied *service.ImageModerationApplyResult
			err := h.Database.DB.RunInTx(ctx, func(txCtx context.Context) error {
				var callbackErr error
				applied, callbackErr = h.Moderation.TriggerImageCallback(
					txCtx, "post-image-duplicate", model.ModerationVerdictPass,
					moderationService.ApplyImageCallback,
				)
				return callbackErr
			})
			results <- callbackResult{result: applied, err: err}
		}()
	}
	<-ready
	<-ready
	close(start)
	duplicateCount, appliedCount := 0, 0
	for range 2 {
		outcome := <-results
		require.NoError(t, outcome.err)
		if outcome.result.Duplicate {
			duplicateCount++
		}
		appliedCount += int(outcome.result.ApprovedPosts)
	}
	require.Equal(t, 1, duplicateCount)
	require.Equal(t, 1, appliedCount)
	assertStoredPostStatus(t, h.Database.GORM, duplicatePost.ID, model.PostStatusApproved)

	serial := triggerPostImageCallback(
		t, h, moderationService, "post-image-duplicate", model.ModerationVerdictPass,
	)
	require.True(t, serial.Duplicate)
	require.Zero(t, serial.ApprovedPosts)
	var recordCount int64
	require.NoError(t, h.Database.GORM.Model(&model.ModerationRecord{}).
		Where("image_asset_id = ? AND provider_job_id = ?", duplicateImage.UploadID, "post-image-duplicate").
		Count(&recordCount).Error)
	require.EqualValues(t, 1, recordCount)
	h.Moderation.RequireCallbackOrder(
		t,
		"post-image-pass", "post-image-block", "post-image-early",
		"post-image-duplicate", "post-image-duplicate", "post-image-duplicate",
	)
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

	status, response, _ := performJSON(t, engine, http.MethodDelete, postPath(post.ID), nil, "")
	require.Equal(t, http.StatusUnauthorized, status)
	require.Equal(t, apierr.BizUnauthorized, response.ErrorCode)

	status, _, _ = performJSON(t, engine, http.MethodDelete, postPath(post.ID), nil, author.Token)
	require.Equal(t, http.StatusOK, status)
	var stored model.Post
	require.NoError(t, gdb.First(&stored, post.ID).Error)
	require.NotNil(t, stored.DeletedAt)
	require.Equal(t, model.DeleteReasonAuthor, *stored.DeletedReason)
	require.NoError(t, gdb.First(&asset, asset.ID).Error)
	require.Equal(t, model.ImageStatusRetired, asset.Status)

	status, response, _ = performJSON(t, engine, http.MethodGet, postPath(post.ID), nil, author.Token)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizPostDeleted, response.ErrorCode)
	status, response, _ = performJSON(t, engine, http.MethodDelete, postPath(post.ID), nil, author.Token)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizPostDeleted, response.ErrorCode)

	status, response, _ = performJSON(
		t, engine, http.MethodGet, "/api/v2/posts?tags="+url.QueryEscape("删除"), nil, author.Token,
	)
	require.Equal(t, http.StatusOK, status)
	var list service.PostFeedList
	decodeData(t, response, &list)
	require.NotContains(t, postListIDs(list.Posts), post.ID)

	status, response, _ = performJSON(
		t, engine, http.MethodGet,
		"/api/v2/search/posts?q="+url.QueryEscape("软删除帖子"), nil, author.Token,
	)
	require.Equal(t, http.StatusOK, status)
	var search service.SearchPostList
	decodeData(t, response, &search)
	for _, item := range search.Posts {
		require.NotEqual(t, post.ID, item.ID)
	}

	for _, suffix := range []string{"/like", "/favorite"} {
		status, response, _ = performJSON(
			t, engine, http.MethodPost, postPath(post.ID)+suffix, nil, author.Token,
		)
		require.Equal(t, http.StatusNotFound, status)
		require.Equal(t, apierr.BizPostDeleted, response.ErrorCode)
	}
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

func loadPostFixture(t *testing.T, gdb *gorm.DB) postFixture {
	t.Helper()
	dictionaries := testutil.NewFixtures(t, gdb).CreateDictionaries()
	return postFixture{
		Canteen: dictionaries.Canteen,
		Window:  dictionaries.Window,
		Cuisine: dictionaries.Cuisine,
		Flavors: dictionaries.Flavors,
	}
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
		"email": email, "password": "password-123", "verification_code": capturedCode(t, sender, email),
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

func seekingPostPayload(fixture postFixture, title string) map[string]any {
	return map[string]any{
		"post_type": model.PostTypeSeeking, "status": model.PostStatusApproved,
		"title": title, "content": "提问帖集成测试正文", "category": model.PostCategoryFood,
		"canteen_code": fixture.Canteen.Code, "canteen_window_id": fixture.Window.ID,
		"cuisine": fixture.Cuisine.Name, "tags": []string{"求推荐"}, "images": []string{},
		"budget_range": map[string]any{"min": 10, "max": 30},
		"preferences": map[string]any{
			"prefer_flavors": []string{fixture.Flavors[0].Name},
			"avoid_flavors":  []string{fixture.Flavors[1].Name},
		},
	}
}

func requireFieldError(
	t *testing.T,
	status int,
	response testAPIResponse,
	field string,
	code apierr.FieldCode,
) {
	t.Helper()
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, apierr.BizValidation, response.ErrorCode)
	var data struct {
		Errors []apierr.FieldError `json:"errors"`
	}
	decodeData(t, response, &data)
	require.NotEmpty(t, data.Errors)
	for _, item := range data.Errors {
		if item.Field == field {
			require.Equal(t, code, item.Code)
			return
		}
	}
	require.Failf(t, "缺少字段错误", "field=%s errors=%+v", field, data.Errors)
}

func postRevisions(histories []model.PostHistory) []int32 {
	revisions := make([]int32, 0, len(histories))
	for _, history := range histories {
		revisions = append(revisions, history.Revision)
	}
	return revisions
}

func postListIDs(posts []service.PostListItem) []uint64 {
	ids := make([]uint64, 0, len(posts))
	for _, post := range posts {
		ids = append(ids, post.ID)
	}
	return ids
}

func assertPrivatePostVisibility(
	t *testing.T,
	engine *server.Hertz,
	postID uint64,
	authorToken string,
	otherToken string,
) {
	t.Helper()
	status, response, _ := performJSON(t, engine, http.MethodGet, postPath(postID), nil, authorToken)
	require.Equal(t, http.StatusOK, status, "作者应能查看自己的非公开帖子")
	var detail service.PostDetail
	decodeData(t, response, &detail)
	require.Equal(t, postID, detail.ID)
	status, response, _ = performJSON(t, engine, http.MethodGet, postPath(postID), nil, otherToken)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizPostNotPublished, response.ErrorCode)
}

func assertPostAbsentFromList(
	t *testing.T,
	engine *server.Hertz,
	token string,
	tag string,
	postID uint64,
) {
	t.Helper()
	status, response, _ := performJSON(
		t, engine, http.MethodGet, "/api/v2/posts?tags="+url.QueryEscape(tag), nil, token,
	)
	require.Equal(t, http.StatusOK, status)
	var list service.PostFeedList
	decodeData(t, response, &list)
	require.NotContains(t, postListIDs(list.Posts), postID)
}

func assertPostModeration(
	t *testing.T,
	gdb *gorm.DB,
	postID uint64,
	verdict model.ModerationVerdict,
	labels []string,
) {
	t.Helper()
	var record model.ModerationRecord
	require.NoError(t, gdb.Where("post_id = ?", postID).Order("id DESC").First(&record).Error)
	require.Equal(t, verdict, record.Verdict)
	require.Equal(t, labels, []string(record.Labels))
	require.NotNil(t, record.PostHistoryID)
}

func completePostImage(
	t *testing.T,
	h *testutil.Harness,
	token string,
	size int64,
) service.UploadCompleteResult {
	t.Helper()
	presign := presignImage(t, h.Engine, token, size)
	status, response, _ := performJSON(
		t, h.Engine, http.MethodPost,
		fmt.Sprintf("/api/v2/uploads/%d/complete", presign.UploadID), nil, token,
	)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
	var completed service.UploadCompleteResult
	decodeData(t, response, &completed)
	require.Equal(t, model.ImageStatusPending, completed.Status)
	return completed
}

func triggerPostImageCallback(
	t *testing.T,
	h *testutil.Harness,
	moderationService *service.ModerationService,
	jobID string,
	verdict model.ModerationVerdict,
) *service.ImageModerationApplyResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var applied *service.ImageModerationApplyResult
	err := h.Database.DB.RunInTx(ctx, func(txCtx context.Context) error {
		var callbackErr error
		applied, callbackErr = h.Moderation.TriggerImageCallback(
			txCtx, jobID, verdict, moderationService.ApplyImageCallback,
		)
		return callbackErr
	})
	require.NoError(t, err)
	require.NotNil(t, applied)
	return applied
}

func assertStoredPostStatus(
	t *testing.T,
	gdb *gorm.DB,
	postID uint64,
	status model.PostStatus,
) {
	t.Helper()
	var stored model.Post
	require.NoError(t, gdb.First(&stored, postID).Error)
	require.Equal(t, status, stored.Status)
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
