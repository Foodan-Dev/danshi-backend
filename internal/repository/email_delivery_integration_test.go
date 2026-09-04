package repository_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestVerificationEmailDeliveryRepositoryClaimsAndFences(t *testing.T) {
	database := testutil.OpenPostgres(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	challenge := &model.EmailVerificationCode{
		Email: "delivery-repository@fdueat.com", Purpose: model.VerificationPurposePasswordReset,
		CodeDigest: strings.Repeat("a", 64), ExpiresAt: now.Add(10 * time.Minute),
		SendWindowStartedAt: now,
	}
	require.NoError(t, database.GORM.Create(&model.User{
		Email: challenge.Email, PasswordHash: "x", Name: "delivery repository",
	}).Error)
	require.NoError(t, database.GORM.Create(challenge).Error)

	store := repository.VerificationEmailDeliveryRepository{}
	require.NoError(t, database.DB.RunInTx(context.Background(), func(ctx context.Context) error {
		_, err := store.Enqueue(ctx, challenge, challenge.Email, challenge.Purpose,
			challenge.CodeDigest, []byte("encrypted-code"), now)
		return err
	}))

	var delivery model.VerificationEmailDelivery
	require.NoError(t, database.GORM.Where("challenge_id = ?", challenge.ID).First(&delivery).Error)
	require.Equal(t, model.VerificationEmailDeliveryPending, delivery.State)
	require.Equal(t, 0, int(delivery.Attempts))
	require.Equal(t, []byte("encrypted-code"), delivery.CodeCiphertext)

	claims := make([]repository.VerificationEmailDeliveryClaim, 0, 1)
	leaseToken := "repository-test-lease-token"
	require.NoError(t, database.DB.RunInTx(context.Background(), func(ctx context.Context) error {
		var err error
		claims, err = store.ClaimDue(ctx, now, now.Add(time.Minute), leaseToken, 10)
		return err
	}))
	require.Len(t, claims, 1)
	require.Equal(t, model.VerificationEmailDeliverySending, claims[0].State)
	require.Equal(t, challenge.CodeDigest, claims[0].CurrentCodeDigest)
	require.NotNil(t, claims[0].LeaseToken)
	require.Equal(t, leaseToken, *claims[0].LeaseToken)

	var updated bool
	require.NoError(t, database.DB.RunInTx(context.Background(), func(ctx context.Context) error {
		var err error
		updated, err = store.UpdateClaim(ctx, claims[0], map[string]any{
			"state": model.VerificationEmailDeliverySent, "next_attempt_at": nil,
			"lease_token": nil, "lease_until": nil, "sent_at": now,
			"updated_at": now,
		})
		return err
	}))
	require.True(t, updated)

	require.NoError(t, database.DB.RunInTx(context.Background(), func(ctx context.Context) error {
		var err error
		updated, err = store.UpdateClaim(ctx, claims[0], map[string]any{
			"state": model.VerificationEmailDeliverySent, "next_attempt_at": nil,
			"lease_token": nil, "lease_until": nil, "sent_at": now,
			"updated_at": now,
		})
		return err
	}))
	require.False(t, updated, "已失去 lease 的 worker 不得覆盖终态")
}

func TestVerificationEmailDeliveryRepositoryCancelsSupersededChallenge(t *testing.T) {
	database := testutil.OpenPostgres(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	challenge := &model.EmailVerificationCode{
		Email: "delivery-superseded@fdueat.com", Purpose: model.VerificationPurposePasswordReset,
		CodeDigest: strings.Repeat("b", 64), ExpiresAt: now.Add(10 * time.Minute),
		SendWindowStartedAt: now,
	}
	require.NoError(t, database.GORM.Create(&model.User{
		Email: challenge.Email, PasswordHash: "x", Name: "delivery superseded",
	}).Error)
	require.NoError(t, database.GORM.Create(challenge).Error)
	store := repository.VerificationEmailDeliveryRepository{}
	require.NoError(t, database.DB.RunInTx(context.Background(), func(ctx context.Context) error {
		if _, err := store.Enqueue(ctx, challenge, challenge.Email, challenge.Purpose,
			strings.Repeat("a", 64), []byte("old-code"), now); err != nil {
			return err
		}
		_, err := store.Enqueue(ctx, challenge, challenge.Email, challenge.Purpose,
			challenge.CodeDigest, []byte("new-code"), now)
		return err
	}))

	var claims []repository.VerificationEmailDeliveryClaim
	require.NoError(t, database.DB.RunInTx(context.Background(), func(ctx context.Context) error {
		var err error
		claims, err = store.ClaimDue(ctx, now, now.Add(time.Minute), "superseded-test-lease", 10)
		return err
	}))
	require.Len(t, claims, 2)
	for _, claim := range claims {
		if claim.CodeDigest == strings.Repeat("a", 64) {
			require.Empty(t, claim.CurrentCodeDigest)
		} else {
			require.Equal(t, challenge.CodeDigest, claim.CurrentCodeDigest)
		}
	}
}
