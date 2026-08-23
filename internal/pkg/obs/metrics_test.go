package obs

import (
	"net/http"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	hertzconfig "github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/common/ut"
)

func TestRegisterMetricsRegistersRoute(t *testing.T) {
	h := server.New(hertzconfig.Option{F: func(*hertzconfig.Options) {}})
	RegisterMetrics(h)

	response := ut.PerformRequest(h.Engine, http.MethodGet, "/metrics", nil).Result()
	// 该测试只保护编排层可提前抓取的路由存在，不约束 P1.7 占位指标内容。
	if response.StatusCode() != http.StatusOK {
		t.Fatalf("GET /metrics 应返回 200 而不是 404，实际为 %d", response.StatusCode())
	}
}
