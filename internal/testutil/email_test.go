package testutil_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jingyijun/danshi_backend_go/internal/testutil"
)

func TestMockEmailSenderCapturesFailuresSlowDeliveryAndAbsence(t *testing.T) {
	sender := testutil.NewMockEmailSender()
	release := make(chan struct{})
	sender.Program(
		testutil.EmailRule{Call: 2, Behavior: testutil.EmailFailure(errors.New("SES 5xx"))},
		testutil.EmailRule{Call: 3, Behavior: testutil.EmailBlocked(release)},
	)

	require.NoError(t, sender.SendRegistrationCode(
		context.Background(), " User@FDUEAT.com ", "111111",
	))
	err := sender.SendRegistrationCode(context.Background(), "user@fdueat.com", "222222")
	require.EqualError(t, err, "SES 5xx")

	done := make(chan error, 1)
	go func() {
		done <- sender.SendRegistrationCode(context.Background(), "user@fdueat.com", "333333")
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.True(t, sender.WaitForAttempts(ctx, 3))
	require.Equal(t, []string{"111111"}, sender.Codes("user@fdueat.com"))
	select {
	case early := <-done:
		t.Fatalf("慢投递在释放前返回: %v", early)
	default:
	}
	close(release)
	require.NoError(t, <-done)
	require.Equal(t, []string{"111111", "333333"}, sender.Codes("user@fdueat.com"))
	last, ok := sender.LastCode("USER@FDUEAT.COM")
	require.True(t, ok)
	require.Equal(t, "333333", last)
	require.Len(t, sender.Attempts("user@fdueat.com"), 3)
	sender.RequireDeliveryCount(t, "user@fdueat.com", 2)
	sender.RequireNoDelivery(t, "registered@fdueat.com")
}

func TestMockEmailSenderTimeoutUsesCallerContext(t *testing.T) {
	sender := testutil.NewMockEmailSender()
	sender.SetDefault(testutil.EmailTimeout())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := sender.SendRegistrationCode(ctx, "timeout@fdueat.com", "123456")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Len(t, sender.Attempts("timeout@fdueat.com"), 1)
	sender.RequireNoDelivery(t, "timeout@fdueat.com")
}
