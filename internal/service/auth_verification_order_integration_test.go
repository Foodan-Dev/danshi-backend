package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/service"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestRegistrationRejectsUnverifiedEmailBeforeModeration(t *testing.T) {
	database := testutil.OpenPostgres(t)
	moderator := testutil.NewMockModeration()
	auth := service.NewAuthService(testutil.DefaultConfig(), testutil.NewMockEmailSender(), moderator)
	username, code := "audit_unverified", "123456"
	err := database.DB.RunInTx(context.Background(), func(ctx context.Context) error {
		_, err := auth.Register(ctx, service.RegisterInput{Email: "unverified@fdueat.com", Password: "password-123", Username: &username, VerificationCode: &code}, service.ClientInfo{})
		return err
	})
	require.Error(t, err)
	require.Empty(t, moderator.ContentCalls(), "nonexistent verification challenge must be rejected before external moderation")
}
