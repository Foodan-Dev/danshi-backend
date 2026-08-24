package obs

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/common/expfmt"
	"github.com/stretchr/testify/require"
)

func TestImageAccessCollectorUsesBoundedStatesAndSingleRefresher(t *testing.T) {
	var calls atomic.Int64
	release := make(chan struct{})
	collector := newImageAccessCollector(func(context.Context) (map[string]int64, error) {
		calls.Add(1)
		<-release
		return map[string]int64{
			"pending_acl": 2, "dead_letter": 1,
			"https://secret.example/object?token=secret": 999,
		}, nil
	})
	var workers sync.WaitGroup
	for index := 0; index < 16; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			collector.Refresh(context.Background())
		}()
	}
	require.Eventually(t, func() bool { return calls.Load() == 1 }, time.Second, time.Millisecond)
	close(release)
	workers.Wait()
	require.EqualValues(t, 1, calls.Load())

	metrics, err := NewMetrics(nil, WithImageAccessStateCounter(func(context.Context) (map[string]int64, error) {
		return map[string]int64{
			"pending_acl": 2, "dead_letter": 1,
			"https://secret.example/object?token=secret": 999,
		}, nil
	}))
	require.NoError(t, err)
	metrics.imageAccess.Refresh(context.Background())
	families, err := metrics.registry.Gather()
	require.NoError(t, err)
	var output strings.Builder
	encoder := expfmt.NewEncoder(&output, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, family := range families {
		require.NoError(t, encoder.Encode(family))
	}
	body := output.String()
	require.Contains(t, body, `danshi_image_access_delivery_cached_items{state="pending_acl"} 2`)
	require.Contains(t, body, `danshi_image_access_delivery_cached_items{state="dead_letter"} 1`)
	require.NotContains(t, body, "secret.example")
	require.NotContains(t, body, "token=secret")
}
