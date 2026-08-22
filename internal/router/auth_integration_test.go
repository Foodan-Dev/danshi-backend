package router_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

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
)

const (
	integrationSecret = "integration-secret-longer-than-thirty-two-bytes"
	authFlowEmail     = "auth-flow@fdueat.com"
)

type captureEmailSender struct {
	mu    sync.Mutex
	codes map[string]string
	count map[string]int
}

func newCaptureEmailSender() *captureEmailSender {
	return &captureEmailSender{codes: make(map[string]string), count: make(map[string]int)}
}

func (s *captureEmailSender) SendRegistrationCode(
	_ context.Context,
	email string,
	code string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codes[email] = code
	s.count[email]++
	return nil
}

func (s *captureEmailSender) code(email string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.codes[email]
}

func (s *captureEmailSender) sends(email string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count[email]
}

type failingEmailSender struct{}

func (failingEmailSender) SendRegistrationCode(context.Context, string, string) error {
	return errors.New("test delivery failure")
}

type timeoutEmailSender struct{}

func (timeoutEmailSender) SendRegistrationCode(context.Context, string, string) error {
	return context.DeadlineExceeded
}

type blockingEmailSender struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (s blockingEmailSender) SendRegistrationCode(
	ctx context.Context,
	_ string,
	_ string,
) error {
	select {
	case s.started <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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
		failingEngine := authTestEngine(cfg, database, failingEmailSender{})
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
		timeoutEngine := authTestEngine(cfg, database, timeoutEmailSender{})
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
		require.NotEqual(t, sender.code(email), challenge.CodeDigest, "验证码明文不得落库")
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

	started := make(chan struct{}, maxInFlight)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)

	engine := authTestEngine(cfg, database, blockingEmailSender{started: started, release: release})
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

	allStarted := time.NewTimer(5 * time.Second)
	defer allStarted.Stop()
	for range maxInFlight {
		select {
		case <-started:
		case <-allStarted.C:
			require.FailNow(t, "5 个发信请求未能全部进入阻塞 sender")
		}
	}
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
	require.Equal(t, 1, sender.sends(email))
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
	require.Zero(t, sender.sends(email), "已注册邮箱绝不能收到验证码投递")

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
	for range 2 {
		status, response, _ := performJSON(t, engine, http.MethodPost, "/api/v2/auth/register", map[string]any{
			"email": email, "password": "password-123", "verification_code": "999999",
		}, "")
		require.Equal(t, http.StatusBadRequest, status)
		require.Equal(t, apierr.BizVerifyCodeInvalid, response.ErrorCode)
	}
	var challenge model.EmailVerificationCode
	require.NoError(t, gdb.Where("email = ?", email).First(&challenge).Error)
	require.EqualValues(t, 2, challenge.FailedAttempts, "4xx 不能回滚验证码失败安全计数")

	status, _, _ := performJSON(t, engine, http.MethodPost, "/api/v2/auth/register", map[string]any{
		"email": email, "password": "password-123", "verification_code": sender.code(email),
	}, "")
	require.Equal(t, http.StatusOK, status)
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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	container, err := tcpostgres.Run(
		ctx,
		"postgres:18",
		tcpostgres.WithDatabase("danshi_auth_test"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	testcontainers.CleanupContainer(t, container)

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	sqlDB, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, sqlDB.PingContext(ctx))
	require.NoError(t, dbinfra.Up(ctx, sqlDB))

	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		DisableAutomaticPing:                     true,
		DisableForeignKeyConstraintWhenMigrating: true,
		SkipDefaultTransaction:                   true,
		Logger:                                   logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	return gdb, &dbinfra.DB{DB: gdb}
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
		"email": email, "password": "password-123", "verification_code": sender.code(email),
		"name": "测试用户", "gender": "female", "device_label": device,
	}, "")
	require.Equal(t, http.StatusOK, status, "response=%s", response.Message)
	var result service.AuthResult
	decodeData(t, response, &result)
	return result
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
