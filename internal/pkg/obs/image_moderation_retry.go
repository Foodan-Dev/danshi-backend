package obs

import (
	"context"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var imageModerationRetryMetricStates = [...]string{"pending", "dead_letter"}

// ImageModerationRetryStateCounter 返回固定状态的持久补审数量。
type ImageModerationRetryStateCounter func(context.Context) (map[string]int64, error)

type imageModerationRetryCollector struct {
	counter ImageModerationRetryStateCounter

	mu            sync.RWMutex
	refreshing    bool
	ready         bool
	counts        map[string]int64
	lastAttempt   time.Time
	lastSuccess   time.Time
	refreshErrors uint64

	itemsDesc         *prometheus.Desc
	readyDesc         *prometheus.Desc
	lastSuccessDesc   *prometheus.Desc
	refreshingDesc    *prometheus.Desc
	refreshErrorsDesc *prometheus.Desc
}

func newImageModerationRetryCollector(
	counter ImageModerationRetryStateCounter,
) *imageModerationRetryCollector {
	return &imageModerationRetryCollector{
		counter: counter,
		itemsDesc: prometheus.NewDesc(
			"danshi_image_moderation_retry_cached_items",
			"Last successfully observed image moderation retries by bounded state.",
			[]string{"state"}, nil,
		),
		readyDesc: prometheus.NewDesc(
			"danshi_image_moderation_retry_cache_ready",
			"Whether a successful image moderation retry observation is available.", nil, nil,
		),
		lastSuccessDesc: prometheus.NewDesc(
			"danshi_image_moderation_retry_last_success_timestamp_seconds",
			"Unix timestamp of the last successful image moderation retry observation.", nil, nil,
		),
		refreshingDesc: prometheus.NewDesc(
			"danshi_image_moderation_retry_refresh_in_progress",
			"Whether one bounded image moderation retry refresh is running.", nil, nil,
		),
		refreshErrorsDesc: prometheus.NewDesc(
			"danshi_image_moderation_retry_refresh_errors_total",
			"Total failed image moderation retry refresh attempts.", nil, nil,
		),
	}
}

func (c *imageModerationRetryCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.itemsDesc
	ch <- c.readyDesc
	ch <- c.lastSuccessDesc
	ch <- c.refreshingDesc
	ch <- c.refreshErrorsDesc
}

func (c *imageModerationRetryCollector) Refresh(ctx context.Context) {
	if c == nil || c.counter == nil {
		return
	}
	now := time.Now().UTC()
	c.mu.Lock()
	if c.refreshing || (!c.lastAttempt.IsZero() && now.Before(c.lastAttempt.Add(imageAccessRefreshInterval))) {
		c.mu.Unlock()
		return
	}
	c.refreshing = true
	c.lastAttempt = now
	c.mu.Unlock()

	queryCtx, cancel := context.WithTimeout(ctx, imageAccessRefreshTimeout)
	counts, err := c.counter(queryCtx)
	cancel()
	bounded := make(map[string]int64, len(imageModerationRetryMetricStates))
	if err == nil {
		for _, state := range imageModerationRetryMetricStates {
			value := counts[state]
			if value < 0 {
				err = context.Canceled
				break
			}
			bounded[state] = value
		}
	}
	c.mu.Lock()
	c.refreshing = false
	if err != nil {
		c.refreshErrors++
	} else {
		c.ready = true
		c.counts = bounded
		c.lastSuccess = time.Now().UTC()
	}
	c.mu.Unlock()
}

func (c *imageModerationRetryCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	ready := c.ready
	refreshing := c.refreshing
	lastSuccess := c.lastSuccess
	errors := c.refreshErrors
	counts := make(map[string]int64, len(c.counts))
	for key, value := range c.counts {
		counts[key] = value
	}
	c.mu.RUnlock()
	if ready {
		for _, state := range imageModerationRetryMetricStates {
			ch <- prometheus.MustNewConstMetric(
				c.itemsDesc, prometheus.GaugeValue, float64(counts[state]), state,
			)
		}
		ch <- prometheus.MustNewConstMetric(
			c.lastSuccessDesc, prometheus.GaugeValue, float64(lastSuccess.Unix()),
		)
	}
	ch <- prometheus.MustNewConstMetric(c.readyDesc, prometheus.GaugeValue, boolFloat(ready))
	ch <- prometheus.MustNewConstMetric(c.refreshingDesc, prometheus.GaugeValue, boolFloat(refreshing))
	ch <- prometheus.MustNewConstMetric(c.refreshErrorsDesc, prometheus.CounterValue, float64(errors))
}
