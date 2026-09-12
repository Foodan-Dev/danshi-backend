package service_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/passwordx"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestPasswordResetAndAuthenticatedProfileUpdateDoNotDeadlock(t *testing.T) {
	database := testutil.OpenPostgres(t)
	cfg := testutil.DefaultConfig()
	auth := service.NewAuthService(cfg, testutil.NewMockEmailSender())
	hash, err := passwordx.Hash("old-password-123")
	require.NoError(t, err)
	user := model.User{Email: "audit_deadlock@fdueat.com", Username: "audit_deadlock", PasswordHash: hash}
	require.NoError(t, database.GORM.Create(&user).Error)
	var login *service.AuthResult
	require.NoError(t, database.DB.RunInTx(context.Background(), func(ctx context.Context) error {
		var err error
		login, err = auth.Login(ctx, service.LoginInput{Identifier: user.Email, Password: "old-password-123"}, service.ClientInfo{})
		return err
	}))
	require.NoError(t, database.GORM.Model(&model.UserSession{}).Where("user_id = ?", user.ID).UpdateColumns(map[string]any{"last_seen_at": time.Now().Add(-time.Hour), "created_at": time.Now().Add(-2 * time.Hour)}).Error)
	now := time.Now().UTC()
	mac := hmac.New(sha256.New, []byte(cfg.EmailVerificationSecret))
	_, _ = mac.Write([]byte("password_reset:" + user.Email + ":123456"))
	challenge := model.EmailVerificationCode{Email: user.Email, Purpose: model.VerificationPurposePasswordReset, CodeDigest: fmt.Sprintf("%x", mac.Sum(nil)), ExpiresAt: now.Add(10 * time.Minute), SendWindowStartedAt: now}
	require.NoError(t, database.GORM.Create(&challenge).Error)
	authenticated, userLocked := make(chan struct{}), make(chan struct{})
	type resetKey struct{}
	require.NoError(t, database.GORM.Callback().Update().After("gorm:update").Register("audit:reset_password", func(tx *gorm.DB) {
		if tx.Statement.Context.Value(resetKey{}) == true && tx.Statement.Table == "users" {
			close(userLocked)
		}
	}))
	defer func() { require.NoError(t, database.GORM.Callback().Update().Remove("audit:reset_password")) }()
	done := make(chan error, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() {
		done <- database.DB.RunInTx(ctx, func(ctx context.Context) error {
			_, err := auth.Authenticate(ctx, login.Token)
			if err != nil {
				return err
			}
			close(authenticated)
			select {
			case <-userLocked:
			case <-ctx.Done():
				return ctx.Err()
			}
			gender := "other"
			_, err = service.NewUserService(nil, nil).Update(ctx, user.ID, user.ID, service.UpdateUserInput{Gender: &gender, GenderSet: true})
			return err
		})
	}()
	select {
	case <-authenticated:
	case err := <-done:
		t.Fatalf("authentication failed: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() {
		done <- database.DB.RunInTx(context.WithValue(ctx, resetKey{}, true), func(ctx context.Context) error {
			return auth.ResetPassword(ctx, service.PasswordResetInput{Email: user.Email, VerificationCode: "123456", NewPassword: "new-password-123"})
		})
	}()
	errs := []error{<-done, <-done}
	for _, err := range errs {
		require.NoError(t, err, "reset and authenticated profile update must not deadlock")
	}
	var sessions []model.UserSession
	require.NoError(t, database.GORM.Where("user_id = ?", user.ID).Find(&sessions).Error)
	require.Len(t, sessions, 1)
	require.NotNil(t, sessions[0].RevokedAt, "deferred Touch must not revive the revoked session")
	require.NoError(t, database.GORM.First(&user, user.ID).Error)
	require.True(t, passwordx.Verify("new-password-123", user.PasswordHash))
	require.Equal(t, model.GenderOther, *user.Gender)
}
