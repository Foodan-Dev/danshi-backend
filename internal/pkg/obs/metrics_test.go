package obs

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	hertzconfig "github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/common/ut"
)

func TestMetricsEndpointExportsRealCollectors(t *testing.T) {
	m := mustMetrics(t, nil)
	h := server.New(hertzconfig.Option{F: func(*hertzconfig.Options) {}})
	h.Use(m.Middleware())
	m.Register(h)
	h.GET("/api/v2/posts/:post_id", func(_ context.Context, c *app.RequestContext) {
		c.String(http.StatusCreated, "created")
	})

	response := ut.PerformRequest(h.Engine, http.MethodGet, "/api/v2/posts/123", nil).Result()
	if response.StatusCode() != http.StatusCreated {
		t.Fatalf("模板路由请求状态码 = %d，期望 %d", response.StatusCode(), http.StatusCreated)
	}

	response = ut.PerformRequest(h.Engine, http.MethodGet, "/metrics", nil).Result()
	if response.StatusCode() != http.StatusOK {
		t.Fatalf("GET /metrics 应返回 200 而不是 404，实际为 %d", response.StatusCode())
	}
	if contentType := string(response.Header.Peek("Content-Type")); !strings.HasPrefix(contentType, "text/plain") {
		t.Fatalf("/metrics Content-Type = %q，期望 Prometheus 文本格式", contentType)
	}

	body := string(response.Body())
	for _, metric := range []string{
		"danshi_http_server_requests_total",
		"danshi_http_server_request_duration_seconds_bucket",
		"danshi_http_server_response_size_bytes_bucket",
		"go_gc_duration_seconds",
		"process_cpu_seconds_total",
	} {
		if !strings.Contains(body, metric) {
			t.Errorf("/metrics 缺少 %s", metric)
		}
	}
	if !strings.Contains(body, `route="/api/v2/posts/:post_id"`) {
		t.Fatalf("HTTP 指标必须使用路由模板，实际输出:\n%s", body)
	}
	if strings.Contains(body, `route="/api/v2/posts/123"`) {
		t.Fatalf("HTTP 指标泄露了实际路径，产生高基数标签:\n%s", body)
	}
}

func TestDBStatsCollectorReflectsSQLPoolState(t *testing.T) {
	pool := sql.OpenDB(stubConnector{})
	pool.SetMaxOpenConns(2)
	pool.SetMaxIdleConns(2)
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Errorf("关闭测试连接池: %v", err)
		}
	})

	ctx := context.Background()
	if err := pool.PingContext(ctx); err != nil {
		t.Fatalf("预热测试连接池: %v", err)
	}
	held, err := pool.Conn(ctx)
	if err != nil {
		t.Fatalf("占用测试连接: %v", err)
	}
	t.Cleanup(func() {
		if err := held.Close(); err != nil {
			t.Errorf("归还测试连接: %v", err)
		}
	})
	if err := pool.PingContext(ctx); err != nil {
		t.Fatalf("创建空闲测试连接: %v", err)
	}

	m := mustMetrics(t, pool)
	h := server.New(hertzconfig.Option{F: func(*hertzconfig.Options) {}})
	m.Register(h)
	body := string(ut.PerformRequest(h.Engine, http.MethodGet, "/metrics", nil).Result().Body())
	for _, sample := range []string{
		`danshi_db_pool_connections{state="idle"} 1`,
		`danshi_db_pool_connections{state="in_use"} 1`,
		`danshi_db_pool_open_connections 2`,
		`danshi_db_pool_max_open_connections 2`,
		`danshi_db_pool_wait_total 0`,
		`danshi_db_pool_wait_duration_seconds_total 0`,
	} {
		if !strings.Contains(body, sample) {
			t.Errorf("连接池指标缺少 %q，实际输出:\n%s", sample, body)
		}
	}
}

func TestMetricsInitializationUsesIndependentRegistries(t *testing.T) {
	for range 2 {
		m := mustMetrics(t, nil)
		if _, err := m.registry.Gather(); err != nil {
			t.Fatalf("收集独立 registry: %v", err)
		}
	}
}

func TestMetricsRouteStaysOutsideLaterBusinessMiddleware(t *testing.T) {
	m := mustMetrics(t, nil)
	h := server.New(hertzconfig.Option{F: func(*hertzconfig.Options) {}})
	h.Use(m.Middleware())
	m.Register(h)

	businessMiddlewareCalls := 0
	h.Use(func(ctx context.Context, c *app.RequestContext) {
		businessMiddlewareCalls++
		c.Next(ctx)
	})
	h.GET("/business", func(_ context.Context, c *app.RequestContext) {
		c.String(http.StatusOK, "ok")
	})

	response := ut.PerformRequest(h.Engine, http.MethodGet, "/metrics", nil).Result()
	if response.StatusCode() != http.StatusOK {
		t.Fatalf("GET /metrics 状态码 = %d", response.StatusCode())
	}
	if businessMiddlewareCalls != 0 {
		t.Fatalf("/metrics 进入了后挂载的业务/UoW 中间件 %d 次", businessMiddlewareCalls)
	}
	_ = ut.PerformRequest(h.Engine, http.MethodGet, "/business", nil).Result()
	if businessMiddlewareCalls != 1 {
		t.Fatalf("业务路由应进入业务中间件，实际 %d 次", businessMiddlewareCalls)
	}
}

func mustMetrics(t *testing.T, pool *sql.DB) *Metrics {
	t.Helper()
	m, err := NewMetrics(pool)
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	return m
}

type stubConnector struct{}

func (stubConnector) Connect(context.Context) (driver.Conn, error) { return stubConn{}, nil }
func (stubConnector) Driver() driver.Driver                        { return stubDriver{} }

type stubDriver struct{}

func (stubDriver) Open(string) (driver.Conn, error) { return stubConn{}, nil }

type stubConn struct{}

func (stubConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("stub connection does not prepare statements")
}

func (stubConn) Close() error { return nil }
func (stubConn) Begin() (driver.Tx, error) {
	return nil, errors.New("stub connection does not begin transactions")
}
func (stubConn) Ping(context.Context) error { return nil }
