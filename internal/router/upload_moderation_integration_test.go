package router_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	hertzconfig "github.com/cloudwego/hertz/pkg/common/config"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	appconfig "github.com/jingyijun/danshi_backend_go/internal/config"
	dbinfra "github.com/jingyijun/danshi_backend_go/internal/infra/db"
	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/router"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

const moderationCallbackToken = "integration-callback-token"

type fakeImageStorage struct {
	mu            sync.Mutex
	objects       map[string]int64
	presigns      []service.StoragePresignRequest
	deleteStarted chan struct{}
	releaseDelete <-chan struct{}
}

func newFakeImageStorage() *fakeImageStorage {
	return &fakeImageStorage{objects: make(map[string]int64)}
}

func (s *fakeImageStorage) PresignPut(
	_ context.Context,
	request service.StoragePresignRequest,
) (service.StorageUploadTicket, error) {
	s.mu.Lock()
	s.objects[request.ObjectKey] = request.ContentLength
	s.presigns = append(s.presigns, request)
	s.mu.Unlock()
	return service.StorageUploadTicket{
		UploadURL: "https://cos.example.test/" + url.PathEscape(request.ObjectKey),
		ExpiresAt: time.Now().UTC().Add(request.TTL),
	}, nil
}

func (s *fakeImageStorage) HeadObject(
	_ context.Context,
	objectKey string,
) (service.StorageObjectMeta, error) {
	s.mu.Lock()
	size, exists := s.objects[objectKey]
	s.mu.Unlock()
	return service.StorageObjectMeta{Exists: exists, ContentLength: size}, nil
}

func (s *fakeImageStorage) DeleteObject(_ context.Context, objectKey string) error {
	if s.deleteStarted != nil {
		select {
		case s.deleteStarted <- struct{}{}:
		default:
		}
	}
	if s.releaseDelete != nil {
		<-s.releaseDelete
	}
	s.mu.Lock()
	delete(s.objects, objectKey)
	s.mu.Unlock()
	return nil
}

func (*fakeImageStorage) PublicURL(objectKey string) (string, error) {
	return "https://img.example.test/" + objectKey, nil
}

func (s *fakeImageStorage) lastPresign() service.StoragePresignRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.presigns[len(s.presigns)-1]
}

type fixedAsyncImageModerator struct {
	jobID string
}

func (m fixedAsyncImageModerator) SubmitImage(
	context.Context,
	service.ImageModerationRequest,
) (service.ImageModerationSubmission, error) {
	jobID := m.jobID
	return service.ImageModerationSubmission{
		Provider:      model.ModerationProviderTencentCI,
		ProviderJobID: &jobID,
	}, nil
}

func TestUploadModerationAgainstPostgres(t *testing.T) {
	gdb, database := openAuthPostgres(t)
	storage := newFakeImageStorage()
	sender := newCaptureEmailSender()
	cfg := uploadModerationTestConfig()
	engine := uploadModerationEngine(cfg, database, sender, storage)
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

	t.Run("pending image callback approves post exactly once", func(t *testing.T) {
		testImageCallbackApprovesPost(t, engine, gdb, storage, author, fixture)
	})

	t.Run("complete loses expiry race without resurrecting asset", func(t *testing.T) {
		testCompleteExpiryRace(t, database, storage, author.User.ID)
	})

	t.Run("manual review appends and supersedes machine record", func(t *testing.T) {
		testManualReviewIsAppendOnly(t, gdb, database, author.User.ID)
	})
}

func uploadModerationTestConfig() appconfig.Config {
	cfg := authTestConfig()
	cfg.COSMaxImageBytes = 10 * 1024 * 1024
	cfg.COSPresignTTLS = 600
	cfg.ModerationCallbackToken = moderationCallbackToken
	return cfg
}

func uploadModerationEngine(
	cfg appconfig.Config,
	database *dbinfra.DB,
	sender service.VerificationEmailSender,
	storage service.ImageStorage,
) *server.Hertz {
	engine := server.New(
		server.WithHandleMethodNotAllowed(true),
		hertzconfig.Option{F: func(_ *hertzconfig.Options) {}},
	)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router.Register(engine, router.Deps{
		Config: cfg, DB: database, Log: log, EmailSender: sender,
		ContentModerator: service.DirectPassContentModerator{}, ImageStorage: storage,
		ImageModerator:    fixedAsyncImageModerator{jobID: "ci-image-pass-job"},
		ModerationAlerter: service.DiscardModerationAlerter{},
	})
	return engine
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
	request := storage.lastPresign()
	require.Equal(t, payload["content_md5"], request.ContentMD5)
	require.EqualValues(t, 2048, request.ContentLength)
	require.Equal(t, "image/jpeg", request.ContentType)
	var presign service.UploadPresignResult
	decodeData(t, response, &presign)
	completePath := fmt.Sprintf("/api/v2/uploads/%d/complete", presign.UploadID)
	status, response, _ = performJSON(t, engine, http.MethodPost, completePath, nil, token)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
}

func testImageCallbackApprovesPost(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
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
	require.Equal(t, model.ImageStatusReady, completed.Status)
	require.Equal(t, storage.lastPresign().ObjectKey, completed.ObjectKey)

	var asset model.ImageAsset
	require.NoError(t, gdb.First(&asset, completed.UploadID).Error)
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

	invalidPath := "/api/v2/moderation/tencent-ci/callback?token=wrong"
	status, _, _ = performJSON(t, engine, http.MethodPost, invalidPath, callbackBody, "")
	require.Equal(t, http.StatusForbidden, status)
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
	moderation := service.NewModerationService(service.DiscardModerationAlerter{})
	uploads := service.NewUploadService(
		storage, fixedAsyncImageModerator{jobID: "race-job"}, moderation,
		10*1024*1024, 10*time.Minute,
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

	deleteStarted := make(chan struct{}, 1)
	releaseDelete := make(chan struct{})
	storage.deleteStarted = deleteStarted
	storage.releaseDelete = releaseDelete
	defer func() {
		storage.deleteStarted = nil
		storage.releaseDelete = nil
	}()

	expireResult := make(chan error, 1)
	go func() {
		expireResult <- database.RunInTx(ctx, func(txCtx context.Context) error {
			count, expireErr := uploads.ExpirePending(txCtx, time.Now().UTC().Add(time.Hour), 1)
			if expireErr == nil && count != 1 {
				return fmt.Errorf("期望回收 1 条，实际 %d", count)
			}
			return expireErr
		})
	}()
	select {
	case <-deleteStarted:
	case <-ctx.Done():
		t.Fatal("过期清理未进入对象删除阶段")
	}

	completeResult := make(chan error, 1)
	go func() {
		completeResult <- database.RunInTx(ctx, func(txCtx context.Context) error {
			_, completeErr := uploads.Complete(txCtx, presign.UploadID, userID)
			return completeErr
		})
	}()
	select {
	case early := <-completeResult:
		t.Fatalf("complete 未等待过期清理行锁：%v", early)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseDelete)
	require.NoError(t, <-expireResult)
	completeErr := <-completeResult
	require.Error(t, completeErr)
	require.Equal(t, http.StatusConflict, apierr.As(completeErr).Status)

	var asset model.ImageAsset
	require.NoError(t, database.First(&asset, presign.UploadID).Error)
	require.Equal(t, model.ImageStatusRetired, asset.Status)
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

	moderation := service.NewModerationService(service.DiscardModerationAlerter{})
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
