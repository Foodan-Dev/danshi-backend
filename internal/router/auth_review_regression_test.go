package router_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/passwordx"
	"github.com/Foodan-Dev/danshi-backend/internal/router"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestAuthReviewRegressions(t *testing.T) {
	gdb, database := openAuthPostgres(t)
	cfg := authTestConfig()
	sender := newCaptureEmailSender()
	engine := authTestEngine(cfg, database, sender)
	hash, err := passwordx.Hash("old-password-123")
	require.NoError(t, err)
	user := model.User{Email: "reset-race@fdueat.com", Username: "reset_race", PasswordHash: hash}
	require.NoError(t, gdb.Create(&user).Error)

	t.Run("missing reset template does not disable registration", func(t *testing.T) {
		registrationOnly := registrationOnlySender{newCaptureEmailSender()}
		localEngine := authTestEngine(cfg, database, registrationOnly)
		for _, email := range []string{user.Email, "unknown-template@fdueat.com"} {
			status, response, _ := performJSON(t, localEngine, http.MethodPost,
				"/api/v2/auth/password-reset-codes", map[string]any{"email": email}, "")
			require.Equal(t, http.StatusServiceUnavailable, status, response.Message)
		}
		email := "registration-only@fdueat.com"
		sendCode(t, localEngine, email)
		require.Len(t, capturedCode(t, registrationOnly.MockEmailSender, email), 6)
	})

	t.Run("Unicode username policy survives database writes", func(t *testing.T) {
		localCfg := cfg
		localCfg.EmailVerificationRequired = false
		localEngine := authTestEngine(localCfg, database, sender)
		for index, username := range []string{"用户௰", "用户〇", "用户١", "用户꧰", "用户𞓰"} {
			status, response, _ := performJSON(t, localEngine, http.MethodPost, "/api/v2/auth/register", map[string]any{
				"email": fmt.Sprintf("unicode-%d@fdueat.com", index), "username": username, "password": "password-123",
			}, "")
			require.Equal(t, http.StatusOK, status, response.Message)
			var auth service.AuthResult
			decodeData(t, response, &auth)
			require.Equal(t, username, auth.User.Username)
			// A second normalized identity must hit the stable conflict, never a SQL 500.
			status, response, _ = performJSON(t, localEngine, http.MethodPost, "/api/v2/auth/register", map[string]any{
				"email": fmt.Sprintf("unicode-duplicate-%d@fdueat.com", index), "username": username, "password": "password-123",
			}, "")
			require.Equal(t, http.StatusConflict, status, response.Message)
			require.Equal(t, apierr.BizUsernameTaken, response.ErrorCode)
			updatedUsername := "更新" + username
			status, response, _ = performJSON(t, localEngine, http.MethodPut,
				userPath(auth.User.ID), map[string]any{"username": updatedUsername}, auth.Token)
			require.Equal(t, http.StatusOK, status, response.Message)
			var update service.UserUpdateResult
			decodeData(t, response, &update)
			require.Equal(t, updatedUsername, update.User.Username)

		}
	})

	t.Run("reset invalidates login that read the old password", func(t *testing.T) {
		status, response, _ := performJSON(t, engine, http.MethodPost, "/api/v2/auth/password-reset-codes", map[string]any{"email": user.Email}, "")
		require.Equal(t, http.StatusOK, status, response.Message)
		code := capturedCode(t, sender, user.Email)
		type loginBarrierKey struct{}
		read, release := make(chan struct{}), make(chan struct{})
		var paused atomic.Bool
		require.NoError(t, gdb.Callback().Query().After("gorm:query").Register("regression:pause_login", func(tx *gorm.DB) {
			if tx.Statement.Context.Value(loginBarrierKey{}) == true && tx.Statement.Table == "users" && paused.CompareAndSwap(false, true) {
				close(read)
				<-release
			}
		}))
		defer func() { require.NoError(t, gdb.Callback().Query().Remove("regression:pause_login")) }()
		released := false
		defer func() {
			if !released {
				close(release)
			}
		}()
		auth := service.NewAuthService(cfg, sender)
		done := make(chan error, 1)
		go func() {
			done <- database.RunInTx(context.WithValue(context.Background(), loginBarrierKey{}, true), func(ctx context.Context) error {
				_, err := auth.Login(ctx, service.LoginInput{Identifier: user.Email, Password: "old-password-123"}, service.ClientInfo{})
				return err
			})
		}()
		select {
		case <-read:
		case <-time.After(5 * time.Second):
			t.Fatal("登录未到达旧密码读取屏障")
		}
		status, response, _ = performJSON(t, engine, http.MethodPost, "/api/v2/auth/password-resets", map[string]any{
			"email": user.Email, "verification_code": code, "new_password": "new-password-123",
		}, "")
		close(release)
		released = true
		require.Equal(t, http.StatusOK, status, response.Message)
		select {
		case err := <-done:
			var failure *apierr.Error
			require.ErrorAs(t, err, &failure)
			require.Equal(t, http.StatusUnauthorized, failure.Status)
			require.Equal(t, apierr.BizUnauthorized, failure.Code)
		case <-time.After(5 * time.Second):
			t.Fatal("登录未在重置后结束")
		}
		var sessions int64
		require.NoError(t, gdb.Model(&model.UserSession{}).Where("user_id = ? AND revoked_at IS NULL", user.ID).Count(&sessions).Error)
		require.Zero(t, sessions, "旧密码不得在重置完成后创建有效会话")
		status, response, _ = performJSON(t, engine, http.MethodPost, "/api/v2/auth/login", map[string]any{"identifier": user.Email, "password": "new-password-123"}, "")
		require.Equal(t, http.StatusOK, status, response.Message)
	})

	t.Run("provider stalls never block reset responses", func(t *testing.T) {
		require.NoError(t, gdb.Model(&model.EmailVerificationCode{}).Where("email = ?", user.Email).Update("last_sent_at", time.Now().Add(-2*time.Minute)).Error)
		blocked := testutil.NewMockEmailSender()
		release := make(chan struct{})
		blocked.SetDefault(testutil.EmailBlocked(release))
		worker := service.NewVerificationEmailDeliveryWorker(database, blocked, service.VerificationEmailDeliveryWorkerOptions{})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); worker.Run(ctx) }()
		defer func() {
			close(release)
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("worker 未能停止")
			}
		}()
		realEngine := server.New(server.WithHandleMethodNotAllowed(true))
		router.Register(realEngine, router.Deps{Config: cfg, DB: database, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), EmailSender: blocked, EmailDeliveryWorker: worker})
		for _, email := range []string{user.Email, "unknown-reset@fdueat.com"} {
			response := make(chan asyncRequestResult, 1)
			go func() {
				status, body, raw, err := performJSONRequest(realEngine, http.MethodPost, "/api/v2/auth/password-reset-codes", map[string]any{"email": email}, "")
				response <- asyncRequestResult{status: status, response: body, raw: raw, err: err}
			}()
			select {
			case result := <-response:
				require.NoError(t, result.err)
				require.Equal(t, http.StatusOK, result.status, result.response.Message)
			case <-time.After(2 * time.Second):
				t.Fatal("邮件供应商阻塞了 HTTP 响应")
			}
		}
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer waitCancel()
		require.True(t, blocked.WaitForAttempts(waitCtx, 1), "后台 worker 应当执行已提交的任务")
	})
}

type registrationOnlySender struct{ *testutil.MockEmailSender }

func (registrationOnlySender) PasswordResetConfigured() bool { return false }
