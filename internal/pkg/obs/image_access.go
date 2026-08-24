package obs

import (
	"context"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	imageAccessRefreshTimeout  = time.Second
	imageAccessRefreshInterval = 15 * time.Second
)

var imageAccessMetricStates = [...]string{
	"pending_acl", "pending_submit", "submitting", "submitted", "succeeded", "dead_letter",
}

// ImageAccessStateCounter 返回固定状态的 durable delivery 数量。
type ImageAccessStateCounter func(context.Context) (map[string]int64, error)

type imageAccessCollector struct {
	counter ImageAccessStateCounter

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

func newImageAccessCollector(counter ImageAccessStateCounter) *imageAccessCollector {
	return &imageAccessCollector{
		counter: counter,
		itemsDesc: prometheus.NewDesc(
			"danshi_image_access_delivery_cached_items",
			"Last successfully observed durable image access deliveries by bounded state.",
			[]string{"state"}, nil,
		),
		readyDesc: prometheus.NewDesc(
			"danshi_image_access_delivery_cache_ready",
			"Whether a successful durable image access observation is available.", nil, nil,
		),
		lastSuccessDesc: prometheus.NewDesc(
			"danshi_image_access_delivery_last_success_timestamp_seconds",
			"Unix timestamp of the last successful durable image access observation.", nil, nil,
		),
		refreshingDesc: prometheus.NewDesc(
			"danshi_image_access_delivery_refresh_in_progress",
			"Whether one bounded durable image access refresh is running.", nil, nil,
		),
		refreshErrorsDesc: prometheus.NewDesc(
			"danshi_image_access_delivery_refresh_errors_total",
			"Total failed durable image access refresh attempts.", nil, nil,
		),
	}
}

func (c *imageAccessCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.itemsDesc
	ch <- c.readyDesc
	ch <- c.lastSuccessDesc
	ch <- c.refreshingDesc
	ch <- c.refreshErrorsDesc
}

func (c *imageAccessCollector) Refresh(ctx context.Context) {
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
	bounded := make(map[string]int64, len(imageAccessMetricStates))
	if err == nil {
		for _, state := range imageAccessMetricStates {
			value := counts[state]
			if value < 0 {
				err = context.Canceled // 只暴露失败状态，不保留非法值或错误正文。
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

func (c *imageAccessCollector) Collect(ch chan<- prometheus.Metric) {
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
		for _, state := range imageAccessMetricStates {
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
