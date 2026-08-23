package contract_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	hertzconfig "github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/Foodan-Dev/danshi-backend/internal/apicontract"
	appconfig "github.com/Foodan-Dev/danshi-backend/internal/config"
	dbinfra "github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	routermiddleware "github.com/Foodan-Dev/danshi-backend/internal/middleware"
	"github.com/Foodan-Dev/danshi-backend/internal/router"
)

const (
	contractSecret = "contract-test-secret-longer-than-thirty-two-bytes"
	businessRoutes = 80
	runtimeRoutes  = 2
)

type response struct {
	status int
	body   map[string]any
}

var routeExpectedStatus = expectedStatusByRoute()

func expectedStatusByRoute() map[string]int {
	statuses := make(map[string]int, businessRoutes+runtimeRoutes)
	for _, declaration := range apicontract.Routes() {
		statuses[declaration.OperationKey()] = declaration.ExpectedStatus
	}
	return statuses
}

func TestRouteTableContract(t *testing.T) {
	engine := newContractEngine(t)
	routes := engine.Routes()
	require.Len(t, routes, businessRoutes+runtimeRoutes)

	actual := make(map[string]struct{}, len(routes))
	covered := make(map[string]int, len(routes))
	businessCount := 0
	runtimeCount := 0
	for _, route := range routes {
		key := operationKey(route.Method, route.Path)
		actual[key] = struct{}{}
		switch {
		case strings.HasPrefix(route.Path, router.APIPrefix+"/"):
			businessCount++
		case route.Path == "/health" || route.Path == "/ready":
			runtimeCount++
		default:
			t.Fatalf("路由表包含契约范围外的端点: %s", key)
		}
	}
	require.Equal(t, coverageSet(routeExpectedStatus), actual,
		"运行时路由与显式契约清单必须双向一致；新增路由必须登记预期状态")

	for _, route := range routes {
		key := operationKey(route.Method, route.Path)
		path := concretePath(route.Path)
		got := performRequest(t, engine, route.Method, path, requestPayload(route.Method), "")
		assertEnvelope(t, got)
		require.Equal(t, routeExpectedStatus[key], got.status, "%s 响应体=%v", key, got.body)
		covered[key]++
	}

	require.Equal(t, businessRoutes, businessCount)
	require.Equal(t, runtimeRoutes, runtimeCount)
	require.Equal(t, coverageSet(routeExpectedStatus), coverageSet(covered),
		"显式清单中的每条路由都必须由契约套件发起 HTTP 请求")
	for key, hits := range covered {
		require.Equal(t, 1, hits, "%s 应且仅应由路由表生成一次", key)
	}
}

func TestStatusBranchesContract(t *testing.T) {
	engine := newContractEngine(t)

	t.Run("401", func(t *testing.T) {
		got := performRequest(t, engine, http.MethodGet, "/api/v2/posts", nil, "")
		assertError(t, got, http.StatusUnauthorized, "unauthorized")
	})

	t.Run("403", func(t *testing.T) {
		token := registerContractUser(t, engine)
		got := performRequest(t, engine, http.MethodGet, "/api/v2/admin/posts", nil, token)
		assertError(t, got, http.StatusForbidden, "permission_denied")
	})

	t.Run("404", func(t *testing.T) {
		got := performRequest(t, engine, http.MethodGet, "/api/v2/not-a-contract-route", nil, "")
		assertError(t, got, http.StatusNotFound, "not_found")
	})

	t.Run("405", func(t *testing.T) {
		got := performRequest(t, engine, http.MethodPost, "/api/v2/config", map[string]any{}, "")
		assertError(t, got, http.StatusMethodNotAllowed, "method_not_allowed")
	})

	t.Run("422", func(t *testing.T) {
		got := performRequest(t, engine, http.MethodPost, "/api/v2/auth/register", map[string]any{
			"email": "not-an-email", "password": "short",
		}, "")
		assertError(t, got, http.StatusUnprocessableEntity, "validation_failed")
	})

	t.Run("500", func(t *testing.T) {
		faultEngine := newFaultEngine()
		got := performRequest(t, faultEngine, http.MethodGet, "/__contract/fault", nil, "")
		assertError(t, got, http.StatusInternalServerError, "internal_error")
		data, ok := got.body["data"].(map[string]any)
		require.True(t, ok, "500 data 必须是对象: %v", got.body)
		errorID, ok := data["error_id"].(string)
		require.True(t, ok)
		require.NotEmpty(t, errorID)
	})
}

func TestAdminNamespaceAlwaysRequiresAdmin(t *testing.T) {
	engine := newContractEngine(t)
	token := registerContractUser(t, engine)
	checked := 0
	for _, route := range engine.Routes() {
		if !strings.HasPrefix(route.Path, router.APIPrefix+"/admin/") {
			continue
		}
		got := performRequest(
			t, engine, route.Method, concretePath(route.Path), requestPayload(route.Method), token,
		)
		assertError(t, got, http.StatusForbidden, "permission_denied")
		checked++
	}
	require.Positive(t, checked, "必须实际检查至少一条 admin 路由")
}

func registerContractUser(t *testing.T, engine *server.Hertz) string {
	t.Helper()
	got := performRequest(t, engine, http.MethodPost, "/api/v2/auth/register", map[string]any{
		"email": "contract-user@fdueat.com", "password": "contract-password-123",
		"name": "契约测试用户", "gender": "female", "device_label": "contract-suite",
	}, "")
	assertEnvelope(t, got)
	require.Equal(t, http.StatusOK, got.status, "注册失败: %v", got.body)
	data, ok := got.body["data"].(map[string]any)
	require.True(t, ok, "注册 data 必须是对象: %v", got.body)
	token, ok := data["token"].(string)
	require.True(t, ok)
	require.NotEmpty(t, token)
	return token
}

func newContractEngine(t *testing.T) *server.Hertz {
	t.Helper()
	cfg, database := openContractPostgres(t)
	engine := server.New(
		server.WithHandleMethodNotAllowed(true),
		hertzconfig.Option{F: func(_ *hertzconfig.Options) {}},
	)
	router.Register(engine, router.Deps{Config: cfg, DB: database, Log: discardLogger()})
	return engine
}

func openContractPostgres(t *testing.T) (appconfig.Config, *dbinfra.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	container, err := tcpostgres.Run(
		ctx,
		"postgres:18",
		tcpostgres.WithDatabase("danshi_contract_test"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	testcontainers.CleanupContainer(t, container)

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	if strings.Contains(dsn, "?") {
		dsn += "&TimeZone=UTC"
	} else {
		dsn += "?TimeZone=UTC"
	}
	migrationDB, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	require.NoError(t, migrationDB.PingContext(ctx))
	require.NoError(t, dbinfra.Up(ctx, migrationDB))
	require.NoError(t, migrationDB.Close())

	cfg := appconfig.Config{
		Profile: appconfig.ProfileDev, DatabaseURL: dsn,
		DBMaxOpenConns: 5, DBMaxIdleConns: 2, DBConnMaxLifeS: 60,
		JWTSecretKey: contractSecret, JWTExpireMinutes: 60, JWTRefreshExpireDay: 30,
		EmailVerificationRequired: false, AllowedEmailDomains: "fdueat.com",
		ModerationCallbackToken: contractSecret,
	}
	database, err := dbinfra.Open(ctx, cfg, discardLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	return cfg, database
}

func newFaultEngine() *server.Hertz {
	engine := server.New(hertzconfig.Option{F: func(_ *hertzconfig.Options) {}})
	engine.Use(routermiddleware.Recovery(discardLogger()))
	engine.GET("/__contract/fault", func(_ context.Context, _ *app.RequestContext) {
		panic("contract test fault")
	})
	return engine
}

func performRequest(
	t *testing.T,
	engine *server.Hertz,
	method string,
	path string,
	payload any,
	token string,
) response {
	t.Helper()
	var body *ut.Body
	if payload != nil {
		encoded, err := json.Marshal(payload)
		require.NoError(t, err)
		body = &ut.Body{Body: bytes.NewReader(encoded), Len: len(encoded)}
	}
	headers := []ut.Header{{Key: "Content-Type", Value: "application/json"}}
	if token != "" {
		headers = append(headers, ut.Header{Key: "Authorization", Value: "Bearer " + token})
	}
	recorder := ut.PerformRequest(engine.Engine, method, path, body, headers...)
	result := recorder.Result()
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(result.Body(), &decoded), "响应不是 JSON: %s", result.Body())
	return response{status: result.StatusCode(), body: decoded}
}

func assertEnvelope(t *testing.T, got response) {
	t.Helper()
	require.Contains(t, got.body, "code")
	code, ok := got.body["code"].(float64)
	require.True(t, ok, "code 必须是 number: %v", got.body)
	require.EqualValues(t, got.status, code)
	message, ok := got.body["message"].(string)
	require.True(t, ok, "message 必须是 string: %v", got.body)
	require.NotEmpty(t, message)
	require.Contains(t, got.body, "data")

	if got.status >= http.StatusBadRequest {
		errorCode, ok := got.body["error_code"].(string)
		require.True(t, ok, "错误响应必须有 string error_code: %v", got.body)
		require.NotEmpty(t, errorCode)
	} else {
		require.NotContains(t, got.body, "error_code", "成功响应不得包含 error_code")
	}

	if got.status == http.StatusUnprocessableEntity {
		assertValidationData(t, got.body["data"])
	} else if got.status >= http.StatusBadRequest && got.status != http.StatusInternalServerError {
		require.Nil(t, got.body["data"], "非 422/500 错误的 data 必须是 null")
	}
	if got.status == http.StatusOK {
		assertSuccessData(t, got.body["data"])
	}
}

func assertValidationData(t *testing.T, value any) {
	t.Helper()
	data, ok := value.(map[string]any)
	require.True(t, ok, "422 data 必须是对象: %v", value)
	errors, ok := data["errors"].([]any)
	require.True(t, ok, "422 data.errors 必须是数组: %v", data)
	require.NotEmpty(t, errors)
	for _, value := range errors {
		fieldError, ok := value.(map[string]any)
		require.True(t, ok)
		for _, field := range []string{"field", "code", "message"} {
			text, ok := fieldError[field].(string)
			require.True(t, ok, "422 errors[].%s 必须是 string: %v", field, fieldError)
			require.NotEmpty(t, text)
		}
	}
}

func assertSuccessData(t *testing.T, value any) {
	t.Helper()
	if value == nil {
		return
	}
	data, ok := value.(map[string]any)
	if !ok {
		return
	}
	if status, exists := data["status"]; exists {
		_, ok := status.(string)
		require.True(t, ok, "runtime data.status 必须是 string: %v", data)
	}
	if _, exists := data["post_types"]; exists {
		for _, field := range []string{"post_types", "canteens", "cuisines", "flavors"} {
			_, ok := data[field].([]any)
			require.True(t, ok, "config data.%s 必须是 array: %v", field, data)
		}
	}
}

func assertError(t *testing.T, got response, status int, errorCode string) {
	t.Helper()
	assertEnvelope(t, got)
	require.Equal(t, status, got.status, "响应体=%v", got.body)
	require.Equal(t, errorCode, got.body["error_code"])
}

func operationKey(method string, path string) string { return method + " " + path }

func coverageSet[T any](covered map[string]T) map[string]struct{} {
	set := make(map[string]struct{}, len(covered))
	for key := range covered {
		set[key] = struct{}{}
	}
	return set
}

func concretePath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if strings.HasPrefix(part, ":") || strings.HasPrefix(part, "*") {
			parts[i] = "1"
		}
	}
	return strings.Join(parts, "/")
}

func requestPayload(method string) any {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return map[string]any{}
	default:
		return nil
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
