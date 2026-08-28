package obs

import (
	"context"
	"strings"
	"testing"

	"github.com/prometheus/common/expfmt"
	"github.com/stretchr/testify/require"
)

func TestImageModerationRetryCollectorUsesBoundedStates(t *testing.T) {
	metrics, err := NewMetrics(nil, WithImageModerationRetryStateCounter(
		func(context.Context) (map[string]int64, error) {
			return map[string]int64{
				"pending": 2, "dead_letter": 1,
				"https://secret.example/object?token=secret": 999,
			}, nil
		},
	))
	require.NoError(t, err)
	metrics.imageModerationRetry.Refresh(context.Background())
	families, err := metrics.registry.Gather()
	require.NoError(t, err)
	var output strings.Builder
	encoder := expfmt.NewEncoder(&output, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, family := range families {
		require.NoError(t, encoder.Encode(family))
	}
	body := output.String()
	require.Contains(t, body, `danshi_image_moderation_retry_cached_items{state="pending"} 2`)
	require.Contains(t, body, `danshi_image_moderation_retry_cached_items{state="dead_letter"} 1`)
	require.NotContains(t, body, "secret.example")
	require.NotContains(t, body, "token=secret")
}
