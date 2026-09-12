package service_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbinfra "github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestVerificationEmailDeliveryRechecksBeforeSending(t *testing.T) {
	for _, change := range []string{"consumed", "replaced", "expired", "exhausted", "deleted", "lease_lost", "lease_expired"} {
		t.Run(change, func(t *testing.T) {
			database := testutil.OpenPostgres(t)
			now := time.Now().UTC()
			sender := testutil.NewMockEmailSender()
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			sender.Program(testutil.EmailRule{Call: 1, Behavior: testutil.EmailBlocked(release)})
			worker := service.NewVerificationEmailDeliveryWorker(database.DB, sender, service.VerificationEmailDeliveryWorkerOptions{})
			first := seedDeliveryChallenge(t, database, "audit_first", strings.Repeat("a", 64), now)
			second := seedDeliveryChallenge(t, database, "audit_second", strings.Repeat("b", 64), now)
			require.NoError(t, database.DB.RunInTx(context.Background(), func(ctx context.Context) error {
				for _, c := range []*model.EmailVerificationCode{first, second} {
					if _, err := worker.Enqueue(ctx, c, c.Email, c.Purpose, c.CodeDigest, "123456", now); err != nil {
						return err
					}
				}
				return nil
			}))
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := worker.RunBatch(ctx); done <- err }()
			require.True(t, sender.WaitForAttempts(ctx, 1))
			require.Empty(t, sender.Attempts(second.Email))
			err := database.DB.RunInTx(ctx, func(txCtx context.Context) error {
				switch change {
				case "consumed":
					second.ConsumedAt = &now
				case "replaced":
					second.CodeDigest = strings.Repeat("c", 64)
				case "expired":
					second.ExpiresAt = now.Add(-time.Second)
				case "exhausted":
					second.FailedAttempts = 5
				case "deleted":
					return dbinfra.FromContext(txCtx).Model(&model.User{}).Where("email = ?", second.Email).UpdateColumn("deleted_at", now).Error
				case "lease_lost":
					return dbinfra.FromContext(txCtx).Model(&model.VerificationEmailDelivery{}).Where("challenge_id = ?", second.ID).UpdateColumn("lease_token", strings.Repeat("x", 48)).Error
				case "lease_expired":
					return dbinfra.FromContext(txCtx).Model(&model.VerificationEmailDelivery{}).Where("challenge_id = ?", second.ID).UpdateColumn("lease_until", now.Add(-time.Second)).Error
				}
				return (repository.VerificationCodeRepository{}).SaveState(txCtx, second, now)
			})
			unblock()
			require.NoError(t, err)
			require.NoError(t, <-done)
			require.Len(t, sender.Deliveries(first.Email), 1)
			require.Empty(t, sender.Attempts(second.Email), "invalidated before provider call: must not send")
			var delivery model.VerificationEmailDelivery
			require.NoError(t, database.GORM.Where("challenge_id = ?", second.ID).First(&delivery).Error)
			if strings.HasPrefix(change, "lease_") {
				require.Equal(t, model.VerificationEmailDeliverySending, delivery.State, "old worker must not overwrite task state")
			} else {
				require.Equal(t, model.VerificationEmailDeliveryCanceled, delivery.State)
				require.Nil(t, delivery.Code)
			}
		})
	}
}
