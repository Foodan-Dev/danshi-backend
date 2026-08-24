package obs

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/common/expfmt"
)

const (
	metricsPath        = "/metrics"
	unmatchedRoute     = "unmatched"
	metricsContentType = "text/plain; version=0.0.4; charset=utf-8"
)

// Metrics 持有应用自己的 Prometheus registry。每次初始化都创建独立 registry，
// 避免测试和同进程多实例因重复注册全局 collector 而 panic。
type Metrics struct {
	registry        *prometheus.Registry
	requestTotal    *prometheus.CounterVec
	requestDuration *prometheus.HistogramVec
	responseSize    *prometheus.HistogramVec
	moderation      moderationMetrics
	verification    verificationMetrics
	reviewQueue     *reviewQueueCollector
}

type metricsOptions struct {
	reviewQueueCounter ReviewQueueCounter
}

// MetricsOption 配置可选的应用指标 collector。
type MetricsOption func(*metricsOptions)

// WithReviewQueueCounter 注册与管理端待复核列表相同口径的实时计数函数。
func WithReviewQueueCounter(counter ReviewQueueCounter) MetricsOption {
	return func(options *metricsOptions) {
		options.reviewQueueCounter = counter
	}
}

// NewMetrics 创建 HTTP、Go、进程与可选数据库连接池指标。
func NewMetrics(pool *sql.DB, opts ...MetricsOption) (*Metrics, error) {
	options := metricsOptions{}
	for _, opt := range opts {
		opt(&options)
	}
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		requestTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "danshi",
			Subsystem: "http_server",
			Name:      "requests_total",
			Help:      "Total number of HTTP requests handled by route template.",
		}, []string{"route", "method", "status"}),
		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "danshi",
			Subsystem: "http_server",
			Name:      "request_duration_seconds",
			Help:      "HTTP request duration in seconds by route template.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"route", "method", "status"}),
		responseSize: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "danshi",
			Subsystem: "http_server",
			Name:      "response_size_bytes",
			Help:      "HTTP response size in bytes by route template.",
			Buckets:   prometheus.ExponentialBuckets(128, 2, 16),
		}, []string{"route", "method", "status"}),
		moderation:   newModerationMetrics(),
		verification: newVerificationMetrics(),
	}

	metricCollectors := []prometheus.Collector{
		m.requestTotal,
		m.requestDuration,
		m.responseSize,
		m.moderation.submissions,
		m.moderation.providerFailures,
		m.moderation.terminalOutcomes,
		m.moderation.callbacks,
		m.verification.events,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	}
	if pool != nil {
		metricCollectors = append(metricCollectors, newDBStatsCollector(pool))
	}
	if options.reviewQueueCounter != nil {
		m.reviewQueue = newReviewQueueCollector(options.reviewQueueCounter)
		metricCollectors = append(metricCollectors, m.reviewQueue)
	}
	for _, collector := range metricCollectors {
		if err := m.registry.Register(collector); err != nil {
			return nil, fmt.Errorf("注册 Prometheus collector 失败: %w", err)
		}
	}
	return m, nil
}

// Middleware 记录 HTTP 请求指标。route 只取 Hertz 路由模板；未匹配请求统一
// 归入 unmatched，绝不回退到包含资源 ID 的实际 URL path。
func (m *Metrics) Middleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		started := time.Now()
		c.Next(ctx)

		route := routeTemplate(c)
		method := boundedMethod(string(c.Method()))
		status := boundedStatus(c.Response.StatusCode())
		labels := []string{route, method, status}
		m.requestTotal.WithLabelValues(labels...).Inc()
		m.requestDuration.WithLabelValues(labels...).Observe(time.Since(started).Seconds())
		m.responseSize.WithLabelValues(labels...).Observe(float64(len(c.Response.Body())))
	}
}

// Register 挂载 Prometheus 拉取端点。调用方应在业务路由与 UoW 中间件之前调用，
// 保证 /metrics 不鉴权也不开事务。
func (m *Metrics) Register(h *server.Hertz) {
	h.GET(metricsPath, m.handle)
}

func (m *Metrics) handle(ctx context.Context, c *app.RequestContext) {
	if m.reviewQueue != nil {
		m.reviewQueue.Refresh(ctx)
	}
	families, err := m.registry.Gather()
	if err != nil {
		c.String(http.StatusInternalServerError, "metrics collection failed\n")
		return
	}

	var output bytes.Buffer
	encoder := expfmt.NewEncoder(&output, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, family := range families {
		if err := encoder.Encode(family); err != nil {
			c.String(http.StatusInternalServerError, "metrics encoding failed\n")
			return
		}
	}
	c.Data(http.StatusOK, metricsContentType, output.Bytes())
}

func routeTemplate(c *app.RequestContext) string {
	if route := c.FullPath(); route != "" {
		return route
	}
	return unmatchedRoute
}

func boundedMethod(method string) string {
	switch method {
	case http.MethodConnect,
		http.MethodDelete,
		http.MethodGet,
		http.MethodHead,
		http.MethodOptions,
		http.MethodPatch,
		http.MethodPost,
		http.MethodPut,
		http.MethodTrace:
		return method
	default:
		return "OTHER"
	}
}

func boundedStatus(status int) string {
	if status < 100 || status > 599 {
		return "OTHER"
	}
	return strconv.Itoa(status)
}

type dbStatsCollector struct {
	pool               *sql.DB
	connections        *prometheus.Desc
	openConnections    *prometheus.Desc
	maxOpenConnections *prometheus.Desc
	waitTotal          *prometheus.Desc
	waitDuration       *prometheus.Desc
}

func newDBStatsCollector(pool *sql.DB) prometheus.Collector {
	return &dbStatsCollector{
		pool: pool,
		connections: prometheus.NewDesc(
			"danshi_db_pool_connections",
			"Current database connections by bounded state.",
			[]string{"state"}, nil,
		),
		openConnections: prometheus.NewDesc(
			"danshi_db_pool_open_connections",
			"Current number of open database connections.",
			nil, nil,
		),
		maxOpenConnections: prometheus.NewDesc(
			"danshi_db_pool_max_open_connections",
			"Configured maximum number of open database connections.",
			nil, nil,
		),
		waitTotal: prometheus.NewDesc(
			"danshi_db_pool_wait_total",
			"Total number of waits for a database connection.",
			nil, nil,
		),
		waitDuration: prometheus.NewDesc(
			"danshi_db_pool_wait_duration_seconds_total",
			"Total time blocked waiting for a database connection in seconds.",
			nil, nil,
		),
	}
}

func (c *dbStatsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.connections
	ch <- c.openConnections
	ch <- c.maxOpenConnections
	ch <- c.waitTotal
	ch <- c.waitDuration
}

func (c *dbStatsCollector) Collect(ch chan<- prometheus.Metric) {
	stats := c.pool.Stats()
	ch <- prometheus.MustNewConstMetric(c.connections, prometheus.GaugeValue, float64(stats.InUse), "in_use")
	ch <- prometheus.MustNewConstMetric(c.connections, prometheus.GaugeValue, float64(stats.Idle), "idle")
	ch <- prometheus.MustNewConstMetric(c.openConnections, prometheus.GaugeValue, float64(stats.OpenConnections))
	ch <- prometheus.MustNewConstMetric(c.maxOpenConnections, prometheus.GaugeValue, float64(stats.MaxOpenConnections))
	ch <- prometheus.MustNewConstMetric(c.waitTotal, prometheus.CounterValue, float64(stats.WaitCount))
	ch <- prometheus.MustNewConstMetric(
		c.waitDuration,
		prometheus.CounterValue,
		stats.WaitDuration.Seconds(),
	)
}
