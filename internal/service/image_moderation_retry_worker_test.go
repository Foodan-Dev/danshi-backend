package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestImageModerationRetryBackoffIsExponentialAndBounded(t *testing.T) {
	base := 30 * time.Second
	maximum := 30 * time.Minute
	require.Equal(t, 30*time.Second, imageModerationRetryBackoff(1, base, maximum))
	require.Equal(t, time.Minute, imageModerationRetryBackoff(2, base, maximum))
	require.Equal(t, 16*time.Minute, imageModerationRetryBackoff(6, base, maximum))
	require.Equal(t, 30*time.Minute, imageModerationRetryBackoff(7, base, maximum))
	require.Equal(t, 30*time.Minute, imageModerationRetryBackoff(100, base, maximum))
}
