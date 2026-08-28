package router_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	hertzconfig "github.com/cloudwego/hertz/pkg/common/config"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	appconfig "github.com/Foodan-Dev/danshi-backend/internal/config"
	dbinfra "github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/router"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

const moderationCallbackToken = "integration-callback-token"

type fakeImageStorage = testutil.MockImageStorage

func newFakeImageStorage() *fakeImageStorage {
	storage := testutil.NewMockImageStorage()
	storage.SetAutoMaterialize(true)
	storage.SetUploadURLBase("https://cos.example.test/")
	storage.SetPublicURLBase("https://img.example.test/")
	return storage
}

func lastPresign(t *testing.T, storage *fakeImageStorage) service.StoragePresignRequest {
	t.Helper()
	request, ok := storage.LastPresign()
	require.True(t, ok, "预期至少一次 presign 调用")
	return request
}

func fixedAsyncImageModerator(jobID string) *testutil.MockModeration {
	mock := testutil.NewMockModeration()
	outcome := testutil.ImagePending(jobID)
	outcome.Submission.Provider = model.ModerationProviderTencentCI
	mock.SetDefaultImage(outcome)
	return mock
}

func TestUploadModerationAgainstPostgres(t *testing.T) {
	gdb, database := openAuthPostgres(t)
	storage := newFakeImageStorage()
	sender := newCaptureEmailSender()
	cfg := uploadModerationTestConfig()
	imageModerator := testutil.NewMockModeration()
	imageModerator.ProgramImage(
		testutil.ImageModerationRule{Call: 1, Outcome: tencentPending("ci-md5-job")},
		testutil.ImageModerationRule{Call: 2, Outcome: tencentPending("ci-image-pass-job")},
		testutil.ImageModerationRule{Call: 3, Outcome: tencentPending("ci-image-block-job")},
		testutil.ImageModerationRule{Call: 4, Outcome: tencentPending("ci-image-acl-retry-job")},
		testutil.ImageModerationRule{Call: 5, Outcome: tencentPending("ci-image-early-job")},
	)
	engine := uploadModerationEngine(cfg, database, sender, storage, imageModerator)
	author := registerPostTestUser(
		t, engine, sender, "upload-moderation@fdueat.com", "上传审核用户",
	)
	fixture := loadPostFixture(t, gdb)

	t.Run("upload and moderation route inventory", func(t *testing.T) {
		testUploadModerationRouteInventory(t, engine)
	})

	t.Run("content md5 is mandatory and signed", func(t *testing.T) {
		testUploadContentMD5(t, engine, storage, author.Token)
	})

	t.Run("pending uploads coexist while completed public URLs stay unique", func(t *testing.T) {
		testUploadPublicURLPartialUniqueness(t, engine, gdb, sender, author)
	})

	t.Run("pending image callback approves post exactly once", func(t *testing.T) {
		testImageCallbackAccessControl(t, engine, gdb, sender, storage, author, fixture)
	})

	t.Run("image callback before post creation publishes immediately", func(t *testing.T) {
		testImageCallbackBeforePostCreation(t, engine, gdb, author, fixture)
	})

	t.Run("review queue and admin user detail use signed image URLs", func(t *testing.T) {
		testSignedModerationQueueAndAdminUser(
			t, engine, gdb, sender, storage, imageModerator,
		)
	})

	t.Run("post-level queue groups all objects and applies one decision", func(t *testing.T) {
		testPostLevelUnifiedModeration(t, engine, gdb, database, sender, storage, author)
	})

	t.Run("complete returns synchronous verdict and omits blocked public URL", func(t *testing.T) {
		testUploadCompleteReturnsModeration(t, engine, gdb, imageModerator, author)
	})

	t.Run("upload validation ownership duplicate and expiry", func(t *testing.T) {
		testUploadBoundaries(t, engine, gdb, database, cfg, storage, imageModerator, author)
	})

	t.Run("storage and moderation failures roll back upload state", func(t *testing.T) {
		testUploadDependencyFailures(t, engine, gdb, database, storage, imageModerator, author)
	})

	t.Run("callback token payload and missing asset validation", func(t *testing.T) {
		testCallbackValidation(t, engine, gdb, database, sender, storage, imageModerator, cfg)
	})

	t.Run("duplicate callbacks serialize and remain idempotent", func(t *testing.T) {
		testConcurrentImageCallbacks(t, gdb, database, author.User.ID)
	})

	t.Run("independent image callbacks may arrive out of order", func(t *testing.T) {
		testOutOfOrderImageCallbacks(t, gdb, database, author.User.ID)
	})

	t.Run("complete loses expiry race without resurrecting asset", func(t *testing.T) {
		testCompleteExpiryRace(t, database, storage, author.User.ID)
	})

	t.Run("expiry workers partition batches and isolate object failures", func(t *testing.T) {
		testConcurrentExpiryWorkers(t, gdb, database, storage, author.User.ID)
	})

	t.Run("manual review appends and supersedes machine record", func(t *testing.T) {
		testManualReviewIsAppendOnly(t, gdb, database, author.User.ID)
	})
}

func TestFailedTencentImageCallbackAgainstPostgres(t *testing.T) {
	gdb, database := openAuthPostgres(t)
	storage := newFakeImageStorage()
	sender := newCaptureEmailSender()
	engine := uploadModerationEngine(
		uploadModerationTestConfig(), database, sender, storage,
		fixedAsyncImageModerator("unused-job"),
	)
	asset := createModerationAsset(t, gdb, nil, "provider-failed")
	callbackBody := map[string]any{
		"EventName": "ReviewImage",
		"JobsDetail": map[string]any{
			"Code": "InvalidImage", "Message": "image width and height are too small",
			"JobId": "ci-provider-failed-job", "State": "Failed",
			"Object": asset.ObjectKey, "DataId": fmt.Sprintf("image_asset:%d", asset.ID),
		},
	}
	callbackPath := "/api/v2/moderation/tencent-ci/callback?token=" +
		url.QueryEscape(moderationCallbackToken)

	status, response, _ := performJSON(
		t, engine, http.MethodPost, callbackPath, callbackBody, "",
	)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
	var applied service.ImageModerationApplyResult
	decodeData(t, response, &applied)
	require.False(t, applied.Duplicate)

	require.NoError(t, gdb.First(&asset, asset.ID).Error)
	require.Equal(t, model.ModerationStatusReview, asset.Moderation,
		"腾讯 CI 失败回调必须结束 pending 并进入人工复核")
	var record model.ModerationRecord
	require.NoError(t, gdb.Where(
		"provider = ? AND provider_job_id = ?",
		model.ModerationProviderTencentCI, "ci-provider-failed-job",
	).First(&record).Error)
	require.Equal(t, model.ModerationVerdictReview, record.Verdict)
	require.Equal(t, pq.StringArray{"provider_failed"}, record.Labels)
	require.JSONEq(t, `{
  "EventName":"ReviewImage",
  "JobsDetail":{
    "Code":"InvalidImage","Message":"image width and height are too small",
    "JobId":"ci-provider-failed-job","State":"Failed",
    "Object":"`+asset.ObjectKey+`","DataId":"image_asset:`+fmt.Sprint(asset.ID)+`"
  }
}`, string(record.RawResponse))

	status, response, _ = performJSON(
		t, engine, http.MethodPost, callbackPath, callbackBody, "",
	)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
	decodeData(t, response, &applied)
	require.True(t, applied.Duplicate, "腾讯重试失败回调时必须幂等")
}

func testPostLevelUnifiedModeration(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	database *dbinfra.DB,
	sender *captureEmailSender,
	storage *fakeImageStorage,
	author service.AuthResult,
) {
	t.Helper()
	reviewer := registerPostTestUser(
		t, engine, sender, "post-level-reviewer@fdueat.com", "整帖复核管理员",
	)
	require.NoError(t, gdb.Create(&model.UserRoleBinding{
		UserID: reviewer.User.ID, Role: model.UserRoleModerator, GrantedAt: time.Now().UTC(),
	}).Error)

	type unifiedFixture struct {
		post           model.Post
		text           model.ModerationRecord
		images         []model.ImageAsset
		imageRecords   []model.ModerationRecord
		pendingRecords []model.ModerationRecord
	}
	createFixture := func(
		title string,
		textVerdict model.ModerationVerdict,
		imageVerdicts ...model.ModerationVerdict,
	) unifiedFixture {
		shareType := model.ShareTypeRecommend
		postStatus := model.PostStatusApproved
		switch textVerdict {
		case model.ModerationVerdictBlock:
			postStatus = model.PostStatusRejected
		case model.ModerationVerdictReview:
			postStatus = model.PostStatusPending
		}
		for _, verdict := range imageVerdicts {
			if verdict == model.ModerationVerdictBlock {
				postStatus = model.PostStatusRejected
				break
			}
			if verdict == model.ModerationVerdictReview && postStatus != model.PostStatusRejected {
				postStatus = model.PostStatusPending
			}
		}
		fixture := unifiedFixture{post: model.Post{
			AuthorID: author.User.ID, PostType: model.PostTypeShare, ShareType: &shareType,
			Status: postStatus, Category: model.PostCategoryFood,
			Title: title, Content: title + " 的正文",
		}}
		require.NoError(t, gdb.Create(&fixture.post).Error)
		textScore := decimal.NewFromInt(62)
		contentRevision := int32(1)
		fixture.text = model.ModerationRecord{
			PostID: &fixture.post.ID, ContentRevision: &contentRevision, Scene: model.ModerationSceneText,
			Provider: "batch38_text", Verdict: textVerdict,
			Labels: pq.StringArray{"text_" + string(textVerdict)}, Score: &textScore,
			CreatedAt: time.Now().UTC(),
		}
		require.NoError(t, gdb.Create(&fixture.text).Error)
		if textVerdict == model.ModerationVerdictReview {
			fixture.pendingRecords = append(fixture.pendingRecords, fixture.text)
		}
		postImages := make([]model.PostImage, 0, len(imageVerdicts))
		for position, verdict := range imageVerdicts {
			size := int64(2048 + position)
			objectKey := fmt.Sprintf("posts/batch38/%s-%d.jpg", title, position)
			publicURL := "https://img.example.test/" + objectKey
			asset := model.ImageAsset{
				UploaderID: &author.User.ID, Purpose: model.ImagePurposePost,
				ObjectKey: objectKey, PublicURL: publicURL, ContentType: "image/jpeg",
				Size: &size, Status: model.ImageStatusPending,
				Moderation: model.ModerationStatus(verdict),
			}
			require.NoError(t, gdb.Create(&asset).Error)
			storage.PutObject(objectKey, testutil.StoredObject{ContentLength: size})
			storage.SetPublicURL(objectKey, publicURL)
			if verdict != model.ModerationVerdictPass {
				require.NoError(t, storage.SetObjectPublicAccess(context.Background(), objectKey, false))
			}
			score := decimal.NewFromInt(int64(90 - position*10))
			jobID := fmt.Sprintf("batch38-%s-%d", title, position)
			record := model.ModerationRecord{
				ImageAssetID: &asset.ID, Scene: model.ModerationSceneImage,
				Provider: "batch38_image", ProviderJobID: &jobID, Verdict: verdict,
				Labels: pq.StringArray{"image_" + string(verdict)}, Score: &score,
				CreatedAt: time.Now().UTC().Add(time.Duration(position+1) * time.Microsecond),
			}
			require.NoError(t, gdb.Create(&record).Error)
			fixture.images = append(fixture.images, asset)
			fixture.imageRecords = append(fixture.imageRecords, record)
			if verdict == model.ModerationVerdictReview {
				fixture.pendingRecords = append(fixture.pendingRecords, record)
			}
			postImages = append(postImages, model.PostImage{
				PostID: fixture.post.ID, Position: int16(position), ImageAssetID: asset.ID,
			})
		}
		if len(postImages) > 0 {
			require.NoError(t, gdb.Create(&postImages).Error)
		}
		return fixture
	}

	approved := createFixture(
		"batch38-approve", model.ModerationVerdictPass,
		model.ModerationVerdictPass,
		model.ModerationVerdictReview,
		model.ModerationVerdictReview,
	)
	status, response, _ := performJSON(t, engine, http.MethodGet,
		"/api/v2/admin/moderation-records/pending?limit=100", nil, reviewer.Token)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
	var queue service.AdminModerationList
	decodeData(t, response, &queue)
	items := moderationItemsForPost(queue.Records, approved.post.ID)
	require.Len(t, items, 1, "同帖正文和多图只能形成一个队列条目")
	require.NotNil(t, items[0].Post)
	require.Equal(t, approved.post.Title, items[0].Post.Text.Title)
	require.Equal(t, model.ModerationStatusPass, items[0].Post.Text.Moderation)
	require.Equal(t, approved.text.ID, items[0].Post.Text.Machine.ModerationRecordID)
	require.Len(t, items[0].Post.Images, 3)
	require.Equal(t, model.ModerationStatusPass, items[0].Post.Images[0].Moderation)
	require.Equal(t, model.ModerationStatusReview, items[0].Post.Images[1].Moderation)
	require.Equal(t, model.ModerationStatusReview, items[0].Post.Images[2].Moderation)
	for index := range items[0].Post.Images {
		_, readErr := storage.ReadPresignedURL(items[0].Post.Images[index].ImageURL)
		require.NoError(t, readErr, "联合队列中的每张图都必须使用可读短期签名地址")
		require.NotNil(t, items[0].Post.Images[index].Machine)
	}

	status, response, _ = performJSON(t, engine, http.MethodPut,
		fmt.Sprintf("/api/v2/admin/posts/%d/review", approved.post.ID),
		map[string]any{"status": model.PostStatusApproved, "feedback": "整帖确认通过"}, reviewer.Token)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
	var approvedResult service.AdminPostReviewResult
	decodeData(t, response, &approvedResult)
	require.Equal(t, model.PostStatusApproved, approvedResult.Status)
	require.Len(t, approvedResult.ModerationRecordIDs, len(approved.pendingRecords))
	requireUnifiedManualRecords(
		t, gdb, approved.pendingRecords, model.ModerationVerdictPass, reviewer.User.ID,
	)
	for index := 1; index < len(approved.images); index++ {
		var delivery model.ImageAccessDelivery
		require.NoError(t, gdb.First(&delivery, approved.images[index].ID).Error)
		require.True(t, delivery.DesiredPublic)
		require.True(t, delivery.PurgeRequired,
			"review 转公开时必须刷新可能已缓存的私有响应")
	}

	rejected := createFixture(
		"batch38-reject", model.ModerationVerdictReview,
		model.ModerationVerdictReview, model.ModerationVerdictReview,
	)
	status, response, _ = performJSON(t, engine, http.MethodPut,
		fmt.Sprintf("/api/v2/admin/posts/%d/review", rejected.post.ID),
		map[string]any{"status": model.PostStatusRejected, "feedback": "整帖驳回"}, reviewer.Token)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
	var rejectedResult service.AdminPostReviewResult
	decodeData(t, response, &rejectedResult)
	require.Equal(t, model.PostStatusRejected, rejectedResult.Status)
	require.Len(t, rejectedResult.ModerationRecordIDs, len(rejected.pendingRecords))
	requireUnifiedManualRecords(
		t, gdb, rejected.pendingRecords, model.ModerationVerdictBlock, reviewer.User.ID,
	)
	for index := range rejected.images {
		var image model.ImageAsset
		require.NoError(t, gdb.First(&image, rejected.images[index].ID).Error)
		require.Equal(t, model.ModerationStatusBlock, image.Moderation)
		var delivery model.ImageAccessDelivery
		require.NoError(t, gdb.First(&delivery, image.ID).Error)
		require.False(t, delivery.DesiredPublic)
		require.True(t, delivery.PurgeRequired)
		var intent model.ImageAccessIntent
		require.NoError(t, gdb.First(&intent, delivery.DesiredIntentID).Error)
		var source model.ModerationRecord
		require.NoError(t, gdb.First(&source, intent.SourceModerationRecordID).Error)
		require.Equal(t, model.ModerationProviderManual, source.Provider,
			"整帖联合审核判 block 仍必须沿用 manual supersedes 流水")
	}

	blocked := createFixture("batch38-block-priority", model.ModerationVerdictReview)
	size := int64(4096)
	blockedImage := model.ImageAsset{
		UploaderID: &author.User.ID, Purpose: model.ImagePurposePost,
		ObjectKey:   "posts/batch38/block-priority.jpg",
		PublicURL:   "https://img.example.test/posts/batch38/block-priority.jpg",
		ContentType: "image/jpeg", Size: &size, Status: model.ImageStatusPending,
		Moderation: model.ModerationStatusPending,
	}
	require.NoError(t, gdb.Create(&blockedImage).Error)
	require.NoError(t, gdb.Create(&model.PostImage{
		PostID: blocked.post.ID, Position: 0, ImageAssetID: blockedImage.ID,
	}).Error)
	moderation := service.NewModerationService(
		service.DiscardModerationAlerter{}, service.DiscardImageAccessController{},
	)
	var applied *service.ImageModerationApplyResult
	err := database.RunInTx(context.Background(), func(ctx context.Context) error {
		var applyErr error
		applied, applyErr = moderation.ApplyImageResult(ctx, service.ImageModerationCallback{
			ImageAssetID: blockedImage.ID, ObjectKey: blockedImage.ObjectKey,
			Provider: "batch38_image", Verdict: model.ModerationVerdictBlock,
			Labels: []string{"image_block"},
		})
		return applyErr
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, applied.RejectedPosts)
	var blockedPost model.Post
	require.NoError(t, gdb.First(&blockedPost, blocked.post.ID).Error)
	require.Equal(t, model.PostStatusRejected, blockedPost.Status,
		"block 图片必须压过 review 正文直接驳回")
	blockedDetail := getAuthorPostDetail(t, engine, blocked.post.ID, author.Token)
	require.NotNil(t, blockedDetail.Moderation)
	foundBlockedImage := false
	for index := range blockedDetail.Moderation.Issues {
		issue := blockedDetail.Moderation.Issues[index]
		if issue.Part == "image" && issue.ImagePosition != nil && *issue.ImagePosition == 0 {
			foundBlockedImage = true
			require.Equal(t, model.ModerationStatusBlock, issue.Moderation)
			require.Equal(t, []string{"image_block"}, issue.Labels)
		}
	}
	require.True(t, foundBlockedImage, "作者审核摘要必须定位到第 1 张 block 图片")
	textBlocked := createFixture(
		"batch38-text-block", model.ModerationVerdictBlock,
		model.ModerationVerdictPass, model.ModerationVerdictPass,
	)
	require.Equal(t, model.PostStatusRejected, textBlocked.post.Status,
		"正文 block 且图片全 pass 时仍必须直接驳回")
	allPassed := createFixture(
		"batch38-all-pass", model.ModerationVerdictPass,
		model.ModerationVerdictPass, model.ModerationVerdictPass,
	)
	require.Equal(t, model.PostStatusApproved, allPassed.post.Status,
		"正文与图片全 pass 时必须直接发布")
	status, response, _ = performJSON(t, engine, http.MethodGet,
		"/api/v2/admin/moderation-records/pending?limit=100", nil, reviewer.Token)
	require.Equal(t, http.StatusOK, status)
	decodeData(t, response, &queue)
	blockedItems := moderationItemsForPost(queue.Records, blocked.post.ID)
	require.Len(t, blockedItems, 1, "图片 block 的帖子必须进入人工复核队列")
	require.Equal(t, model.ModerationVerdictBlock, blockedItems[0].Verdict)
	textBlockedItems := moderationItemsForPost(queue.Records, textBlocked.post.ID)
	require.Len(t, textBlockedItems, 1, "正文 block 的帖子必须进入人工复核队列")
	require.Equal(t, model.ModerationVerdictBlock, textBlockedItems[0].Verdict)
	require.Empty(t, moderationItemsForPost(queue.Records, allPassed.post.ID),
		"全部 pass 的帖子不得进入人工队列")
}

func testUploadCompleteReturnsModeration(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	imageModerator *testutil.MockModeration,
	author service.AuthResult,
) {
	t.Helper()
	nextCall := len(imageModerator.ImageCalls()) + 1
	imageModerator.ProgramImage(
		testutil.ImageModerationRule{
			Call: nextCall, Outcome: testutil.ImageImmediate(model.ModerationVerdictReview),
		},
		testutil.ImageModerationRule{
			Call: nextCall + 1, Outcome: testutil.ImageImmediate(model.ModerationVerdictBlock),
		},
	)
	reviewPresign := presignImage(t, engine, author.Token, 2304)
	status, response, _ := performJSON(t, engine, http.MethodPost,
		fmt.Sprintf("/api/v2/uploads/%d/complete", reviewPresign.UploadID), nil, author.Token)
	require.Equal(t, http.StatusOK, status)
	var reviewed service.UploadCompleteResult
	decodeData(t, response, &reviewed)
	require.Equal(t, model.ImageStatusPending, reviewed.Status)
	require.Equal(t, model.ModerationStatusReview, reviewed.Moderation)
	require.NotEmpty(t, reviewed.PublicURL, "review 图片仍需把稳定资产身份交给发帖请求")

	blockPresign := presignImage(t, engine, author.Token, 2560)
	status, response, raw := performJSON(t, engine, http.MethodPost,
		fmt.Sprintf("/api/v2/uploads/%d/complete", blockPresign.UploadID), nil, author.Token)
	require.Equal(t, http.StatusOK, status)
	var blocked service.UploadCompleteResult
	decodeData(t, response, &blocked)
	require.Equal(t, model.ModerationStatusBlock, blocked.Moderation)
	require.Empty(t, blocked.PublicURL)
	require.NotContains(t, string(raw.Result().Body()), `"public_url"`,
		"block 响应不得返回已经转私有且必然 403 的公开地址")
	var delivery model.ImageAccessDelivery
	require.NoError(t, gdb.First(&delivery, blocked.UploadID).Error)
	require.False(t, delivery.DesiredPublic)
	require.Equal(t, model.ImageAccessPendingACL, delivery.State)
}

func moderationItemsForPost(
	records []service.AdminModerationView,
	postID uint64,
) []service.AdminModerationView {
	items := make([]service.AdminModerationView, 0, 1)
	for index := range records {
		if records[index].TargetType == "post" && records[index].TargetID == postID {
			items = append(items, records[index])
		}
	}
	return items
}

func requireUnifiedManualRecords(
	t *testing.T,
	gdb *gorm.DB,
	machines []model.ModerationRecord,
	verdict model.ModerationVerdict,
	reviewerID uint64,
) {
	t.Helper()
	var sharedReviewedAt *time.Time
	for index := range machines {
		var manual model.ModerationRecord
		require.NoError(t, gdb.Where("supersedes_id = ?", machines[index].ID).First(&manual).Error)
		require.Equal(t, model.ModerationProviderManual, manual.Provider)
		require.Equal(t, verdict, manual.Verdict)
		require.Equal(t, reviewerID, *manual.ReviewerID)
		require.NotNil(t, manual.ReviewedAt)
		if sharedReviewedAt == nil {
			sharedReviewedAt = manual.ReviewedAt
		} else {
			require.True(t, sharedReviewedAt.Equal(*manual.ReviewedAt),
				"同一次整帖决策产生的 manual 行必须共享 reviewed_at")
		}
	}
}

func uploadModerationTestConfig() appconfig.Config {
	cfg := authTestConfig()
	cfg.COSMaxImageBytes = 10 * 1024 * 1024
	cfg.COSPresignTTLS = 600
	cfg.COSPresignGetTTLS = 3600
	cfg.ModerationCallbackToken = moderationCallbackToken
	return cfg
}

func uploadModerationEngine(
	cfg appconfig.Config,
	database *dbinfra.DB,
	sender service.VerificationEmailSender,
	storage service.ImageStorage,
	imageModerator service.ImageModerator,
) *server.Hertz {
	engine := server.New(
		server.WithHandleMethodNotAllowed(true),
		hertzconfig.Option{F: func(_ *hertzconfig.Options) {}},
	)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	deps := router.Deps{
		Config: cfg, DB: database, Log: log, EmailSender: sender,
		ContentModerator: service.DirectPassContentModerator{}, ImageStorage: storage,
		ImageModerator:    imageModerator,
		ModerationAlerter: service.DiscardModerationAlerter{},
	}
	router.Register(engine, deps)
	return engine
}

func testImageCallbackBeforePostCreation(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	author service.AuthResult,
	fixture postFixture,
) {
	t.Helper()
	presign := presignImage(t, engine, author.Token, 3072)
	completePath := fmt.Sprintf("/api/v2/uploads/%d/complete", presign.UploadID)
	status, response, _ := performJSON(
		t, engine, http.MethodPost, completePath, nil, author.Token,
	)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
	var completed service.UploadCompleteResult
	decodeData(t, response, &completed)
	require.Equal(t, model.ImageStatusPending, completed.Status)
	require.Equal(t, model.ModerationStatusPending, completed.Moderation,
		"异步送审的 complete 响应必须明确表示仍在审核中")

	callbackBody := map[string]any{
		"EventName": "ReviewImage",
		"JobsDetail": map[string]any{
			"JobId": "ci-image-early-job", "State": "Success",
			"Object": completed.ObjectKey,
			"DataId": fmt.Sprintf("image_asset:%d", completed.UploadID),
			"Result": 0, "Score": 99,
		},
	}
	callbackPath := "/api/v2/moderation/tencent-ci/callback?token=" +
		url.QueryEscape(moderationCallbackToken)
	status, response, _ = performJSON(
		t, engine, http.MethodPost, callbackPath, callbackBody, "",
	)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
	var applied service.ImageModerationApplyResult
	decodeData(t, response, &applied)
	require.False(t, applied.Duplicate)
	require.Zero(t, applied.ApprovedPosts, "回调早于帖子创建时还没有帖子可补发布")

	var asset model.ImageAsset
	require.NoError(t, gdb.First(&asset, completed.UploadID).Error)
	require.Equal(t, model.ImageStatusPending, asset.Status)
	require.Equal(t, model.ModerationStatusPass, asset.Moderation)
	status, response, _ = performJSON(t, engine, http.MethodGet,
		fmt.Sprintf("/api/v2/uploads/%d", completed.UploadID), nil, author.Token)
	require.Equal(t, http.StatusOK, status)
	var uploadView service.UploadImageView
	decodeData(t, response, &uploadView)
	require.Equal(t, model.ModerationStatusPass, uploadView.Moderation,
		"异步审核完成后上传者必须能通过查询端点取得最终结论")
	payload := sharePostPayload(fixture, "图片先审完再建帖", []string{"早回调"})
	payload["images"] = []string{completed.PublicURL}
	post := createPost(t, engine, author.Token, payload)
	require.Equal(t, model.PostStatusApproved, post.Status)
	require.NoError(t, gdb.First(&asset, completed.UploadID).Error)
	require.Equal(t, model.ImageStatusReady, asset.Status)
}

func tencentPending(jobID string) testutil.ImageModerationOutcome {
	outcome := testutil.ImagePending(jobID)
	outcome.Submission.Provider = model.ModerationProviderTencentCI
	return outcome
}

func testSignedModerationQueueAndAdminUser(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	sender *captureEmailSender,
	storage *fakeImageStorage,
	imageModerator *testutil.MockModeration,
) {
	t.Helper()
	uploader := registerPostTestUser(
		t, engine, sender, "signed-review-uploader@fdueat.com", "签名复核上传者",
	)
	reviewer := registerPostTestUser(
		t, engine, sender, "signed-reviewer@fdueat.com", "签名复核管理员",
	)
	unrelated := registerPostTestUser(
		t, engine, sender, "signed-review-unrelated@fdueat.com", "签名复核无关用户",
	)
	require.NoError(t, gdb.Create(&model.UserRoleBinding{
		UserID: reviewer.User.ID, Role: model.UserRoleModerator, GrantedAt: time.Now().UTC(),
	}).Error)
	jobID := "signed-review-queue-job"
	imageModerator.ProgramImage(testutil.ImageModerationRule{
		Call: len(imageModerator.ImageCalls()) + 1, Outcome: tencentPending(jobID),
	})
	completed := completeImageForAccessTest(t, engine, uploader.Token, 3072)
	callbackPath := "/api/v2/moderation/tencent-ci/callback?token=" +
		url.QueryEscape(moderationCallbackToken)
	status, response, _ := performJSON(t, engine, http.MethodPost, callbackPath,
		tencentCallbackBody(completed.UploadID, jobID, completed.ObjectKey, 2), "")
	require.Equal(t, http.StatusOK, status, "review 回调应成功: %s", response.Message)
	var asset model.ImageAsset
	require.NoError(t, gdb.First(&asset, completed.UploadID).Error)
	require.Equal(t, model.ModerationStatusReview, asset.Moderation)
	_, err := storage.ReadPublicURL(asset.PublicURL)
	require.NoError(t, err, "HTTP 事务只落 durable intent，ACL 由独立 worker 收敛")
	var delivery model.ImageAccessDelivery
	require.NoError(t, gdb.First(&delivery, completed.UploadID).Error)
	require.False(t, delivery.DesiredPublic)
	require.Equal(t, model.ImageAccessPendingACL, delivery.State)
	var record model.ModerationRecord
	require.NoError(t, gdb.Where("provider_job_id = ?", jobID).First(&record).Error)

	size := int64(2048)
	avatarAsset := model.ImageAsset{
		UploaderID: &uploader.User.ID, Purpose: model.ImagePurposeAvatar,
		ObjectKey:   "avatars/signed-review/private-avatar.jpg",
		PublicURL:   "https://img.example.test/avatars/signed-review/private-avatar.jpg",
		ContentType: "image/jpeg", Size: &size, Status: model.ImageStatusPending,
		Moderation: model.ModerationStatusReview,
	}
	require.NoError(t, gdb.Create(&avatarAsset).Error)
	storage.PutObject(avatarAsset.ObjectKey, testutil.StoredObject{ContentLength: size})
	storage.SetPublicURL(avatarAsset.ObjectKey, avatarAsset.PublicURL)
	require.NoError(t, storage.SetObjectPublicAccess(
		context.Background(), avatarAsset.ObjectKey, false,
	))
	require.NoError(t, gdb.Model(&model.User{}).Where("id = ?", uploader.User.ID).
		UpdateColumn("avatar_image_asset_id", avatarAsset.ID).Error)
	avatarJobID := "signed-review-avatar-job"
	avatarRecord := model.ModerationRecord{
		ImageAssetID: &avatarAsset.ID, Scene: model.ModerationSceneImage,
		Provider: model.ModerationProviderTencentCI, ProviderJobID: &avatarJobID,
		Verdict: model.ModerationVerdictReview, Labels: pq.StringArray{"avatar_review"},
		CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, gdb.Create(&avatarRecord).Error)

	signedCallsBefore := len(storage.PresignGetCalls())
	status, response, _ = performJSON(t, engine, http.MethodGet,
		"/api/v2/admin/moderation-records/pending", nil, unrelated.Token)
	require.Equal(t, http.StatusForbidden, status)
	require.Len(t, storage.PresignGetCalls(), signedCallsBefore)
	status, response, _ = performJSON(t, engine, http.MethodGet,
		"/api/v2/admin/moderation-records/pending", nil, reviewer.Token)
	require.Equal(t, http.StatusOK, status)
	var queue service.AdminModerationList
	decodeData(t, response, &queue)
	require.False(t, moderationRecordPresent(queue.Records, record.ID),
		"尚未绑定帖子的 post 图片不能作为单图待办进入人工队列")
	var signedQueueURL string
	for _, item := range queue.Records {
		if item.ID == avatarRecord.ID && item.Content != nil {
			signedQueueURL = *item.Content
		}
	}
	require.NotEmpty(t, signedQueueURL)
	require.NotContains(t, signedQueueURL, "imageMogr2",
		"签名读取地址不得盲目追加图片处理参数")
	_, err = storage.ReadPresignedURL(signedQueueURL)
	require.NoError(t, err, "待人工复核队列应返回可读取私有对象的短期 URL")

	status, response, _ = performJSON(t, engine, http.MethodGet,
		fmt.Sprintf("/api/v2/admin/users/%d", uploader.User.ID), nil, reviewer.Token)
	require.Equal(t, http.StatusOK, status)
	var detail service.AdminUserDetail
	decodeData(t, response, &detail)
	require.NotNil(t, detail.AvatarURL)
	require.NotContains(t, *detail.AvatarURL, "imageMogr2",
		"管理端签名头像不得生成会破坏签名的缩略地址")
	_, err = storage.ReadPresignedURL(*detail.AvatarURL)
	require.NoError(t, err, "管理端单用户详情应签发私有头像读取 URL")

	status, response, _ = performJSON(t, engine, http.MethodGet,
		fmt.Sprintf("/api/v2/users/%d", uploader.User.ID), nil, unrelated.Token)
	require.Equal(t, http.StatusOK, status)
	var publicProfile service.UserProfile
	decodeData(t, response, &publicProfile)
	require.NotNil(t, publicProfile.AvatarURL)
	require.Equal(t, avatarAsset.PublicURL, *publicProfile.AvatarURL,
		"公开详情继续返回 PublicURL，不得接入签名 URL")
	_, err = storage.ReadPublicURL(*publicProfile.AvatarURL)
	require.ErrorIs(t, err, testutil.ErrMockPublicAccessDenied)
}

func testUploadModerationRouteInventory(t *testing.T, engine *server.Hertz) {
	t.Helper()
	operations := make([]string, 0)
	for _, route := range engine.Routes() {
		if strings.HasPrefix(route.Path, "/api/v2/uploads") ||
			strings.HasPrefix(route.Path, "/api/v2/moderation") {
			operations = append(operations, route.Method+" "+route.Path)
		}
	}
	require.ElementsMatch(t, []string{
		"POST /api/v2/uploads/presign",
		"GET /api/v2/uploads/:upload_id",
		"POST /api/v2/uploads/:upload_id/complete",
		"POST /api/v2/moderation/tencent-ci/callback",
	}, operations)
}

func testUploadContentMD5(
	t *testing.T,
	engine *server.Hertz,
	storage *fakeImageStorage,
	token string,
) {
	t.Helper()
	payload := map[string]any{
		"purpose": "post", "content_type": "image/jpeg", "size": 2048,
	}
	status, response, _ := performJSON(
		t, engine, http.MethodPost, "/api/v2/uploads/presign", payload, token,
	)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Contains(t, response.Message, "content_md5")

	payload["content_md5"] = base64.StdEncoding.EncodeToString(make([]byte, 16))
	status, response, _ = performJSON(
		t, engine, http.MethodPost, "/api/v2/uploads/presign", payload, token,
	)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
	request := lastPresign(t, storage)
	require.Equal(t, payload["content_md5"], request.ContentMD5)
	require.EqualValues(t, 2048, request.ContentLength)
	require.Equal(t, "image/jpeg", request.ContentType)
	var presign service.UploadPresignResult
	decodeData(t, response, &presign)
	completePath := fmt.Sprintf("/api/v2/uploads/%d/complete", presign.UploadID)
	status, response, _ = performJSON(t, engine, http.MethodPost, completePath, nil, token)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
}

func testUploadPublicURLPartialUniqueness(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	sender *captureEmailSender,
	author service.AuthResult,
) {
	t.Helper()
	first := presignImage(t, engine, author.Token, 1024)
	second := presignImage(t, engine, author.Token, 2048)

	var sameUserAssets []model.ImageAsset
	require.NoError(t, gdb.Where("id IN ?", []uint64{first.UploadID, second.UploadID}).
		Order("id").Find(&sameUserAssets).Error)
	require.Len(t, sameUserAssets, 2)
	for _, asset := range sameUserAssets {
		require.NotNil(t, asset.UploaderID)
		require.Equal(t, author.User.ID, *asset.UploaderID)
		require.Equal(t, model.ImageStatusPending, asset.Status)
		require.Empty(t, asset.PublicURL)
	}

	other := registerPostTestUser(
		t, engine, sender, "upload-public-url-other@fdueat.com", "上传约束用户",
	)
	authorUpload := presignImage(t, engine, author.Token, 3072)
	otherUpload := presignImage(t, engine, other.Token, 4096)
	expectedUploaders := map[uint64]uint64{
		authorUpload.UploadID: author.User.ID,
		otherUpload.UploadID:  other.User.ID,
	}
	var differentUserAssets []model.ImageAsset
	require.NoError(t, gdb.Where("id IN ?", []uint64{authorUpload.UploadID, otherUpload.UploadID}).
		Find(&differentUserAssets).Error)
	require.Len(t, differentUserAssets, 2)
	for _, asset := range differentUserAssets {
		require.NotNil(t, asset.UploaderID)
		require.Equal(t, expectedUploaders[asset.ID], *asset.UploaderID)
		require.Equal(t, model.ImageStatusPending, asset.Status)
		require.Empty(t, asset.PublicURL)
	}

	const duplicatePublicURL = "https://img.example.test/completed/shared.jpg"
	require.NoError(t, gdb.Model(&model.ImageAsset{}).Where("id = ?", first.UploadID).
		Update("public_url", duplicatePublicURL).Error)
	err := gdb.Model(&model.ImageAsset{}).Where("id = ?", second.UploadID).
		Update("public_url", duplicatePublicURL).Error
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "23505", pgErr.Code)
	require.Equal(t, "uq_image_assets_public_url", pgErr.ConstraintName)

	var rejected model.ImageAsset
	require.NoError(t, gdb.First(&rejected, second.UploadID).Error)
	require.Empty(t, rejected.PublicURL, "重复非空 public_url 被拒后不得污染 pending 资产")
}

func testUploadBoundaries(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	database *dbinfra.DB,
	cfg appconfig.Config,
	storage *fakeImageStorage,
	imageModerator *testutil.MockModeration,
	author service.AuthResult,
) {
	t.Helper()
	validMD5 := base64.StdEncoding.EncodeToString(make([]byte, 16))
	for _, testCase := range []struct {
		name  string
		body  map[string]any
		field string
		code  apierr.FieldCode
	}{
		{
			name:  "invalid purpose",
			body:  map[string]any{"purpose": "cover", "content_type": "image/jpeg", "size": 1, "content_md5": validMD5},
			field: "purpose", code: apierr.FieldInvalidEnum,
		},
		{
			name:  "oversized",
			body:  map[string]any{"purpose": "post", "content_type": "image/jpeg", "size": cfg.COSMaxImageBytes + 1, "content_md5": validMD5},
			field: "size", code: apierr.FieldOutOfRange,
		},
		{
			name:  "missing md5",
			body:  map[string]any{"purpose": "post", "content_type": "image/jpeg", "size": 1},
			field: "content_md5", code: apierr.FieldInvalidFormat,
		},
		{
			name:  "invalid content type",
			body:  map[string]any{"purpose": "post", "content_type": "text/plain", "size": 1, "content_md5": validMD5},
			field: "content_type", code: apierr.FieldInvalidEnum,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			status, response, _ := performJSON(t, engine, http.MethodPost,
				"/api/v2/uploads/presign", testCase.body, author.Token)
			require.Equal(t, http.StatusUnprocessableEntity, status)
			require.Equal(t, apierr.BizValidation, response.ErrorCode)
			requireUploadFieldError(t, response, testCase.field, testCase.code)
		})
	}

	other := testutil.NewFixtures(t, gdb).CreateActor(cfg)
	presign := presignImage(t, engine, author.Token, 1536)
	path := fmt.Sprintf("/api/v2/uploads/%d/complete", presign.UploadID)
	status, response, _ := performJSON(t, engine, http.MethodPost, path, nil, other.Token)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, apierr.BizImageNotOwned, response.ErrorCode)
	var pending model.ImageAsset
	require.NoError(t, gdb.First(&pending, presign.UploadID).Error)
	require.Equal(t, model.ImageStatusPending, pending.Status)

	moderationCalls := len(imageModerator.ImageCalls())
	status, response, _ = performJSON(t, engine, http.MethodPost, path, nil, author.Token)
	require.Equal(t, http.StatusOK, status)
	var completed service.UploadCompleteResult
	decodeData(t, response, &completed)
	require.Equal(t, model.ImageStatusPending, completed.Status)
	status, response, _ = performJSON(t, engine, http.MethodPost, path, nil, author.Token)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, apierr.BizUploadClosed, response.ErrorCode)
	require.Len(t, imageModerator.ImageCalls(), moderationCalls+1,
		"重复 complete 不能再次提交图片审核")

	status, response, _ = performJSON(t, engine, http.MethodPost,
		"/api/v2/uploads/9223372036854775807/complete", nil, author.Token)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizUploadNotFound, response.ErrorCode)

	expired := presignImage(t, engine, author.Token, 2048)
	expiredAt := time.Now().UTC().Add(-2 * time.Hour)
	require.NoError(t, gdb.Model(&model.ImageAsset{}).
		Where("id IN ?", []uint64{completed.UploadID, expired.UploadID}).
		Update("created_at", expiredAt).Error)
	uploads := service.NewUploadService(
		storage, imageModerator, service.NewModerationService(
			service.DiscardModerationAlerter{}, service.DiscardImageAccessController{},
		),
		cfg.COSMaxImageBytes, cfg.COSPresignTTL(), cfg.COSPresignGetTTL(),
	)
	err := database.RunInTx(context.Background(), func(ctx context.Context) error {
		result, expireErr := uploads.ExpirePending(ctx, service.ExpirePendingOptions{
			Before: time.Now().UTC().Add(-time.Hour), Limit: 2,
		})
		if expireErr == nil && (result.Selected != 2 || result.Retired != 2) {
			return fmt.Errorf("期望领取并回收 2 条过期上传，实际 selected=%d retired=%d",
				result.Selected, result.Retired)
		}
		return expireErr
	})
	require.NoError(t, err)
	var completedOrphan model.ImageAsset
	require.NoError(t, gdb.First(&completedOrphan, completed.UploadID).Error)
	require.Equal(t, model.ImageStatusRetired, completedOrphan.Status,
		"complete 后从未被引用的资产必须可被回收")
	require.NotEqual(t, completed.PublicURL, completedOrphan.PublicURL,
		"对象删除后不得保留失效公开 URL")
	require.True(t, model.IsPurgedImageURL(completedOrphan.PublicURL))
	status, response, _ = performJSON(t, engine, http.MethodPost,
		fmt.Sprintf("/api/v2/uploads/%d/complete", expired.UploadID), nil, author.Token)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, apierr.BizUploadClosed, response.ErrorCode)
	var retired model.ImageAsset
	require.NoError(t, gdb.First(&retired, expired.UploadID).Error)
	require.Equal(t, model.ImageStatusRetired, retired.Status)
	require.True(t, model.IsPurgedImageURL(retired.PublicURL),
		"对象删除后必须用唯一墓碑值替换失效公开 URL")
	// 清理本测试资产，避免额外行影响后续独立场景。
	require.NoError(t, gdb.Exec(
		"SELECT danshi_purge_image_assets(ARRAY[?::bigint])", retired.ID,
	).Error)
}

func testUploadDependencyFailures(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	database *dbinfra.DB,
	storage *fakeImageStorage,
	imageModerator *testutil.MockModeration,
	author service.AuthResult,
) {
	t.Helper()
	var before int64
	require.NoError(t, gdb.Model(&model.ImageAsset{}).Count(&before).Error)
	storage.QueuePresign(testutil.StoragePresignBehavior{Err: errors.New("COS presign 5xx")})
	status, response, _ := performJSON(t, engine, http.MethodPost, "/api/v2/uploads/presign", map[string]any{
		"purpose": "post", "content_type": "image/jpeg", "size": 1024,
		"content_md5": base64.StdEncoding.EncodeToString(make([]byte, 16)),
	}, author.Token)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, apierr.BizServiceUnavailable, response.ErrorCode)
	var after int64
	require.NoError(t, gdb.Model(&model.ImageAsset{}).Count(&after).Error)
	require.Equal(t, before, after, "presign 失败不能创建悬空资产行")

	headFailure := presignImage(t, engine, author.Token, 1024)
	storage.QueueHead(testutil.StorageHeadBehavior{Err: errors.New("COS HEAD 5xx")})
	status, response, _ = performJSON(t, engine, http.MethodPost,
		fmt.Sprintf("/api/v2/uploads/%d/complete", headFailure.UploadID), nil, author.Token)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, apierr.BizServiceUnavailable, response.ErrorCode)
	requireUploadPending(t, gdb, headFailure.UploadID)
	status, response, _ = performJSON(t, engine, http.MethodPost,
		fmt.Sprintf("/api/v2/uploads/%d/complete", headFailure.UploadID), nil, author.Token)
	require.Equal(t, http.StatusOK, status)

	moderationFailure := presignImage(t, engine, author.Token, 1024)
	failureCall := len(imageModerator.ImageCalls()) + 1
	imageModerator.ProgramImage(testutil.ImageModerationRule{
		Call: failureCall,
		Outcome: testutil.ImageFailure(
			apierr.ServiceUnavailable("图片审核 5xx").WithCause(errors.New("moderation 5xx")),
		),
	})
	status, response, _ = performJSON(t, engine, http.MethodPost,
		fmt.Sprintf("/api/v2/uploads/%d/complete", moderationFailure.UploadID), nil, author.Token)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, apierr.BizServiceUnavailable, response.ErrorCode)
	requireUploadPending(t, gdb, moderationFailure.UploadID)
	status, response, _ = performJSON(t, engine, http.MethodPost,
		fmt.Sprintf("/api/v2/uploads/%d/complete", moderationFailure.UploadID), nil, author.Token)
	require.Equal(t, http.StatusOK, status)

	timeoutUpload := presignImage(t, engine, author.Token, 1024)
	release := make(chan struct{})
	timeoutCall := len(imageModerator.ImageCalls()) + 1
	imageModerator.ProgramImage(testutil.ImageModerationRule{
		Call: timeoutCall,
		Outcome: testutil.ImageModerationOutcome{
			Submission: testutil.ImageImmediate(model.ModerationVerdictPass).Submission,
			Release:    release,
		},
	})
	uploads := service.NewUploadService(
		storage, imageModerator, service.NewModerationService(
			service.DiscardModerationAlerter{}, service.DiscardImageAccessController{},
		),
		10*1024*1024, 10*time.Minute, time.Hour,
	)
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	timeoutResult := make(chan error, 1)
	go func() {
		timeoutResult <- database.RunInTx(requestCtx, func(ctx context.Context) error {
			_, completeErr := uploads.Complete(ctx, timeoutUpload.UploadID, author.User.ID)
			return completeErr
		})
	}()
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelWait()
	require.True(t, imageModerator.WaitForImageCalls(waitCtx, timeoutCall))
	cancelRequest()
	require.ErrorIs(t, <-timeoutResult, context.Canceled)
	close(release)
	requireUploadPending(t, gdb, timeoutUpload.UploadID)
	status, response, _ = performJSON(t, engine, http.MethodPost,
		fmt.Sprintf("/api/v2/uploads/%d/complete", timeoutUpload.UploadID), nil, author.Token)
	require.Equal(t, http.StatusOK, status)
}

func testCallbackValidation(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	database *dbinfra.DB,
	sender service.VerificationEmailSender,
	storage *fakeImageStorage,
	imageModerator *testutil.MockModeration,
	cfg appconfig.Config,
) {
	t.Helper()
	body := tencentCallbackBody(9223372036854775807, "missing-job", "missing/object.jpg", 0)
	status, response, _ := performJSON(t, engine, http.MethodPost,
		"/api/v2/moderation/tencent-ci/callback", body, "")
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, apierr.BizModerationCallbackInvalid, response.ErrorCode)
	status, response, _ = performJSON(t, engine, http.MethodPost,
		"/api/v2/moderation/tencent-ci/callback?token=wrong", body, "")
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, apierr.BizModerationCallbackInvalid, response.ErrorCode)

	unconfigured := cfg
	unconfigured.ModerationCallbackToken = ""
	unconfiguredEngine := uploadModerationEngine(
		unconfigured, database, sender, storage, imageModerator,
	)
	status, response, _ = performJSON(t, unconfiguredEngine, http.MethodPost,
		"/api/v2/moderation/tencent-ci/callback", body, "")
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, apierr.BizServiceUnavailable, response.ErrorCode)

	callbackPath := "/api/v2/moderation/tencent-ci/callback?token=" +
		url.QueryEscape(moderationCallbackToken)
	status, response, _ = performJSON(t, engine, http.MethodPost, callbackPath,
		map[string]any{"EventName": "UnexpectedEvent"}, "")
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, apierr.BizModerationCallbackInvalid, response.ErrorCode)

	status, response, _ = performJSON(t, engine, http.MethodPost, callbackPath, body, "")
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizImageNotFound, response.ErrorCode)

	asset := createModerationAsset(t, gdb, nil, "purged-callback")
	require.NoError(t, gdb.Exec(
		"SELECT danshi_purge_image_assets(ARRAY[?::bigint])", asset.ID,
	).Error)
	var purgedCount int64
	require.NoError(t, gdb.Model(&model.ImageAsset{}).Where("id = ?", asset.ID).Count(&purgedCount).Error)
	require.Zero(t, purgedCount)
	status, response, _ = performJSON(t, engine, http.MethodPost, callbackPath,
		tencentCallbackBody(asset.ID, "purged-job", asset.ObjectKey, 0), "")
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizImageNotFound, response.ErrorCode)
	var callbackRecords int64
	require.NoError(t, gdb.Model(&model.ModerationRecord{}).
		Where("provider_job_id IN ?", []string{"missing-job", "purged-job"}).Count(&callbackRecords).Error)
	require.Zero(t, callbackRecords, "无效目标回调不能写审核流水")
}

func testConcurrentImageCallbacks(
	t *testing.T,
	gdb *gorm.DB,
	database *dbinfra.DB,
	uploaderID uint64,
) {
	t.Helper()
	asset := createModerationAsset(t, gdb, &uploaderID, "concurrent-callback")
	mock := testutil.NewMockModeration()
	mock.SetDefaultImage(tencentPending("concurrent-callback-job"))
	_, err := mock.SubmitImage(context.Background(), service.ImageModerationRequest{
		ImageAssetID: asset.ID, ObjectKey: asset.ObjectKey,
	})
	require.NoError(t, err)

	const workers = 8
	ready := make(chan struct{}, workers)
	start := make(chan struct{})
	type callbackResult struct {
		result *service.ImageModerationApplyResult
		err    error
	}
	results := make(chan callbackResult, workers)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	moderation := service.NewModerationService(
		service.DiscardModerationAlerter{}, service.DiscardImageAccessController{},
	)
	for range workers {
		go func() {
			ready <- struct{}{}
			<-start
			var applied *service.ImageModerationApplyResult
			runErr := database.RunInTx(ctx, func(txCtx context.Context) error {
				var callbackErr error
				applied, callbackErr = mock.TriggerImageCallback(
					txCtx, "concurrent-callback-job", model.ModerationVerdictPass,
					moderation.ApplyImageCallback,
				)
				return callbackErr
			})
			results <- callbackResult{result: applied, err: runErr}
		}()
	}
	for range workers {
		<-ready
	}
	close(start)
	nonDuplicates := 0
	for range workers {
		result := <-results
		require.NoError(t, result.err)
		require.NotNil(t, result.result)
		if !result.result.Duplicate {
			nonDuplicates++
		}
	}
	require.Equal(t, 1, nonDuplicates)
	var records int64
	require.NoError(t, gdb.Model(&model.ModerationRecord{}).
		Where("provider_job_id = ?", "concurrent-callback-job").Count(&records).Error)
	require.EqualValues(t, 1, records)
	require.NoError(t, gdb.First(&asset, asset.ID).Error)
	require.Equal(t, model.ModerationStatusPass, asset.Moderation)
}

func testOutOfOrderImageCallbacks(
	t *testing.T,
	gdb *gorm.DB,
	database *dbinfra.DB,
	uploaderID uint64,
) {
	t.Helper()
	first := createModerationAsset(t, gdb, &uploaderID, "out-of-order-first")
	second := createModerationAsset(t, gdb, &uploaderID, "out-of-order-second")
	mock := testutil.NewMockModeration()
	mock.ProgramImage(
		testutil.ImageModerationRule{Call: 1, Outcome: tencentPending("out-of-order-job-1")},
		testutil.ImageModerationRule{Call: 2, Outcome: tencentPending("out-of-order-job-2")},
	)
	_, err := mock.SubmitImage(context.Background(), service.ImageModerationRequest{
		ImageAssetID: first.ID, ObjectKey: first.ObjectKey,
	})
	require.NoError(t, err)
	_, err = mock.SubmitImage(context.Background(), service.ImageModerationRequest{
		ImageAssetID: second.ID, ObjectKey: second.ObjectKey,
	})
	require.NoError(t, err)
	moderation := service.NewModerationService(
		service.DiscardModerationAlerter{}, service.DiscardImageAccessController{},
	)
	apply := func(jobID string, verdict model.ModerationVerdict) *service.ImageModerationApplyResult {
		var result *service.ImageModerationApplyResult
		runErr := database.RunInTx(context.Background(), func(ctx context.Context) error {
			var callbackErr error
			result, callbackErr = mock.TriggerImageCallback(ctx, jobID, verdict, moderation.ApplyImageCallback)
			return callbackErr
		})
		require.NoError(t, runErr)
		return result
	}
	require.False(t, apply("out-of-order-job-2", model.ModerationVerdictBlock).Duplicate)
	require.False(t, apply("out-of-order-job-1", model.ModerationVerdictPass).Duplicate)
	require.True(t, apply("out-of-order-job-1", model.ModerationVerdictPass).Duplicate)
	err = database.RunInTx(context.Background(), func(ctx context.Context) error {
		_, callbackErr := moderation.ApplyImageCallback(ctx, service.ImageModerationCallback{
			ImageAssetID:  second.ID,
			ObjectKey:     second.ObjectKey,
			Provider:      model.ModerationProviderTencentCI,
			ProviderJobID: "out-of-order-job-1",
			Verdict:       model.ModerationVerdictBlock,
		})
		return callbackErr
	})
	require.Equal(t, http.StatusConflict, apierr.As(err).Status,
		"同一供应商任务号不得被重放到另一张图片")
	mock.RequireCallbackOrder(t, "out-of-order-job-2", "out-of-order-job-1", "out-of-order-job-1")
	require.NoError(t, gdb.First(&first, first.ID).Error)
	require.NoError(t, gdb.First(&second, second.ID).Error)
	require.Equal(t, model.ModerationStatusPass, first.Moderation)
	require.Equal(t, model.ModerationStatusBlock, second.Moderation)
}

func testImageCallbackAccessControl(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	sender *captureEmailSender,
	storage *fakeImageStorage,
	author service.AuthResult,
	fixture postFixture,
) {
	t.Helper()
	presign := presignImage(t, engine, author.Token, 4096)
	completePath := fmt.Sprintf("/api/v2/uploads/%d/complete", presign.UploadID)
	status, response, _ := performJSON(t, engine, http.MethodPost, completePath, nil, author.Token)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
	var completed service.UploadCompleteResult
	decodeData(t, response, &completed)
	require.Equal(t, model.ImageStatusPending, completed.Status)
	require.Equal(t, lastPresign(t, storage).ObjectKey, completed.ObjectKey)

	var asset model.ImageAsset
	require.NoError(t, gdb.First(&asset, completed.UploadID).Error)
	require.Equal(t, model.ImageStatusPending, asset.Status)
	require.Equal(t, model.ModerationStatusPending, asset.Moderation)

	payload := sharePostPayload(fixture, "图片回调补发布测试", []string{"回调审核"})
	payload["images"] = []string{completed.PublicURL}
	post := createPost(t, engine, author.Token, payload)
	require.Equal(t, model.PostStatusPending, post.Status)

	callbackBody := map[string]any{
		"EventName": "ReviewImage",
		"JobsDetail": map[string]any{
			"JobId": "ci-image-pass-job", "State": "Success",
			"Object": completed.ObjectKey,
			"DataId": fmt.Sprintf("image_asset:%d", completed.UploadID),
			"Result": 0, "Score": 98,
		},
	}
	callbackPath := "/api/v2/moderation/tencent-ci/callback?token=" +
		url.QueryEscape(moderationCallbackToken)

	status, response, _ = performJSON(
		t, engine, http.MethodPost, callbackPath, callbackBody, "",
	)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
	var first service.ImageModerationApplyResult
	decodeData(t, response, &first)
	require.False(t, first.Duplicate)
	require.EqualValues(t, 1, first.ApprovedPosts)

	var storedPost model.Post
	require.NoError(t, gdb.First(&storedPost, post.ID).Error)
	require.Equal(t, model.PostStatusApproved, storedPost.Status)
	require.NoError(t, gdb.First(&asset, completed.UploadID).Error)
	require.Equal(t, model.ModerationStatusPass, asset.Moderation)
	var publishedDelivery model.ImageAccessDelivery
	require.NoError(t, gdb.First(&publishedDelivery, completed.UploadID).Error)
	require.True(t, publishedDelivery.DesiredPublic)
	require.False(t, publishedDelivery.PurgeRequired,
		"pending 首次放行的 URL 从未缓存过私有响应，不应消耗刷新配额")

	status, response, _ = performJSON(
		t, engine, http.MethodPost, callbackPath, callbackBody, "",
	)
	require.Equal(t, http.StatusOK, status)
	var duplicate service.ImageModerationApplyResult
	decodeData(t, response, &duplicate)
	require.True(t, duplicate.Duplicate)
	require.Zero(t, duplicate.ApprovedPosts)

	var records []model.ModerationRecord
	require.NoError(t, gdb.Where(
		"image_asset_id = ?", completed.UploadID,
	).Find(&records).Error)
	require.Len(t, records, 1, "重复回调只能追加一条机器审核流水")
	require.Equal(t, model.ModerationProviderTencentCI, records[0].Provider)
	require.NotNil(t, records[0].ProviderJobID)
	require.Equal(t, "ci-image-pass-job", *records[0].ProviderJobID)
	require.NotNil(t, records[0].Score)
	require.True(t, records[0].Score.Equal(decimal.NewFromInt(98)))
	require.Contains(t, string(records[0].RawResponse), "ci-image-pass-job")

	blocked := completeImageForAccessTest(t, engine, author.Token, 5120)
	blockedPayload := sharePostPayload(fixture, "图片违规后不可访问", []string{"图片访问控制"})
	blockedPayload["images"] = []string{blocked.PublicURL}
	blockedPost := createPost(t, engine, author.Token, blockedPayload)
	require.Equal(t, model.PostStatusPending, blockedPost.Status)
	_, err := storage.ReadPublicURL(blocked.PublicURL)
	require.NoError(t, err, "审核结论落地前对象应可公开读取")
	blockedBody := tencentCallbackBody(
		blocked.UploadID, "ci-image-block-job", blocked.ObjectKey, 1,
	)
	status, response, _ = performJSON(t, engine, http.MethodPost, callbackPath, blockedBody, "")
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
	_, err = storage.ReadPublicURL(blocked.PublicURL)
	require.NoError(t, err, "回调事务不得执行外部 ACL；worker 随后收敛")
	var blockedAsset model.ImageAsset
	require.NoError(t, gdb.First(&blockedAsset, blocked.UploadID).Error)
	require.Equal(t, model.ModerationStatusBlock, blockedAsset.Moderation)
	var blockedDelivery model.ImageAccessDelivery
	require.NoError(t, gdb.First(&blockedDelivery, blocked.UploadID).Error)
	require.False(t, blockedDelivery.DesiredPublic)
	require.True(t, blockedDelivery.PurgeRequired,
		"转私有时必须清除可能仍缓存的公开响应")
	require.Equal(t, model.ImageAccessPendingACL, blockedDelivery.State)

	unrelated := registerPostTestUser(
		t, engine, sender, "blocked-image-unrelated@fdueat.com", "违规图片无关用户",
	)
	reviewer := registerPostTestUser(
		t, engine, sender, "blocked-image-reviewer@fdueat.com", "违规图片审核员",
	)
	dictionaryReviewer := registerPostTestUser(
		t, engine, sender, "blocked-image-dict@fdueat.com", "违规图片词表员",
	)
	require.NoError(t, gdb.Create(&[]model.UserRoleBinding{
		{UserID: reviewer.User.ID, Role: model.UserRoleModerator, GrantedAt: time.Now().UTC()},
		{UserID: dictionaryReviewer.User.ID, Role: model.UserRoleDictReviewer, GrantedAt: time.Now().UTC()},
	}).Error)
	signedCallsBefore := len(storage.PresignGetCalls())
	status, response, _ = performJSON(t, engine, http.MethodGet,
		fmt.Sprintf("/api/v2/uploads/%d", blocked.UploadID), nil, unrelated.Token)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, apierr.BizImageNotOwned, response.ErrorCode)
	require.Len(t, storage.PresignGetCalls(), signedCallsBefore,
		"无关用户不得触发读取 URL 签发")

	status, response, _ = performJSON(t, engine, http.MethodGet,
		fmt.Sprintf("/api/v2/uploads/%d", blocked.UploadID), nil, author.Token)
	require.Equal(t, http.StatusOK, status)
	var ownerView service.UploadImageView
	decodeData(t, response, &ownerView)
	require.Equal(t, model.ModerationStatusBlock, ownerView.Moderation)
	require.NotContains(t, ownerView.ImageURL, "imageMogr2",
		"上传者签名读取地址不得盲目追加图片处理参数")
	_, err = storage.ReadPresignedURL(ownerView.ImageURL)
	require.NoError(t, err, "上传者本人应能通过短期签名 URL 读取同一张私有图片")

	adminPath := fmt.Sprintf("/api/v2/admin/images/%d", blocked.UploadID)
	status, response, _ = performJSON(t, engine, http.MethodGet, adminPath, nil, dictionaryReviewer.Token)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, apierr.BizPermissionDenied, response.ErrorCode)
	status, response, _ = performJSON(t, engine, http.MethodGet, adminPath, nil, reviewer.Token)
	require.Equal(t, http.StatusOK, status)
	var reviewerView service.AdminImageView
	decodeData(t, response, &reviewerView)
	require.NotNil(t, reviewerView.ImageURL)
	require.NotContains(t, *reviewerView.ImageURL, "imageMogr2",
		"审核员签名读取地址不得盲目追加图片处理参数")
	_, err = storage.ReadPresignedURL(*reviewerView.ImageURL)
	require.NoError(t, err, "内容审核员应能通过短期签名 URL 读取同一张私有图片")
	require.Equal(t, []testutil.StoragePresignGetCall{
		{ObjectKey: blocked.ObjectKey, TTL: time.Hour},
		{ObjectKey: blocked.ObjectKey, TTL: time.Hour},
	}, storage.PresignGetCalls()[signedCallsBefore:])

	require.NoError(t, gdb.Model(&model.ImageAsset{}).Where("id = ?", blocked.UploadID).
		UpdateColumn("uploader_id", nil).Error)
	status, response, _ = performJSON(t, engine, http.MethodGet,
		fmt.Sprintf("/api/v2/uploads/%d", blocked.UploadID), nil, author.Token)
	require.Equal(t, http.StatusForbidden, status, "uploader_id 为 NULL 时不得误放行")
	status, response, _ = performJSON(t, engine, http.MethodGet, adminPath, nil, reviewer.Token)
	require.Equal(t, http.StatusOK, status, "uploader_id 为 NULL 不影响既有内容审核能力")
	decodeData(t, response, &reviewerView)
	require.Nil(t, reviewerView.UploaderID)
	require.NotNil(t, reviewerView.ImageURL)
	_, err = storage.ReadPresignedURL(*reviewerView.ImageURL)
	require.NoError(t, err)

	status, response, _ = performJSON(t, engine, http.MethodPost, callbackPath, blockedBody, "")
	require.Equal(t, http.StatusOK, status)
	decodeData(t, response, &duplicate)
	require.True(t, duplicate.Duplicate)
	var duplicateDelivery model.ImageAccessDelivery
	require.NoError(t, gdb.First(&duplicateDelivery, blocked.UploadID).Error)
	require.Equal(t, blockedDelivery.Generation, duplicateDelivery.Generation,
		"同一 provider job 重放不得重置 durable worker")

	retry := completeImageForAccessTest(t, engine, author.Token, 6144)
	retryPayload := sharePostPayload(fixture, "ACL 故障不回滚审核", []string{"ACL 重试"})
	retryPayload["images"] = []string{retry.PublicURL}
	createPost(t, engine, author.Token, retryPayload)
	retryBody := tencentCallbackBody(
		retry.UploadID, "ci-image-acl-retry-job", retry.ObjectKey, 1,
	)
	status, response, _ = performJSON(t, engine, http.MethodPost, callbackPath, retryBody, "")
	require.Equal(t, http.StatusOK, status, "外部存储不在审核事务中")
	var retryAsset model.ImageAsset
	require.NoError(t, gdb.First(&retryAsset, retry.UploadID).Error)
	require.Equal(t, model.ModerationStatusBlock, retryAsset.Moderation,
		"对象存储故障时审核事实仍必须提交")
	var retryRecords int64
	require.NoError(t, gdb.Model(&model.ModerationRecord{}).Where(
		"image_asset_id = ? AND provider_job_id = ?", retry.UploadID, "ci-image-acl-retry-job",
	).Count(&retryRecords).Error)
	require.EqualValues(t, 1, retryRecords)
	_, err = storage.ReadPublicURL(retry.PublicURL)
	require.NoError(t, err, "模拟 ACL 失败后对象状态不应被 Mock 伪造为私有")

	status, response, _ = performJSON(t, engine, http.MethodPost, callbackPath, retryBody, "")
	require.Equal(t, http.StatusOK, status)
	decodeData(t, response, &duplicate)
	require.True(t, duplicate.Duplicate)
	_, err = storage.ReadPublicURL(retry.PublicURL)
	require.NoError(t, err, "重复回调不直接调用外部存储")

	invalidPath := "/api/v2/moderation/tencent-ci/callback?token=wrong"
	status, _, _ = performJSON(t, engine, http.MethodPost, invalidPath, callbackBody, "")
	require.Equal(t, http.StatusForbidden, status)
}

func completeImageForAccessTest(
	t *testing.T,
	engine *server.Hertz,
	token string,
	size int64,
) service.UploadCompleteResult {
	t.Helper()
	presign := presignImage(t, engine, token, size)
	status, response, _ := performJSON(t, engine, http.MethodPost,
		fmt.Sprintf("/api/v2/uploads/%d/complete", presign.UploadID), nil, token)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
	var completed service.UploadCompleteResult
	decodeData(t, response, &completed)
	return completed
}

func presignImage(
	t *testing.T,
	engine *server.Hertz,
	token string,
	size int64,
) service.UploadPresignResult {
	t.Helper()
	status, response, _ := performJSON(t, engine, http.MethodPost, "/api/v2/uploads/presign", map[string]any{
		"purpose": "post", "content_type": "image/jpeg", "size": size,
		"content_md5": base64.StdEncoding.EncodeToString(make([]byte, 16)),
	}, token)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
	var result service.UploadPresignResult
	decodeData(t, response, &result)
	return result
}

func testCompleteExpiryRace(
	t *testing.T,
	database *dbinfra.DB,
	storage *fakeImageStorage,
	userID uint64,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	moderation := service.NewModerationService(
		service.DiscardModerationAlerter{}, service.DiscardImageAccessController{},
	)
	uploads := service.NewUploadService(
		storage, fixedAsyncImageModerator("race-job"), moderation,
		10*1024*1024, 10*time.Minute, time.Hour,
	)
	var presign *service.UploadPresignResult
	err := database.RunInTx(ctx, func(txCtx context.Context) error {
		var presignErr error
		presign, presignErr = uploads.Presign(txCtx, userID, service.UploadPresignInput{
			Purpose: "post", ContentType: "image/jpeg", Size: 1024,
			ContentMD5: base64.StdEncoding.EncodeToString(make([]byte, 16)),
		})
		return presignErr
	})
	require.NoError(t, err)
	require.NoError(t, database.Model(&model.ImageAsset{}).Where("id = ?", presign.UploadID).
		Update("created_at", time.Now().UTC().Add(-2*time.Hour)).Error)

	releaseDelete := make(chan struct{})
	deleteCallsBefore := len(storage.DeleteCalls())
	storage.QueueDelete(testutil.StorageDeleteBehavior{Release: releaseDelete})

	expireResult := make(chan error, 1)
	go func() {
		expireResult <- database.RunInTx(ctx, func(txCtx context.Context) error {
			result, expireErr := uploads.ExpirePending(txCtx, service.ExpirePendingOptions{
				Before: time.Now().UTC().Add(-time.Hour), Limit: 1,
			})
			if expireErr == nil && result.Retired != 1 {
				return fmt.Errorf("期望回收 1 条，实际 %d", result.Retired)
			}
			return expireErr
		})
	}()
	require.True(t, storage.WaitForDeleteCalls(ctx, deleteCallsBefore+1),
		"过期清理未进入对象删除阶段")

	completeResult := make(chan error, 1)
	completeStarted := make(chan struct{})
	go func() {
		close(completeStarted)
		completeResult <- database.RunInTx(ctx, func(txCtx context.Context) error {
			_, completeErr := uploads.Complete(txCtx, presign.UploadID, userID)
			return completeErr
		})
	}()
	<-completeStarted
	close(releaseDelete)
	require.NoError(t, <-expireResult)
	completeErr := <-completeResult
	require.Error(t, completeErr)
	require.Equal(t, http.StatusConflict, apierr.As(completeErr).Status)

	var asset model.ImageAsset
	require.NoError(t, database.First(&asset, presign.UploadID).Error)
	require.Equal(t, model.ImageStatusRetired, asset.Status)
}

func testConcurrentExpiryWorkers(
	t *testing.T,
	gdb *gorm.DB,
	database *dbinfra.DB,
	storage *fakeImageStorage,
	uploaderID uint64,
) {
	t.Helper()
	createPending := func(suffix string, createdAt time.Time) model.ImageAsset {
		size := int64(1024)
		asset := model.ImageAsset{
			UploaderID: &uploaderID, Purpose: model.ImagePurposePost,
			ObjectKey:   "expiry/" + suffix + ".jpg",
			PublicURL:   "https://img.example.test/expiry/" + suffix + ".jpg",
			ContentType: "image/jpeg", Size: &size, Status: model.ImageStatusPending,
			Moderation: model.ModerationStatusPass, CreatedAt: createdAt, UpdatedAt: createdAt,
		}
		require.NoError(t, gdb.Create(&asset).Error)
		return asset
	}
	uploads := service.NewUploadService(
		storage, nil, nil, 10*1024*1024, 10*time.Minute, time.Hour,
	)
	before := time.Now().UTC().Add(-3 * time.Hour)
	first := createPending("worker-first", before.Add(-2*time.Hour))
	second := createPending("worker-second", before.Add(-time.Hour))

	type workerResult struct {
		expiration service.UploadExpirationResult
		err        error
	}
	runWorker := func(results chan<- workerResult) {
		var expiration service.UploadExpirationResult
		err := database.RunInTx(context.Background(), func(ctx context.Context) error {
			var expireErr error
			expiration, expireErr = uploads.ExpirePending(ctx, service.ExpirePendingOptions{
				Before: before, Limit: 1,
			})
			return expireErr
		})
		results <- workerResult{expiration: expiration, err: err}
	}

	releaseFirst := make(chan struct{})
	deleteCallsBefore := len(storage.DeleteCalls())
	storage.QueueDelete(testutil.StorageDeleteBehavior{Release: releaseFirst})
	results := make(chan workerResult, 2)
	go runWorker(results)
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.True(t, storage.WaitForDeleteCalls(waitCtx, deleteCallsBefore+1))
	go runWorker(results)
	require.True(t, storage.WaitForDeleteCalls(waitCtx, deleteCallsBefore+2),
		"第二个 worker 应通过 SKIP LOCKED 领取另一条资产")
	close(releaseFirst)
	for range 2 {
		result := <-results
		require.NoError(t, result.err)
		require.Equal(t, 1, result.expiration.Selected)
		require.Equal(t, 1, result.expiration.Retired)
		require.Empty(t, result.expiration.Failures)
	}
	require.ElementsMatch(t, []string{first.ObjectKey, second.ObjectKey},
		storage.DeleteCalls()[deleteCallsBefore:])

	failureFirst := createPending("failure-first", before.Add(-2*time.Hour))
	failureSecond := createPending("failure-second", before.Add(-time.Hour))
	storage.QueueDelete(testutil.StorageDeleteBehavior{Err: errors.New("COS delete unavailable")})
	var isolated service.UploadExpirationResult
	err := database.RunInTx(context.Background(), func(ctx context.Context) error {
		var expireErr error
		isolated, expireErr = uploads.ExpirePending(ctx, service.ExpirePendingOptions{
			Before: before, Limit: 2,
		})
		return expireErr
	})
	require.NoError(t, err)
	require.Equal(t, 2, isolated.Selected)
	require.Equal(t, 1, isolated.Retired)
	require.Len(t, isolated.Failures, 1)
	require.Equal(t, failureFirst.ID, isolated.Failures[0].ImageAssetID)
	require.NoError(t, gdb.First(&failureFirst, failureFirst.ID).Error)
	require.NoError(t, gdb.First(&failureSecond, failureSecond.ID).Error)
	require.Equal(t, model.ImageStatusPending, failureFirst.Status,
		"删除失败的对象必须保留 pending 供下次重试")
	require.Equal(t, model.ImageStatusRetired, failureSecond.Status,
		"单个对象失败不能回滚同批成功项")

	deleteCallsBefore = len(storage.DeleteCalls())
	var preview service.UploadExpirationResult
	err = database.RunInTx(context.Background(), func(ctx context.Context) error {
		var expireErr error
		preview, expireErr = uploads.ExpirePending(ctx, service.ExpirePendingOptions{
			Before: before, Limit: 1, DryRun: true,
		})
		return expireErr
	})
	require.NoError(t, err)
	require.Equal(t, 1, preview.Selected)
	require.Zero(t, preview.Retired)
	require.Len(t, storage.DeleteCalls(), deleteCallsBefore, "dry-run 不得删除对象")
}

func testManualReviewIsAppendOnly(
	t *testing.T,
	gdb *gorm.DB,
	database *dbinfra.DB,
	reviewerID uint64,
) {
	t.Helper()
	size := int64(512)
	asset := model.ImageAsset{
		UploaderID: &reviewerID, Purpose: model.ImagePurposePost,
		ObjectKey:   "manual-review/image.jpg",
		PublicURL:   "https://img.example.test/manual-review/image.jpg",
		ContentType: "image/jpeg", Size: &size, Status: model.ImageStatusReady,
		Moderation: model.ModerationStatusReview,
	}
	require.NoError(t, gdb.Create(&asset).Error)
	jobID := "manual-review-machine-job"
	machine := model.ModerationRecord{
		ImageAssetID: &asset.ID, Scene: model.ModerationSceneImage,
		Provider: model.ModerationProviderTencentCI, ProviderJobID: &jobID,
		Verdict: model.ModerationVerdictReview, Labels: pq.StringArray{"abuse"},
		CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, gdb.Create(&machine).Error)

	moderation := service.NewModerationService(
		service.DiscardModerationAlerter{}, service.DiscardImageAccessController{},
	)
	var manual *model.ModerationRecord
	err := database.RunInTx(context.Background(), func(ctx context.Context) error {
		var reviewErr error
		manual, reviewErr = moderation.ManualReview(ctx, service.ManualReviewInput{
			MachineRecordID: machine.ID, ReviewerID: reviewerID,
			Verdict: model.ModerationVerdictPass, Labels: []string{"human-approved"},
		})
		return reviewErr
	})
	require.NoError(t, err)
	require.NotZero(t, manual.ID)
	require.Equal(t, model.ModerationProviderManual, manual.Provider)
	require.Nil(t, manual.ProviderJobID)
	require.Equal(t, &machine.ID, manual.SupersedesID)
	require.Equal(t, &reviewerID, manual.ReviewerID)
	require.NotNil(t, manual.ReviewedAt)

	var unchanged model.ModerationRecord
	require.NoError(t, gdb.First(&unchanged, machine.ID).Error)
	require.Equal(t, model.ModerationVerdictReview, unchanged.Verdict)
	require.Nil(t, unchanged.ReviewerID)
	require.Nil(t, unchanged.SupersedesID)
	require.NoError(t, gdb.First(&asset, asset.ID).Error)
	require.Equal(t, model.ModerationStatusPass, asset.Moderation)

	err = database.RunInTx(context.Background(), func(ctx context.Context) error {
		_, reviewErr := moderation.ManualReview(ctx, service.ManualReviewInput{
			MachineRecordID: machine.ID, ReviewerID: reviewerID,
			Verdict: model.ModerationVerdictBlock,
		})
		return reviewErr
	})
	require.Error(t, err)
	require.Equal(t, http.StatusConflict, apierr.As(err).Status)
	var count int64
	require.NoError(t, gdb.Model(&model.ModerationRecord{}).
		Where("id = ? OR supersedes_id = ?", machine.ID, machine.ID).Count(&count).Error)
	require.EqualValues(t, 2, count, "同一条机器记录至多只能追加一次人工复核")
}

func requireUploadFieldError(
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

func requireUploadPending(t *testing.T, gdb *gorm.DB, uploadID uint64) {
	t.Helper()
	var asset model.ImageAsset
	require.NoError(t, gdb.First(&asset, uploadID).Error)
	require.Equal(t, model.ImageStatusPending, asset.Status)
	require.Empty(t, asset.PublicURL)
	require.Equal(t, model.ModerationStatusPending, asset.Moderation)
}

func tencentCallbackBody(
	assetID uint64,
	jobID string,
	objectKey string,
	result int,
) map[string]any {
	return map[string]any{
		"EventName": "ReviewImage",
		"JobsDetail": map[string]any{
			"JobId": jobID, "State": "Success", "Object": objectKey,
			"DataId": fmt.Sprintf("image_asset:%d", assetID), "Result": result, "Score": 99,
		},
	}
}

func createModerationAsset(
	t *testing.T,
	gdb *gorm.DB,
	uploaderID *uint64,
	key string,
) model.ImageAsset {
	t.Helper()
	size := int64(1024)
	asset := model.ImageAsset{
		UploaderID: uploaderID, Purpose: model.ImagePurposePost,
		ObjectKey:   "moderation/" + key + ".jpg",
		PublicURL:   "https://img.example.test/moderation/" + key + ".jpg",
		ContentType: "image/jpeg", Size: &size, Status: model.ImageStatusReady,
		Moderation: model.ModerationStatusPending,
	}
	require.NoError(t, gdb.Create(&asset).Error)
	return asset
}
