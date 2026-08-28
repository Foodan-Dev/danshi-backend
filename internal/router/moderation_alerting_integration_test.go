package router_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	hertzconfig "github.com/cloudwego/hertz/pkg/common/config"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	appconfig "github.com/Foodan-Dev/danshi-backend/internal/config"
	"github.com/Foodan-Dev/danshi-backend/internal/infra/alerting"
	dbinfra "github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/router"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

type moderationAlertCapture struct {
	mu     sync.Mutex
	alerts []service.ModerationAlert
}

func (c *moderationAlertCapture) Alert(_ context.Context, alert service.ModerationAlert) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.alerts = append(c.alerts, alert)
}

func (c *moderationAlertCapture) all() []service.ModerationAlert {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]service.ModerationAlert{}, c.alerts...)
}

func TestModerationAlertingAgainstPostgres(t *testing.T) {
	gdb, database := openAuthPostgres(t)
	cfg := uploadModerationTestConfig()
	cfg.ModerationCallbackAuthFailureThreshold = 3
	cfg.ModerationCallbackAuthFailureWindowS = 60
	cfg.ModerationReviewBacklogThreshold = 1
	cfg.ModerationReviewBacklogCooldownS = 3600
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	capture := &moderationAlertCapture{}
	engine := moderationAlertingEngine(cfg, database, capture, log)
	callbackPath := "/api/v2/moderation/tencent-ci/callback?token=" +
		url.QueryEscape(moderationCallbackToken)

	t.Run("single token failure is quiet and a short burst alerts once", func(t *testing.T) {
		wrongPath := "/api/v2/moderation/tencent-ci/callback?token=wrong"
		body := map[string]any{"ignored": true}
		status, _, _ := performJSON(t, engine, http.MethodPost, wrongPath, body, "")
		require.Equal(t, http.StatusForbidden, status)
		require.Empty(t, alertsByKind(capture.all(), service.ModerationAlertKindCallbackAuthFailures))

		status, _, _ = performJSON(t, engine, http.MethodPost, wrongPath, body, "")
		require.Equal(t, http.StatusForbidden, status)
		status, _, _ = performJSON(t, engine, http.MethodPost, wrongPath, body, "")
		require.Equal(t, http.StatusForbidden, status)
		authAlerts := alertsByKind(capture.all(), service.ModerationAlertKindCallbackAuthFailures)
		require.Len(t, authAlerts, 1)
		require.Equal(t, 3, authAlerts[0].Occurrences)
		require.Equal(t, 60, authAlerts[0].WindowSeconds)

		status, _, _ = performJSON(t, engine, http.MethodPost, wrongPath, body, "")
		require.Equal(t, http.StatusForbidden, status)
		require.Len(t,
			alertsByKind(capture.all(), service.ModerationAlertKindCallbackAuthFailures), 1,
			"同一固定窗口超过阈值后不得每次重复告警")
	})

	t.Run("payload and target protocol failures each alert", func(t *testing.T) {
		status, _, _ := performJSON(t, engine, http.MethodPost, callbackPath,
			map[string]any{"EventName": "UnexpectedEvent"}, "")
		require.Equal(t, http.StatusBadRequest, status)
		require.Len(t,
			alertsByKind(capture.all(), service.ModerationAlertKindCallbackPayloadInvalid), 1)

		body := tencentCallbackBody(9223372036854775807, "missing-alert-job", "missing.jpg", 0)
		status, _, _ = performJSON(t, engine, http.MethodPost, callbackPath, body, "")
		require.Equal(t, http.StatusNotFound, status)
		targetAlerts := alertsByKind(capture.all(), service.ModerationAlertKindCallbackTargetInvalid)
		require.Len(t, targetAlerts, 1)
		require.Equal(t, uint64(9223372036854775807), targetAlerts[0].TargetID)
	})

	t.Run("internal callback processing failure alerts after rollback", func(t *testing.T) {
		asset := createModerationAsset(t, gdb, nil, "forced-internal-alert")
		installModerationInsertFailure(t, gdb)
		defer removeModerationInsertFailure(t, gdb)

		status, _, _ := performJSON(t, engine, http.MethodPost, callbackPath,
			tencentCallbackBody(asset.ID, "forced-internal-alert", asset.ObjectKey, 0), "")
		require.Equal(t, http.StatusInternalServerError, status)
		processingAlerts := alertsByKind(
			capture.all(), service.ModerationAlertKindCallbackProcessingFailed,
		)
		require.Len(t, processingAlerts, 1)
		require.Equal(t, asset.ID, processingAlerts[0].TargetID)
		require.NotEmpty(t, processingAlerts[0].ErrorID)

		var records int64
		require.NoError(t, gdb.Model(&model.ModerationRecord{}).
			Where("provider_job_id = ?", "forced-internal-alert").Count(&records).Error)
		require.Zero(t, records, "内部错误的业务事务必须完整回滚")
	})

	t.Run("commit failure drops the queued verdict alert", func(t *testing.T) {
		asset := createModerationAsset(t, gdb, nil, "forced-commit-alert")
		installModerationCommitFailure(t, gdb)
		defer removeModerationCommitFailure(t, gdb)
		before := len(capture.all())

		status, _, _ := performJSON(t, engine, http.MethodPost, callbackPath,
			tencentCallbackBody(asset.ID, "forced-commit-alert", asset.ObjectKey, 2), "")
		require.Equal(t, http.StatusInternalServerError, status)
		require.Len(t, capture.all(), before, "提交失败时不得发出事务内排队的审核结论告警")

		var records int64
		require.NoError(t, gdb.Model(&model.ModerationRecord{}).
			Where("provider_job_id = ?", "forced-commit-alert").Count(&records).Error)
		require.Zero(t, records)
		var stored model.ImageAsset
		require.NoError(t, gdb.First(&stored, asset.ID).Error)
		require.Equal(t, model.ModerationStatusPending, stored.Moderation)
	})

	t.Run("channel failure is logged while callback still succeeds", func(t *testing.T) {
		var webhookCalls atomic.Int32
		webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			webhookCalls.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer webhook.Close()
		var logs bytes.Buffer
		channelLog := slog.New(slog.NewTextHandler(&logs, nil))
		channel := alerting.NewFeishuWebhook(webhook.URL, webhook.Client(), channelLog)
		channelEngine := moderationAlertingEngine(cfg, database, channel, channelLog)
		asset := createModerationAsset(t, gdb, nil, "channel-fail-open")

		status, _, _ := performJSON(t, channelEngine, http.MethodPost, callbackPath,
			tencentCallbackBody(asset.ID, "channel-fail-open", asset.ObjectKey, 2), "")
		require.Equal(t, http.StatusOK, status)
		require.EqualValues(t, 1, webhookCalls.Load())
		require.Contains(t, logs.String(), "飞书审核告警发送失败")
		require.Contains(t, logs.String(), "alert_kind=verdict")

		var records int64
		require.NoError(t, gdb.Model(&model.ModerationRecord{}).
			Where("provider_job_id = ?", "channel-fail-open").Count(&records).Error)
		require.EqualValues(t, 1, records, "告警通道错误不得回滚已提交的审核事实")
	})

	t.Run("post queue count drives cooldown and reset", func(t *testing.T) {
		testPostLevelBacklogSuppression(t, gdb, database)
	})
}

func moderationAlertingEngine(
	cfg appconfig.Config,
	database *dbinfra.DB,
	alerter service.ModerationAlerter,
	log *slog.Logger,
) *server.Hertz {
	engine := server.New(
		server.WithHandleMethodNotAllowed(true),
		hertzconfig.Option{F: func(_ *hertzconfig.Options) {}},
	)
	router.Register(engine, router.Deps{
		Config: cfg, DB: database, Log: log,
		ContentModerator: service.DirectPassContentModerator{},
		ImageStorage:     newFakeImageStorage(), ImageModerator: service.DirectPassImageModerator{},
		ModerationAlerter: alerter,
	})
	return engine
}

func alertsByKind(
	alerts []service.ModerationAlert,
	kind service.ModerationAlertKind,
) []service.ModerationAlert {
	filtered := make([]service.ModerationAlert, 0)
	for _, alert := range alerts {
		if alert.EffectiveKind() == kind {
			filtered = append(filtered, alert)
		}
	}
	return filtered
}

func installModerationInsertFailure(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	require.NoError(t, gdb.Exec(`
		CREATE FUNCTION test_fail_moderation_alert_insert() RETURNS trigger
		LANGUAGE plpgsql AS $fn$
		BEGIN
			IF NEW.provider_job_id = 'forced-internal-alert' THEN
				RAISE EXCEPTION 'forced moderation callback insert failure';
			END IF;
			RETURN NEW;
		END;
		$fn$;
		CREATE TRIGGER trg_test_fail_moderation_alert_insert
		BEFORE INSERT ON moderation_records
		FOR EACH ROW EXECUTE FUNCTION test_fail_moderation_alert_insert();
	`).Error)
}

func removeModerationInsertFailure(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	require.NoError(t, gdb.Exec(`
		DROP TRIGGER IF EXISTS trg_test_fail_moderation_alert_insert ON moderation_records;
		DROP FUNCTION IF EXISTS test_fail_moderation_alert_insert();
	`).Error)
}

func installModerationCommitFailure(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	require.NoError(t, gdb.Exec(`
		CREATE FUNCTION test_fail_moderation_alert_commit() RETURNS trigger
		LANGUAGE plpgsql AS $fn$
		BEGIN
			IF NEW.provider_job_id = 'forced-commit-alert' THEN
				RAISE EXCEPTION 'forced moderation callback commit failure';
			END IF;
			RETURN NEW;
		END;
		$fn$;
		CREATE CONSTRAINT TRIGGER trg_test_fail_moderation_alert_commit
		AFTER INSERT ON moderation_records
		DEFERRABLE INITIALLY DEFERRED
		FOR EACH ROW EXECUTE FUNCTION test_fail_moderation_alert_commit();
	`).Error)
}

func removeModerationCommitFailure(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	require.NoError(t, gdb.Exec(`
		DROP TRIGGER IF EXISTS trg_test_fail_moderation_alert_commit ON moderation_records;
		DROP FUNCTION IF EXISTS test_fail_moderation_alert_commit();
	`).Error)
}

func testPostLevelBacklogSuppression(t *testing.T, gdb *gorm.DB, database *dbinfra.DB) {
	t.Helper()
	fixtures := testutil.NewFixtures(t, gdb)
	author := fixtures.CreateUser()
	firstImage := fixtures.CreateImage(author.ID, func(image *model.ImageAsset) {
		image.Moderation = model.ModerationStatusReview
	})
	secondImage := fixtures.CreateImage(author.ID, func(image *model.ImageAsset) {
		image.Moderation = model.ModerationStatusReview
	})
	post := fixtures.CreatePost(author.ID,
		testutil.WithPostStatus(model.PostStatusPending),
		testutil.WithPostImages(firstImage, secondImage),
	)
	contentRevision := int32(1)
	records := []model.ModerationRecord{
		{
			PostID: &post.Post.ID, ContentRevision: &contentRevision, Scene: model.ModerationSceneText,
			Provider: model.ModerationProviderTencentCI,
			Verdict:  model.ModerationVerdictReview, Labels: pq.StringArray{"text"},
		},
		{
			ImageAssetID: &firstImage.ID, Scene: model.ModerationSceneImage,
			Provider: model.ModerationProviderTencentCI,
			Verdict:  model.ModerationVerdictReview, Labels: pq.StringArray{"image-a"},
		},
		{
			ImageAssetID: &secondImage.ID, Scene: model.ModerationSceneImage,
			Provider: model.ModerationProviderTencentCI,
			Verdict:  model.ModerationVerdictReview, Labels: pq.StringArray{"image-b"},
		},
	}
	require.NoError(t, gdb.Create(&records).Error)

	capture := &moderationAlertCapture{}
	monitor := service.NewModerationAlertService(alerting.AfterCommit(capture))
	startedAt := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.UTC)
	first := runBacklogCheck(t, database, monitor, startedAt)
	require.EqualValues(t, 1, first.QueueDepth,
		"同一帖子的正文和两张 review 图片必须合并为一个队列条目")
	require.True(t, first.AlertScheduled)
	require.Len(t, alertsByKind(capture.all(), service.ModerationAlertKindReviewBacklog), 1)

	second := runBacklogCheck(t, database, monitor, startedAt.Add(10*time.Minute))
	require.EqualValues(t, 1, second.QueueDepth)
	require.False(t, second.AlertScheduled)
	require.Len(t, alertsByKind(capture.all(), service.ModerationAlertKindReviewBacklog), 1,
		"持续积压在冷却窗口内不得重复告警")

	third := runBacklogCheck(t, database, monitor, startedAt.Add(70*time.Minute))
	require.True(t, third.AlertScheduled, "持续积压超过冷却窗口后应允许提醒")
	require.Len(t, alertsByKind(capture.all(), service.ModerationAlertKindReviewBacklog), 2)

	require.NoError(t, gdb.Model(&model.Post{}).Where("id = ?", post.Post.ID).
		UpdateColumn("status", model.PostStatusApproved).Error)
	below := runBacklogCheck(t, database, monitor, startedAt.Add(75*time.Minute))
	require.Zero(t, below.QueueDepth)
	require.False(t, below.AlertScheduled)

	require.NoError(t, gdb.Model(&model.Post{}).Where("id = ?", post.Post.ID).
		UpdateColumn("status", model.PostStatusPending).Error)
	recrossed := runBacklogCheck(t, database, monitor, startedAt.Add(80*time.Minute))
	require.EqualValues(t, 1, recrossed.QueueDepth)
	require.True(t, recrossed.AlertScheduled,
		"回落后再次达到阈值必须立即告警，不受上一次冷却时间压制")
	require.Len(t, alertsByKind(capture.all(), service.ModerationAlertKindReviewBacklog), 3)
}

func runBacklogCheck(
	t *testing.T,
	database *dbinfra.DB,
	monitor *service.ModerationAlertService,
	now time.Time,
) service.ReviewBacklogCheckResult {
	t.Helper()
	ctx, queue := dbinfra.WithAfterCommitQueue(context.Background())
	result := service.ReviewBacklogCheckResult{}
	err := database.RunInTx(ctx, func(txCtx context.Context) error {
		var checkErr error
		result, checkErr = monitor.CheckReviewBacklog(txCtx, service.ReviewBacklogCheckOptions{
			Threshold: 1, Cooldown: time.Hour, Now: now,
		})
		return checkErr
	})
	require.NoError(t, err)
	require.Empty(t, queue.Run(ctx))
	return result
}
