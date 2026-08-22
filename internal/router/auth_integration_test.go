package router_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	hertzconfig "github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	appconfig "github.com/jingyijun/danshi_backend_go/internal/config"
	dbinfra "github.com/jingyijun/danshi_backend_go/internal/infra/db"
	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/jwtx"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/pagination"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/ptime"
	"github.com/jingyijun/danshi_backend_go/internal/repository"
	"github.com/jingyijun/danshi_backend_go/internal/router"
	routermiddleware "github.com/jingyijun/danshi_backend_go/internal/router/middleware"
	"github.com/jingyijun/danshi_backend_go/internal/service"
	"github.com/jingyijun/danshi_backend_go/internal/testutil"
)

const (
	integrationSecret = "integration-secret-longer-than-thirty-two-bytes"
	authFlowEmail     = "auth-flow@fdueat.com"
)

type captureEmailSender = testutil.MockEmailSender

func newCaptureEmailSender() *captureEmailSender {
	return testutil.NewMockEmailSender()
}

type testAPIResponse struct {
	Code      int             `json:"code"`
	Message   string          `json:"message"`
	ErrorCode apierr.BizCode  `json:"error_code"`
	Data      json.RawMessage `json:"data"`
}

func TestRepositoryAndAuthAgainstPostgres(t *testing.T) {
	gdb, database := openAuthPostgres(t)
	cfg := authTestConfig()
	sender := newCaptureEmailSender()
	engine := authTestEngine(cfg, database, sender)

	t.Run("auth route inventory", func(t *testing.T) {
		testAuthRouteInventory(t, engine)
	})

	t.Run("repository base", func(t *testing.T) {
		testRepositoryBase(t, database, gdb)
	})

	t.Run("commit error cannot commit 5xx", func(t *testing.T) {
		testCommitError5xxRollback(t, database, gdb)
	})

	t.Run("post commit callbacks follow transaction outcome", func(t *testing.T) {
		testPostCommitCallbacks(t, database, gdb)
	})

	t.Run("verification failure rolls back", func(t *testing.T) {
		failingSender := testutil.NewMockEmailSender()
		failingSender.SetDefault(testutil.EmailFailure(errors.New("test delivery failure")))
		failingEngine := authTestEngine(cfg, database, failingSender)
		status, response, _ := performJSON(t, failingEngine, http.MethodPost,
			"/api/v2/auth/email-verification-codes", map[string]any{"email": "delivery-fail@fdueat.com"}, "")
		require.Equal(t, http.StatusServiceUnavailable, status)
		require.Equal(t, apierr.BizServiceUnavailable, response.ErrorCode)
		require.Equal(t, "验证码暂时无法发送，请稍后再试", response.Message)

		var count int64
		require.NoError(t, gdb.Model(&model.EmailVerificationCode{}).
			Where("email = ?", "delivery-fail@fdueat.com").Count(&count).Error)
		require.Zero(t, count, "发信失败时验证码行和发送计数必须一并回滚")
	})

	t.Run("verification timeout returns 503 and rolls back", func(t *testing.T) {
		timeoutSender := testutil.NewMockEmailSender()
		timeoutSender.SetDefault(testutil.EmailFailure(context.DeadlineExceeded))
		timeoutEngine := authTestEngine(cfg, database, timeoutSender)
		status, response, _ := performJSON(t, timeoutEngine, http.MethodPost,
			"/api/v2/auth/email-verification-codes", map[string]any{"email": "timeout@fdueat.com"}, "")
		require.Equal(t, http.StatusServiceUnavailable, status)
		require.Equal(t, apierr.BizServiceUnavailable, response.ErrorCode)

		var count int64
		require.NoError(t, gdb.Model(&model.EmailVerificationCode{}).
			Where("email = ?", "timeout@fdueat.com").Count(&count).Error)
		require.Zero(t, count, "SES 超时时验证码行和发送计数必须一并回滚")
	})

	t.Run("verification in-flight limit precedes uow", func(t *testing.T) {
		testVerificationInFlightLimit(t, cfg, database, gdb)
	})

	t.Run("register login refresh logout", func(t *testing.T) {
		email := authFlowEmail
		sendCode(t, engine, email)
		register := registerUser(t, engine, sender, email, "注册设备")
		require.NotEmpty(t, register.Token)
		require.NotEmpty(t, register.RefreshToken)
		status, response, _ := performJSON(t, engine, http.MethodGet, "/api/v2/auth/me", nil, register.Token)
		require.Equal(t, http.StatusOK, status)
		require.Equal(t, "请求成功", response.Message)
		var me struct {
			User service.UserView `json:"user"`
		}
		decodeData(t, response, &me)
		require.Equal(t, register.User, me.User)

		var sessionCount int64
		require.NoError(t, gdb.Model(&model.UserSession{}).Count(&sessionCount).Error)
		require.EqualValues(t, 1, sessionCount, "注册应在同一事务创建首个会话")
		var challenge model.EmailVerificationCode
		require.NoError(t, gdb.Where("email = ?", email).First(&challenge).Error)
		require.NotNil(t, challenge.ConsumedAt, "用户、会话与验证码消费必须一并提交")
		require.Len(t, challenge.CodeDigest, 64)
		require.NotEqual(t, capturedCode(t, sender, email), challenge.CodeDigest, "验证码明文不得落库")
		claims, err := jwtx.NewCodec(integrationSecret).Parse(register.RefreshToken, jwtx.TypeRefresh)
		require.NoError(t, err)
		var registeredSession model.UserSession
		require.NoError(t, gdb.First(&registeredSession, uint64(claims.SessionID)).Error)
		require.Equal(t, jwtx.Digest(register.RefreshToken), registeredSession.RefreshTokenDigest)
		require.NotEqual(t, register.RefreshToken, registeredSession.RefreshTokenDigest)

		login := loginUser(t, engine, "登录设备 A")
		status, response, _ = performJSON(t, engine, http.MethodPost,
			"/api/v2/auth/refresh", map[string]any{"refresh_token": login.RefreshToken}, "")
		require.Equal(t, http.StatusOK, status)
		var refreshed service.TokenResult
		decodeData(t, response, &refreshed)
		require.Equal(t, login.RefreshToken, refreshed.RefreshToken, "refresh token 不得轮转")
		require.NotEmpty(t, refreshed.Token)

		status, _, _ = performJSON(t, engine, http.MethodPost, "/api/v2/auth/logout", nil, refreshed.Token)
		require.Equal(t, http.StatusOK, status)
		assertUnauthorized(t, engine, http.MethodGet, "/api/v2/auth/sessions", nil, login.Token)
		assertUnauthorized(t, engine, http.MethodPost, "/api/v2/auth/refresh",
			map[string]any{"refresh_token": login.RefreshToken}, "")
	})

	t.Run("logout all", func(t *testing.T) {
		first := loginUser(t, engine, "全端设备 A")
		second := loginUser(t, engine, "全端设备 B")
		status, _, _ := performJSON(t, engine, http.MethodPost, "/api/v2/auth/logout-all", nil, first.Token)
		require.Equal(t, http.StatusOK, status)
		assertUnauthorized(t, engine, http.MethodGet, "/api/v2/auth/sessions", nil, first.Token)
		assertUnauthorized(t, engine, http.MethodGet, "/api/v2/auth/sessions", nil, second.Token)
		assertUnauthorized(t, engine, http.MethodPost, "/api/v2/auth/refresh",
			map[string]any{"refresh_token": second.RefreshToken}, "")
	})

	t.Run("sessions and kick device", func(t *testing.T) {
		controller := loginUser(t, engine, "当前手机")
		other := loginUser(t, engine, "陌生平板")
		claims, err := jwtx.NewCodec(integrationSecret).Parse(other.Token, jwtx.TypeAccess)
		require.NoError(t, err)

		status, response, _ := performJSON(t, engine, http.MethodGet, "/api/v2/auth/sessions", nil, controller.Token)
		require.Equal(t, http.StatusOK, status)
		var listed struct {
			Sessions []service.SessionView `json:"sessions"`
		}
		decodeData(t, response, &listed)
		require.GreaterOrEqual(t, len(listed.Sessions), 2)
		require.True(t, sessionLabelPresent(listed.Sessions, "当前手机"))
		require.True(t, sessionLabelPresent(listed.Sessions, "陌生平板"))

		path := "/api/v2/auth/sessions/" + strconv.FormatInt(claims.SessionID, 10)
		status, _, _ = performJSON(t, engine, http.MethodDelete, path, nil, controller.Token)
		require.Equal(t, http.StatusOK, status)
		assertUnauthorized(t, engine, http.MethodGet, "/api/v2/auth/sessions", nil, other.Token)
		status, _, _ = performJSON(t, engine, http.MethodGet, "/api/v2/auth/sessions", nil, controller.Token)
		require.Equal(t, http.StatusOK, status)
	})

	t.Run("domain banned and rate limits", func(t *testing.T) {
		testDomainRejection(t, engine)
		testBannedLogin(t, engine, gdb)
		testSendRateLimit(t, engine, sender)
		testRegisteredEmailRateLimit(t, engine, sender, gdb)
		testFailedAttemptPersistence(t, engine, sender, gdb)
	})

	t.Run("registration validation and verification states", func(t *testing.T) {
		testRegistrationValidation(t, engine, sender, gdb)
		testVerificationStates(t, engine, sender, gdb)
	})

	t.Run("verification cooldown quota and rolling window", func(t *testing.T) {
		testVerificationRateBoundaries(t, cfg, database, gdb)
	})

	t.Run("login account states", func(t *testing.T) {
		testLoginAccountStates(t, engine, sender, gdb)
	})

	t.Run("session ownership expiry and ban lifecycle", func(t *testing.T) {
		testSessionSecurityLifecycle(t, cfg, engine, sender, gdb)
	})

	t.Run("concurrent registration is single winner", func(t *testing.T) {
		testConcurrentRegistration(t, engine, sender, gdb)
	})
}

func testAuthRouteInventory(t *testing.T, engine *server.Hertz) {
	t.Helper()
	var operations []string
	for _, route := range engine.Routes() {
		if strings.HasPrefix(route.Path, "/api/v2/auth/") {
			operations = append(operations, route.Method+" "+route.Path)
		}
	}
	require.ElementsMatch(t, []string{
		"POST /api/v2/auth/email-verification-codes",
		"POST /api/v2/auth/register",
		"POST /api/v2/auth/login",
		"POST /api/v2/auth/refresh",
		"GET /api/v2/auth/me",
		"POST /api/v2/auth/logout",
		"POST /api/v2/auth/logout-all",
		"GET /api/v2/auth/sessions",
		"DELETE /api/v2/auth/sessions/:id",
	}, operations)
}

func testVerificationInFlightLimit(
	t *testing.T,
	cfg appconfig.Config,
	database *dbinfra.DB,
	gdb *gorm.DB,
) {
	t.Helper()
	const maxInFlight = 5

	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	previousMaxOpen := sqlDB.Stats().MaxOpenConnections
	sqlDB.SetMaxOpenConns(maxInFlight)
	t.Cleanup(func() { sqlDB.SetMaxOpenConns(previousMaxOpen) })

	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)

	blockingSender := testutil.NewMockEmailSender()
	blockingSender.SetDefault(testutil.EmailBlocked(release))
	engine := authTestEngine(cfg, database, blockingSender)
	results := make(chan asyncRequestResult, maxInFlight)
	for index := range maxInFlight {
		email := "in-flight-" + strconv.Itoa(index) + "@fdueat.com"
		go func() {
			status, response, raw, requestErr := performJSONRequest(
				engine,
				http.MethodPost,
				"/api/v2/auth/email-verification-codes",
				map[string]any{"email": email},
				"",
			)
			results <- asyncRequestResult{status: status, response: response, raw: raw, err: requestErr}
		}()
	}

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer waitCancel()
	require.True(t, blockingSender.WaitForAttempts(waitCtx, maxInFlight),
		"5 个发信请求未能全部进入阻塞 sender")
	inUseBeforeReject := sqlDB.Stats().InUse
	require.Equal(t, maxInFlight, inUseBeforeReject, "阻塞 sender 时每个请求都应持有一个事务连接")

	sixthResult := make(chan asyncRequestResult, 1)
	go func() {
		status, response, raw, requestErr := performJSONRequest(
			engine,
			http.MethodPost,
			"/api/v2/auth/email-verification-codes",
			map[string]any{"email": "in-flight-rejected@fdueat.com"},
			"",
		)
		sixthResult <- asyncRequestResult{status: status, response: response, raw: raw, err: requestErr}
	}()

	var rejected asyncRequestResult
	select {
	case rejected = <-sixthResult:
	case <-time.After(500 * time.Millisecond):
		require.FailNow(t, "第 6 个请求没有立即拒绝，可能在等待数据库连接")
	}
	require.NoError(t, rejected.err)
	require.Equal(t, http.StatusTooManyRequests, rejected.status)
	require.Equal(t, apierr.BizVerifyCodeBusy, rejected.response.ErrorCode)
	require.Equal(t, "2", string(rejected.raw.Header().Peek("Retry-After")))
	require.Equal(t, inUseBeforeReject, sqlDB.Stats().InUse,
		"第 6 个请求被拒时不得再借用数据库连接")

	unblock()
	for range maxInFlight {
		result := <-results
		require.NoError(t, result.err)
		require.Equal(t, http.StatusOK, result.status)
	}
	status, response, _ := performJSON(t, engine, http.MethodPost,
		"/api/v2/auth/email-verification-codes", map[string]any{"email": "in-flight-recovered@fdueat.com"}, "")
	require.Equal(t, http.StatusOK, status, "释放在途许可后请求应恢复，response=%s", response.Message)
}

type asyncRequestResult struct {
	status   int
	response testAPIResponse
	raw      *ut.ResponseRecorder
	err      error
}

func testCommitError5xxRollback(t *testing.T, database *dbinfra.DB, gdb *gorm.DB) {
	t.Helper()
	engine := server.New(hertzconfig.Option{F: func(_ *hertzconfig.Options) {}})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine.Use(routermiddleware.ErrorHandler(log))
	engine.Use(routermiddleware.UnitOfWork(database, log))
	email := "commit-error-500@fdueat.com"
	engine.POST("/commit-error-500", func(ctx context.Context, c *app.RequestContext) {
		user := &model.User{
			Email: email, PasswordHash: "$2b$12$test", Name: "rollback", Role: model.UserRoleUser,
		}
		if err := dbinfra.FromContext(ctx).Create(user).Error; err != nil {
			routermiddleware.Fail(ctx, c, apierr.Internal(err))
			return
		}
		routermiddleware.CommitError(c)
		routermiddleware.Fail(ctx, c, apierr.Internal(errors.New("forced failure after write")))
	})

	status, _, _ := performJSON(t, engine, http.MethodPost, "/commit-error-500", nil, "")
	require.Equal(t, http.StatusInternalServerError, status)
	var count int64
	require.NoError(t, gdb.Model(&model.User{}).Where("email = ?", email).Count(&count).Error)
	require.Zero(t, count, "即使置了 CommitError，5xx 也必须回滚事务")
}

func testPostCommitCallbacks(t *testing.T, database *dbinfra.DB, gdb *gorm.DB) {
	t.Helper()
	engine := server.New(hertzconfig.Option{F: func(_ *hertzconfig.Options) {}})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine.Use(routermiddleware.ErrorHandler(log))
	engine.Use(routermiddleware.UnitOfWork(database, log))

	var callbackErr error
	var committedVisible bool
	committedEmail := "post-commit-success@fdueat.com"
	engine.POST("/post-commit-success", func(ctx context.Context, c *app.RequestContext) {
		user := &model.User{
			Email: committedEmail, PasswordHash: "$2b$12$test", Name: "commit", Role: model.UserRoleUser,
		}
		if err := dbinfra.FromContext(ctx).Create(user).Error; err != nil {
			routermiddleware.Fail(ctx, c, apierr.Internal(err))
			return
		}
		if !dbinfra.AfterCommit(ctx, func(context.Context) {
			var count int64
			callbackErr = gdb.Model(&model.User{}).Where("email = ?", committedEmail).Count(&count).Error
			committedVisible = count == 1
		}) {
			routermiddleware.Fail(ctx, c, apierr.Internal(errors.New("missing post-commit queue")))
			return
		}
		c.JSON(http.StatusOK, map[string]any{"ok": true})
	})

	rolledBackCalled := false
	rolledBackEmail := "post-commit-rollback@fdueat.com"
	engine.POST("/post-commit-rollback", func(ctx context.Context, c *app.RequestContext) {
		user := &model.User{
			Email: rolledBackEmail, PasswordHash: "$2b$12$test", Name: "rollback", Role: model.UserRoleUser,
		}
		if err := dbinfra.FromContext(ctx).Create(user).Error; err != nil {
			routermiddleware.Fail(ctx, c, apierr.Internal(err))
			return
		}
		if !dbinfra.AfterCommit(ctx, func(context.Context) {
			rolledBackCalled = true
		}) {
			routermiddleware.Fail(ctx, c, apierr.Internal(errors.New("missing post-commit queue")))
			return
		}
		routermiddleware.Fail(ctx, c, apierr.Forbidden(apierr.BizPermissionDenied, "forced rollback"))
	})

	status, _, _ := performJSON(t, engine, http.MethodPost, "/post-commit-success", nil, "")
	require.Equal(t, http.StatusOK, status)
	require.NoError(t, callbackErr)
	require.True(t, committedVisible, "回调执行时事务写入必须已对其它连接可见")

	status, _, _ = performJSON(t, engine, http.MethodPost, "/post-commit-rollback", nil, "")
	require.Equal(t, http.StatusForbidden, status)
	require.False(t, rolledBackCalled, "事务回滚时不得执行提交后回调")
	var count int64
	require.NoError(t, gdb.Model(&model.User{}).Where("email = ?", rolledBackEmail).Count(&count).Error)
	require.Zero(t, count)
}

func testRepositoryBase(t *testing.T, database *dbinfra.DB, gdb *gorm.DB) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	users := []model.User{
		{Email: "repo-base-a@fdueat.com", PasswordHash: "$2b$12$test", Name: "A", Role: model.UserRoleUser},
		{Email: "repo-base-b@fdueat.com", PasswordHash: "$2b$12$test", Name: "B", Role: model.UserRoleUser},
		{Email: "repo-base-deleted@fdueat.com", PasswordHash: "$2b$12$test", Name: "D", Role: model.UserRoleUser, DeletedAt: &now},
	}
	for index := range users {
		require.NoError(t, gdb.Create(&users[index]).Error)
	}

	err := database.RunInTx(context.Background(), func(ctx context.Context) error {
		params := pagination.Params{Page: 1, Limit: 10}
		scope := func(query *gorm.DB) *gorm.DB {
			return query.Where("email LIKE ?", "repo-base-%").Order("id")
		}
		active, meta, err := repository.FindPage[model.User](
			ctx, params, repository.QueryOptions{}, scope,
		)
		require.NoError(t, err)
		require.Len(t, active, 2)
		require.EqualValues(t, 2, meta.Total)

		all, meta, err := repository.FindPage[model.User](
			ctx, params, repository.QueryOptions{IncludeDeleted: true}, scope,
		)
		require.NoError(t, err)
		require.Len(t, all, 3)
		require.EqualValues(t, 3, meta.Total)

		follow := &model.Follow{FollowerID: users[0].ID, FollowingID: users[1].ID}
		require.NoError(t, repository.UpsertAssociation(ctx, follow))
		require.NoError(t, repository.UpsertAssociation(ctx, follow))
		require.NoError(t, repository.DeleteAssociation(ctx, follow))
		require.NoError(t, repository.DeleteAssociation(ctx, follow))
		require.NoError(t, repository.UpsertAssociation(ctx, follow), "取消后必须可以再次创建")
		return nil
	})
	require.NoError(t, err)

	mapped := repository.ToAPIError(repository.NormalizeError(gorm.ErrRecordNotFound), apierr.BizNotFound, "资源")
	require.Equal(t, http.StatusNotFound, apierr.As(mapped).Status)
}

func testDomainRejection(t *testing.T, engine *server.Hertz) {
	t.Helper()
	status, response, _ := performJSON(t, engine, http.MethodPost, "/api/v2/auth/register", map[string]any{
		"email": "outside@example.com", "password": "password-123", "verification_code": "000000",
	}, "")
	require.Equal(t, http.StatusUnprocessableEntity, status)
	var data struct {
		Errors []apierr.FieldError `json:"errors"`
	}
	decodeData(t, response, &data)
	require.Len(t, data.Errors, 1)
	require.Equal(t, "email", data.Errors[0].Field)
	require.Equal(t, apierr.FieldInvalidDomain, data.Errors[0].Code)
}

func testBannedLogin(t *testing.T, engine *server.Hertz, gdb *gorm.DB) {
	t.Helper()
	until := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Microsecond)
	reason := "多次发布广告"
	require.NoError(t, gdb.Model(&model.User{}).Where("email = ?", authFlowEmail).Updates(map[string]any{
		"ban_is_permanent": false, "banned_until": until, "ban_reason": reason,
	}).Error)
	status, response, _ := performJSON(t, engine, http.MethodPost, "/api/v2/auth/login", map[string]any{
		"email": authFlowEmail, "password": "password-123",
	}, "")
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, apierr.BizAccountBanned, response.ErrorCode)
	require.Contains(t, response.Message, reason)
	require.Contains(t, response.Message, ptime.Format(until))

	require.NoError(t, gdb.Model(&model.User{}).Where("email = ?", authFlowEmail).Updates(map[string]any{
		"ban_is_permanent": false, "banned_until": nil, "ban_reason": nil, "banned_by": nil,
	}).Error)
}

func testSendRateLimit(t *testing.T, engine *server.Hertz, sender *captureEmailSender) {
	t.Helper()
	email := "rate-limit@fdueat.com"
	sendCode(t, engine, email)
	status, response, raw := performJSON(t, engine, http.MethodPost,
		"/api/v2/auth/email-verification-codes", map[string]any{"email": email}, "")
	require.Equal(t, http.StatusTooManyRequests, status)
	require.Equal(t, apierr.BizVerifyCodeTooMany, response.ErrorCode)
	require.NotEmpty(t, raw.Header().Peek("Retry-After"))
	sender.RequireDeliveryCount(t, email, 1)
}

func testRegisteredEmailRateLimit(
	t *testing.T,
	engine *server.Hertz,
	sender *captureEmailSender,
	gdb *gorm.DB,
) {
	t.Helper()
	email := "registered-rate-limit@fdueat.com"
	require.NoError(t, gdb.Create(&model.User{
		Email: email, PasswordHash: "$2b$12$test", Name: "registered", Role: model.UserRoleUser,
	}).Error)

	status, _, _ := performJSON(t, engine, http.MethodPost,
		"/api/v2/auth/email-verification-codes", map[string]any{"email": email}, "")
	require.Equal(t, http.StatusOK, status)
	status, response, raw := performJSON(t, engine, http.MethodPost,
		"/api/v2/auth/email-verification-codes", map[string]any{"email": email}, "")
	require.Equal(t, http.StatusTooManyRequests, status)
	require.Equal(t, apierr.BizVerifyCodeTooMany, response.ErrorCode)
	require.NotEmpty(t, raw.Header().Peek("Retry-After"))
	sender.RequireNoDelivery(t, email)

	var challenge model.EmailVerificationCode
	require.NoError(t, gdb.Where("email = ?", email).First(&challenge).Error)
	require.EqualValues(t, 1, challenge.SendCount)
	require.Len(t, challenge.CodeDigest, 64)
	require.NotEqual(t, strings.Repeat("0", 64), challenge.CodeDigest)
}

func testFailedAttemptPersistence(
	t *testing.T,
	engine *server.Hertz,
	sender *captureEmailSender,
	gdb *gorm.DB,
) {
	t.Helper()
	email := "failed-attempt@fdueat.com"
	sendCode(t, engine, email)
	for range 5 {
		status, response, _ := performJSON(t, engine, http.MethodPost, "/api/v2/auth/register", map[string]any{
			"email": email, "password": "password-123", "verification_code": "999999",
		}, "")
		require.Equal(t, http.StatusBadRequest, status)
		require.Equal(t, apierr.BizVerifyCodeInvalid, response.ErrorCode)
	}
	var challenge model.EmailVerificationCode
	require.NoError(t, gdb.Where("email = ?", email).First(&challenge).Error)
	require.EqualValues(t, 5, challenge.FailedAttempts, "4xx 不能回滚验证码失败安全计数")

	status, response, _ := performJSON(t, engine, http.MethodPost, "/api/v2/auth/register", map[string]any{
		"email": email, "password": "password-123", "verification_code": capturedCode(t, sender, email),
	}, "")
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, apierr.BizVerifyCodeInvalid, response.ErrorCode)
	var users int64
	require.NoError(t, gdb.Model(&model.User{}).Where("email = ?", email).Count(&users).Error)
	require.Zero(t, users, "达到失败次数上限后，即使验证码正确也不能注册")
}

func testRegistrationValidation(
	t *testing.T,
	engine *server.Hertz,
	sender *captureEmailSender,
	gdb *gorm.DB,
) {
	t.Helper()
	status, response, _ := performJSON(t, engine, http.MethodPost, "/api/v2/auth/register", map[string]any{
		"email": authFlowEmail, "password": "password-123", "verification_code": "000000",
	}, "")
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, apierr.BizEmailTaken, response.ErrorCode)

	deletedAt := time.Now().UTC()
	deletedEmail := "registration-deleted@fdueat.com"
	testutil.NewFixtures(t, gdb).CreateUser(
		func(user *model.User) { user.Email = deletedEmail },
		testutil.WithDeletedUser(deletedAt),
	)
	status, response, _ = performJSON(t, engine, http.MethodPost, "/api/v2/auth/register", map[string]any{
		"email": deletedEmail, "password": "password-123", "verification_code": "000000",
	}, "")
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, apierr.BizEmailTaken, response.ErrorCode,
		"软删除行仍占用大小写不敏感邮箱唯一键")

	cases := []struct {
		name      string
		payload   map[string]any
		field     string
		fieldCode apierr.FieldCode
	}{
		{
			name: "password too short",
			payload: map[string]any{
				"email": "short-password@fdueat.com", "password": "1234567",
				"verification_code": "000000",
			},
			field: "password", fieldCode: apierr.FieldTooShort,
		},
		{
			name: "password exceeds bcrypt bytes",
			payload: map[string]any{
				"email": "long-password@fdueat.com", "password": strings.Repeat("界", 25),
				"verification_code": "000000",
			},
			field: "password", fieldCode: apierr.FieldTooLong,
		},
		{
			name: "name exceeds unicode rune limit",
			payload: map[string]any{
				"email": "long-name@fdueat.com", "password": "password-123",
				"verification_code": "000000", "name": strings.Repeat("界", 101),
			},
			field: "name", fieldCode: apierr.FieldTooLong,
		},
		{
			name: "invalid registration gender",
			payload: map[string]any{
				"email": "invalid-gender@fdueat.com", "password": "password-123",
				"verification_code": "000000", "gender": model.GenderOther,
			},
			field: "gender", fieldCode: apierr.FieldInvalidEnum,
		},
		{
			name: "missing verification code",
			payload: map[string]any{
				"email": "missing-code@fdueat.com", "password": "password-123",
			},
			field: "verification_code", fieldCode: apierr.FieldRequired,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, response, _ := performJSON(
				t, engine, http.MethodPost, "/api/v2/auth/register", testCase.payload, "",
			)
			require.Equal(t, http.StatusUnprocessableEntity, status)
			requireAuthFieldError(t, response, testCase.field, testCase.fieldCode)
		})
	}

	status, response, _ = performJSON(t, engine, http.MethodPost,
		"/api/v2/auth/email-verification-codes", map[string]any{"email": ""}, "")
	require.Equal(t, http.StatusUnprocessableEntity, status)
	requireAuthFieldError(t, response, "email", apierr.FieldInvalidFormat)
	status, response, _ = performJSON(t, engine, http.MethodPost, "/api/v2/auth/login", map[string]any{
		"email": authFlowEmail, "password": "",
	}, "")
	require.Equal(t, http.StatusUnprocessableEntity, status)
	requireAuthFieldError(t, response, "password", apierr.FieldRequired)

	boundaryEmail := "unicode-boundary@fdueat.com"
	sendCode(t, engine, boundaryEmail)
	boundaryName := strings.Repeat("界", 100)
	boundaryPassword := strings.Repeat("密", 24)
	status, response, _ = performJSON(t, engine, http.MethodPost, "/api/v2/auth/register", map[string]any{
		"email": boundaryEmail, "password": boundaryPassword,
		"verification_code": capturedCode(t, sender, boundaryEmail),
		"name":              boundaryName, "gender": model.GenderMale,
	}, "")
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
	var boundaryUser model.User
	require.NoError(t, gdb.Where("email = ?", boundaryEmail).First(&boundaryUser).Error)
	require.Equal(t, boundaryName, boundaryUser.Name)
}

func testVerificationStates(
	t *testing.T,
	engine *server.Hertz,
	sender *captureEmailSender,
	gdb *gorm.DB,
) {
	t.Helper()
	testState := func(t *testing.T, email string, mutate func(*model.EmailVerificationCode)) {
		t.Helper()
		sendCode(t, engine, email)
		var challenge model.EmailVerificationCode
		require.NoError(t, gdb.Where("email = ?", email).First(&challenge).Error)
		mutate(&challenge)
		require.NoError(t, gdb.Model(&challenge).Updates(map[string]any{
			"expires_at": challenge.ExpiresAt, "consumed_at": challenge.ConsumedAt,
		}).Error)

		status, response, _ := performJSON(t, engine, http.MethodPost, "/api/v2/auth/register", map[string]any{
			"email": email, "password": "password-123",
			"verification_code": capturedCode(t, sender, email),
		}, "")
		require.Equal(t, http.StatusBadRequest, status)
		require.Equal(t, apierr.BizVerifyCodeInvalid, response.ErrorCode)
		var users int64
		require.NoError(t, gdb.Model(&model.User{}).Where("email = ?", email).Count(&users).Error)
		require.Zero(t, users)
	}

	t.Run("expired", func(t *testing.T) {
		testState(t, "expired-code@fdueat.com", func(challenge *model.EmailVerificationCode) {
			challenge.ExpiresAt = time.Now().UTC().Add(-time.Minute)
		})
	})
	t.Run("consumed", func(t *testing.T) {
		testState(t, "consumed-code@fdueat.com", func(challenge *model.EmailVerificationCode) {
			now := time.Now().UTC()
			challenge.ConsumedAt = &now
		})
	})
}

func testVerificationRateBoundaries(
	t *testing.T,
	cfg appconfig.Config,
	database *dbinfra.DB,
	gdb *gorm.DB,
) {
	t.Helper()
	email := "verification-boundaries@fdueat.com"
	sender := testutil.NewMockEmailSender()
	engine := authTestEngine(cfg, database, sender)
	sendCode(t, engine, email)

	setTimes := func(lastSentAt, windowStartedAt time.Time, sendCount int32) {
		require.NoError(t, gdb.Model(&model.EmailVerificationCode{}).Where("email = ?", email).
			Updates(map[string]any{
				"last_sent_at": lastSentAt, "send_window_started_at": windowStartedAt,
				"send_count": sendCount,
			}).Error)
	}
	now := time.Now().UTC()
	setTimes(now.Add(-59*time.Second), now, 1)
	status, response, raw := performJSON(t, engine, http.MethodPost,
		"/api/v2/auth/email-verification-codes", map[string]any{"email": email}, "")
	require.Equal(t, http.StatusTooManyRequests, status)
	require.Equal(t, apierr.BizVerifyCodeTooMany, response.ErrorCode)
	require.NotEmpty(t, raw.Header().Peek("Retry-After"))
	sender.RequireDeliveryCount(t, email, 1)

	setTimes(time.Now().UTC().Add(-61*time.Second), time.Now().UTC(), 1)
	sendCode(t, engine, email)
	for sender.DeliveryCount(email) < 5 {
		lastSent := time.Now().UTC().Add(-61 * time.Second)
		require.NoError(t, gdb.Model(&model.EmailVerificationCode{}).Where("email = ?", email).
			Update("last_sent_at", lastSent).Error)
		sendCode(t, engine, email)
	}
	var challenge model.EmailVerificationCode
	require.NoError(t, gdb.Where("email = ?", email).First(&challenge).Error)
	require.EqualValues(t, 5, challenge.SendCount)

	setTimes(time.Now().UTC().Add(-61*time.Second), time.Now().UTC().Add(-59*time.Minute), 5)
	status, response, _ = performJSON(t, engine, http.MethodPost,
		"/api/v2/auth/email-verification-codes", map[string]any{"email": email}, "")
	require.Equal(t, http.StatusTooManyRequests, status)
	require.Equal(t, apierr.BizVerifyCodeTooMany, response.ErrorCode)
	sender.RequireDeliveryCount(t, email, 5)

	setTimes(time.Now().UTC().Add(-61*time.Second), time.Now().UTC().Add(-61*time.Minute), 5)
	sendCode(t, engine, email)
	require.NoError(t, gdb.Where("email = ?", email).First(&challenge).Error)
	require.EqualValues(t, 1, challenge.SendCount, "一小时滚动窗口到期后配额必须重置")
	sender.RequireDeliveryCount(t, email, 6)

	failureEmail := "verification-existing-failure@fdueat.com"
	failureSender := testutil.NewMockEmailSender()
	failureSender.Program(testutil.EmailRule{
		Email: failureEmail, Call: 2,
		Behavior: testutil.EmailFailure(errors.New("SES test 5xx")),
	})
	failureEngine := authTestEngine(cfg, database, failureSender)
	sendCode(t, failureEngine, failureEmail)
	lastSent := time.Now().UTC().Add(-61 * time.Second)
	require.NoError(t, gdb.Model(&model.EmailVerificationCode{}).Where("email = ?", failureEmail).
		Update("last_sent_at", lastSent).Error)
	var before model.EmailVerificationCode
	require.NoError(t, gdb.Where("email = ?", failureEmail).First(&before).Error)
	status, response, _ = performJSON(t, failureEngine, http.MethodPost,
		"/api/v2/auth/email-verification-codes", map[string]any{"email": failureEmail}, "")
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, apierr.BizServiceUnavailable, response.ErrorCode)
	var after model.EmailVerificationCode
	require.NoError(t, gdb.Where("email = ?", failureEmail).First(&after).Error)
	require.Equal(t, before.SendCount, after.SendCount)
	require.Equal(t, before.CodeDigest, after.CodeDigest)
	require.Equal(t, before.LastSentAt, after.LastSentAt,
		"已有验证码行的失败发送也必须完整回滚配额和验证码")
	failureSender.RequireDeliveryCount(t, failureEmail, 1)
}

func testLoginAccountStates(
	t *testing.T,
	engine *server.Hertz,
	sender *captureEmailSender,
	gdb *gorm.DB,
) {
	t.Helper()
	email := "login-states@fdueat.com"
	registerPostTestUser(t, engine, sender, email, "登录状态用户")
	status, _, _ := performJSON(t, engine, http.MethodPost, "/api/v2/auth/login", map[string]any{
		"email": "  " + strings.ToUpper(email) + "  ", "password": "password-123",
	}, "")
	require.Equal(t, http.StatusOK, status, "邮箱登录必须大小写不敏感")

	status, response, _ := performJSON(t, engine, http.MethodPost, "/api/v2/auth/login", map[string]any{
		"email": email, "password": "wrong-password",
	}, "")
	require.Equal(t, http.StatusUnauthorized, status)
	require.Equal(t, apierr.BizUnauthorized, response.ErrorCode)

	future := time.Now().UTC().Add(2 * time.Hour)
	require.NoError(t, gdb.Model(&model.User{}).Where("email = ?", email).Updates(map[string]any{
		"ban_is_permanent": false, "banned_until": future, "ban_reason": "限时测试封禁",
	}).Error)
	status, response, _ = performJSON(t, engine, http.MethodPost, "/api/v2/auth/login", map[string]any{
		"email": email, "password": "password-123",
	}, "")
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, apierr.BizAccountBanned, response.ErrorCode)

	require.NoError(t, gdb.Model(&model.User{}).Where("email = ?", email).Updates(map[string]any{
		"ban_is_permanent": true, "banned_until": nil, "ban_reason": "永久测试封禁",
	}).Error)
	status, response, _ = performJSON(t, engine, http.MethodPost, "/api/v2/auth/login", map[string]any{
		"email": email, "password": "password-123",
	}, "")
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, apierr.BizAccountBanned, response.ErrorCode)
	require.Contains(t, response.Message, "永久")

	past := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, gdb.Model(&model.User{}).Where("email = ?", email).Updates(map[string]any{
		"ban_is_permanent": false, "banned_until": past, "ban_reason": "已经到期",
	}).Error)
	status, response, _ = performJSON(t, engine, http.MethodPost, "/api/v2/auth/login", map[string]any{
		"email": email, "password": "password-123",
	}, "")
	require.Equal(t, http.StatusOK, status, "限时封禁到期后无需人工清字段即可登录")
	require.NoError(t, gdb.Model(&model.User{}).Where("email = ?", email).Updates(map[string]any{
		"banned_until": nil, "ban_reason": nil, "banned_by": nil,
	}).Error)

	deletedEmail := "login-deleted@fdueat.com"
	deleted := registerPostTestUser(t, engine, sender, deletedEmail, "注销登录用户")
	deletedAt := time.Now().UTC()
	require.NoError(t, gdb.Model(&model.User{}).Where("id = ?", deleted.User.ID).
		Update("deleted_at", deletedAt).Error)
	status, response, _ = performJSON(t, engine, http.MethodPost, "/api/v2/auth/login", map[string]any{
		"email": deletedEmail, "password": "password-123",
	}, "")
	require.Equal(t, http.StatusUnauthorized, status)
	require.Equal(t, apierr.BizUnauthorized, response.ErrorCode)
}

func testSessionSecurityLifecycle(
	t *testing.T,
	cfg appconfig.Config,
	engine *server.Hertz,
	sender *captureEmailSender,
	gdb *gorm.DB,
) {
	t.Helper()
	controller := loginUser(t, engine, "会话控制器")
	status, response, _ := performJSON(t, engine, http.MethodPost,
		"/api/v2/auth/refresh", map[string]any{"refresh_token": controller.Token}, "")
	require.Equal(t, http.StatusUnauthorized, status)
	require.Equal(t, apierr.BizUnauthorized, response.ErrorCode, "access token 不能当 refresh token 使用")

	fixtures := testutil.NewFixtures(t, gdb)
	other := fixtures.CreateActor(cfg)
	status, response, _ = performJSON(t, engine, http.MethodDelete,
		"/api/v2/auth/sessions/"+strconv.FormatUint(other.Session.ID, 10), nil, controller.Token)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizSessionNotFound, response.ErrorCode)
	status, _, _ = performJSON(t, engine, http.MethodGet, "/api/v2/auth/me", nil, other.Token)
	require.Equal(t, http.StatusOK, status, "跨用户踢设备失败不得影响目标会话")

	owned := loginUser(t, engine, "重复踢设备")
	ownedClaims, err := jwtx.NewCodec(integrationSecret).Parse(owned.Token, jwtx.TypeAccess)
	require.NoError(t, err)
	ownedPath := "/api/v2/auth/sessions/" + strconv.FormatInt(ownedClaims.SessionID, 10)
	status, _, _ = performJSON(t, engine, http.MethodDelete, ownedPath, nil, controller.Token)
	require.Equal(t, http.StatusOK, status)
	status, response, _ = performJSON(t, engine, http.MethodDelete, ownedPath, nil, controller.Token)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizSessionNotFound, response.ErrorCode)

	expiring := loginUser(t, engine, "过期边界设备")
	expiringClaims, err := jwtx.NewCodec(integrationSecret).Parse(expiring.Token, jwtx.TypeAccess)
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, gdb.Model(&model.UserSession{}).Where("id = ?", expiringClaims.SessionID).
		Updates(map[string]any{
			"created_at": now.Add(-2 * time.Hour), "last_seen_at": now.Add(-2 * time.Hour),
			"expires_at": now.Add(-time.Hour),
		}).Error)
	assertUnauthorized(t, engine, http.MethodGet, "/api/v2/auth/me", nil, expiring.Token)
	assertUnauthorized(t, engine, http.MethodPost, "/api/v2/auth/refresh",
		map[string]any{"refresh_token": expiring.RefreshToken}, "")
	status, response, _ = performJSON(t, engine, http.MethodGet, "/api/v2/auth/sessions", nil, controller.Token)
	require.Equal(t, http.StatusOK, status)
	var sessions struct {
		Sessions []service.SessionView `json:"sessions"`
	}
	decodeData(t, response, &sessions)
	for _, session := range sessions.Sessions {
		require.NotEqual(t, uint64(expiringClaims.SessionID), session.ID,
			"刚好处于过期边界之外的会话不得出现在活跃列表")
	}

	victimEmail := "session-ban-lifecycle@fdueat.com"
	victim := registerPostTestUser(t, engine, sender, victimEmail, "封禁会话用户")
	victimClaims, err := jwtx.NewCodec(integrationSecret).Parse(victim.Token, jwtx.TypeAccess)
	require.NoError(t, err)
	admin := fixtures.CreateActor(cfg, testutil.WithUserRole(model.UserRoleAdmin))
	status, response, _ = performJSON(t, engine, http.MethodPut,
		fmt.Sprintf("/api/v2/admin/users/%d/status", victim.User.ID),
		map[string]any{"ban_is_permanent": true, "ban_reason": "会话撤销组合测试"}, admin.Token)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
	assertUnauthorized(t, engine, http.MethodGet, "/api/v2/auth/me", nil, victim.Token)
	var victimSession model.UserSession
	require.NoError(t, gdb.First(&victimSession, uint64(victimClaims.SessionID)).Error)
	require.NotNil(t, victimSession.RevokedAt, "封禁必须在同一事务撤销已有会话")

	status, response, _ = performJSON(t, engine, http.MethodPut,
		fmt.Sprintf("/api/v2/admin/users/%d/status", victim.User.ID),
		map[string]any{"ban_is_permanent": false}, admin.Token)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
	assertUnauthorized(t, engine, http.MethodGet, "/api/v2/auth/me", nil, victim.Token)
	assertUnauthorized(t, engine, http.MethodPost, "/api/v2/auth/refresh",
		map[string]any{"refresh_token": victim.RefreshToken}, "")
	status, response, _ = performJSON(t, engine, http.MethodPost, "/api/v2/auth/login", map[string]any{
		"email": victimEmail, "password": "password-123",
	}, "")
	require.Equal(t, http.StatusOK, status, "解封后只允许新登录建立新会话")
}

func testConcurrentRegistration(
	t *testing.T,
	engine *server.Hertz,
	sender *captureEmailSender,
	gdb *gorm.DB,
) {
	t.Helper()
	email := "concurrent-register@fdueat.com"
	sendCode(t, engine, email)
	code := capturedCode(t, sender, email)
	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	results := make(chan asyncRequestResult, 2)
	for range 2 {
		go func() {
			ready <- struct{}{}
			<-start
			status, response, raw, err := performJSONRequest(
				engine, http.MethodPost, "/api/v2/auth/register", map[string]any{
					"email": email, "password": "password-123", "verification_code": code,
					"name": "并发注册用户",
				}, "",
			)
			results <- asyncRequestResult{status: status, response: response, raw: raw, err: err}
		}()
	}
	<-ready
	<-ready
	close(start)

	successes, rejected := 0, 0
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for range 2 {
		select {
		case result := <-results:
			require.NoError(t, result.err)
			switch result.status {
			case http.StatusOK:
				successes++
			case http.StatusBadRequest:
				require.Equal(t, apierr.BizVerifyCodeInvalid, result.response.ErrorCode)
				rejected++
			case http.StatusConflict:
				require.Equal(t, apierr.BizEmailTaken, result.response.ErrorCode)
				rejected++
			default:
				require.Failf(t, "unexpected registration result", "status=%d response=%+v",
					result.status, result.response)
			}
		case <-waitCtx.Done():
			require.FailNow(t, "并发注册未在期限内返回")
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, rejected)
	var user model.User
	require.NoError(t, gdb.Where("email = ?", email).First(&user).Error)
	var userCount, sessionCount int64
	require.NoError(t, gdb.Model(&model.User{}).Where("email = ?", email).Count(&userCount).Error)
	require.NoError(t, gdb.Model(&model.UserSession{}).Where("user_id = ?", user.ID).Count(&sessionCount).Error)
	require.EqualValues(t, 1, userCount)
	require.EqualValues(t, 1, sessionCount)
	var challenge model.EmailVerificationCode
	require.NoError(t, gdb.Where("email = ?", email).First(&challenge).Error)
	require.NotNil(t, challenge.ConsumedAt)
}

func requireAuthFieldError(
	t *testing.T,
	response testAPIResponse,
	field string,
	code apierr.FieldCode,
) {
	t.Helper()
	require.Equal(t, apierr.BizValidation, response.ErrorCode)
	var data struct {
		Errors []apierr.FieldError `json:"errors"`
	}
	decodeData(t, response, &data)
	require.NotEmpty(t, data.Errors)
	require.Equal(t, field, data.Errors[0].Field)
	require.Equal(t, code, data.Errors[0].Code)
}

func authTestConfig() appconfig.Config {
	return appconfig.Config{
		Profile: appconfig.ProfileDev, JWTSecretKey: integrationSecret,
		JWTExpireMinutes: 60, JWTRefreshExpireDay: 30,
		EmailVerificationRequired: true, EmailVerificationSecret: integrationSecret,
		AllowedEmailDomains: "fdueat.com,m.fudan.edu.cn,fudan.edu.cn",
	}
}

func authTestEngine(
	cfg appconfig.Config,
	database *dbinfra.DB,
	sender service.VerificationEmailSender,
) *server.Hertz {
	engine := server.New(
		server.WithHandleMethodNotAllowed(true),
		hertzconfig.Option{F: func(_ *hertzconfig.Options) {}},
	)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router.Register(engine, router.Deps{Config: cfg, DB: database, Log: log, EmailSender: sender})
	return engine
}

func openAuthPostgres(t *testing.T) (*gorm.DB, *dbinfra.DB) {
	t.Helper()
	database := testutil.OpenPostgres(t, testutil.WithDatabaseName("danshi_auth_test"))
	return database.GORM, database.DB
}

func sendCode(t *testing.T, engine *server.Hertz, email string) {
	t.Helper()
	status, _, _ := performJSON(t, engine, http.MethodPost,
		"/api/v2/auth/email-verification-codes", map[string]any{"email": email}, "")
	require.Equal(t, http.StatusOK, status)
}

func registerUser(
	t *testing.T,
	engine *server.Hertz,
	sender *captureEmailSender,
	email string,
	device string,
) service.AuthResult {
	t.Helper()
	status, response, _ := performJSON(t, engine, http.MethodPost, "/api/v2/auth/register", map[string]any{
		"email": email, "password": "password-123", "verification_code": capturedCode(t, sender, email),
		"name": "测试用户", "gender": "female", "device_label": device,
	}, "")
	require.Equal(t, http.StatusOK, status, "response=%s", response.Message)
	var result service.AuthResult
	decodeData(t, response, &result)
	return result
}

func capturedCode(t *testing.T, sender *captureEmailSender, email string) string {
	t.Helper()
	code, ok := sender.LastCode(email)
	require.True(t, ok, "邮箱 %s 没有成功投递的验证码", email)
	return code
}

func loginUser(t *testing.T, engine *server.Hertz, device string) service.AuthResult {
	t.Helper()
	status, response, _ := performJSON(t, engine, http.MethodPost, "/api/v2/auth/login", map[string]any{
		"email": authFlowEmail, "password": "password-123", "device_label": device,
	}, "")
	require.Equal(t, http.StatusOK, status, "response=%s", response.Message)
	var result service.AuthResult
	decodeData(t, response, &result)
	return result
}

func assertUnauthorized(
	t *testing.T,
	engine *server.Hertz,
	method string,
	path string,
	body any,
	token string,
) {
	t.Helper()
	status, response, _ := performJSON(t, engine, method, path, body, token)
	require.Equal(t, http.StatusUnauthorized, status)
	require.Equal(t, apierr.BizUnauthorized, response.ErrorCode)
	require.Equal(t, "未登录或登录已失效", response.Message)
}

func performJSON(
	t *testing.T,
	engine *server.Hertz,
	method string,
	path string,
	payload any,
	token string,
) (int, testAPIResponse, *ut.ResponseRecorder) {
	t.Helper()
	status, response, recorder, err := performJSONRequest(engine, method, path, payload, token)
	require.NoError(t, err)
	return status, response, recorder
}

func performJSONRequest(
	engine *server.Hertz,
	method string,
	path string,
	payload any,
	token string,
) (int, testAPIResponse, *ut.ResponseRecorder, error) {
	var body *ut.Body
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, testAPIResponse{}, nil, err
		}
		body = &ut.Body{Body: bytes.NewReader(encoded), Len: len(encoded)}
	}
	headers := []ut.Header{{Key: "Content-Type", Value: "application/json"}, {Key: "User-Agent", Value: "Danshi-Test/1.0"}}
	if token != "" {
		headers = append(headers, ut.Header{Key: "Authorization", Value: "Bearer " + token})
	}
	recorder := ut.PerformRequest(engine.Engine, method, path, body, headers...)
	result := recorder.Result()
	var decoded testAPIResponse
	if err := json.Unmarshal(result.Body(), &decoded); err != nil {
		return result.StatusCode(), testAPIResponse{}, recorder, err
	}
	return result.StatusCode(), decoded, recorder, nil
}

func decodeData(t *testing.T, response testAPIResponse, target any) {
	t.Helper()
	require.NoError(t, json.Unmarshal(response.Data, target), "data=%s", response.Data)
}

func sessionLabelPresent(sessions []service.SessionView, label string) bool {
	for _, session := range sessions {
		if session.DeviceLabel != nil && strings.EqualFold(*session.DeviceLabel, label) {
			return true
		}
	}
	return false
}
