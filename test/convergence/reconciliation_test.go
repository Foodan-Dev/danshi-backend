package convergence_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	hertzconfig "github.com/cloudwego/hertz/pkg/common/config"
	"github.com/stretchr/testify/require"

	appconfig "github.com/jingyijun/danshi_backend_go/internal/config"
	"github.com/jingyijun/danshi_backend_go/internal/router"
)

const (
	pythonBusinessOperations = 48
	goBusinessRoutes         = 77
)

var operationPattern = regexp.MustCompile(
	`\b(GET|POST|PUT|PATCH|DELETE)[ \t]+(/[A-Za-z0-9_{}:./-]+)`,
)

var (
	placeholderPattern  = regexp.MustCompile(`\{([^}/]+)\}`)
	domainReportPattern = regexp.MustCompile(`^(0[2-9]|10)-.*\.md$`)
)

type openAPIDocument struct {
	Paths map[string]map[string]json.RawMessage `json:"paths"`
}

func TestEndpointReconciliation(t *testing.T) {
	root := repositoryRoot(t)
	python := loadPythonBusinessOperations(t, filepath.Join(root, "api/baseline-python.json"))
	goRoutes := loadGoBusinessRoutes(t)
	require.Len(t, python, pythonBusinessOperations)
	require.Len(t, goRoutes, goBusinessRoutes)

	breakingPath := filepath.Join(root, "api/BREAKING-CHANGES.md")
	breaking := readText(t, breakingPath)
	require.Contains(t, breaking, "§4.17")
	require.Contains(t, breaking, "`/api/v1`")
	require.Contains(t, breaking, "`/api/v2`")

	registeredExceptions := registeredBaselineExceptions(breaking)
	mappedPython := make(map[string]struct{}, len(python))
	missingPython := make([]string, 0)
	for operation := range python {
		mapped := mapPythonOperation(operation)
		if _, exists := goRoutes[mapped]; exists {
			mappedPython[mapped] = struct{}{}
			continue
		}
		if _, registered := registeredExceptions[normalizePlaceholders(operation)]; registered {
			continue
		}
		missingPython = append(missingPython, operation+" -> "+mapped)
	}

	documented := documentedGoOperations(t, root, breaking)
	undocumentedGo := make([]string, 0)
	additions := make([]string, 0)
	for operation := range goRoutes {
		if _, existedInPython := mappedPython[operation]; existedInPython {
			continue
		}
		additions = append(additions, operation)
		if _, hasEvidence := documented[operation]; !hasEvidence {
			undocumentedGo = append(undocumentedGo, operation)
		}
	}
	sort.Strings(missingPython)
	sort.Strings(additions)
	sort.Strings(undocumentedGo)

	t.Logf(
		"Python business=%d, Go business=%d, mapped=%d, registered exceptions=%d, Go additions=%d",
		len(python), len(goRoutes), len(mappedPython), len(registeredExceptions), len(additions),
	)
	require.Empty(t, missingPython,
		"Python 基线端点既未在 Go 实现，也没有在 BREAKING-CHANGES 登记删除或改名:\n%s",
		strings.Join(missingPython, "\n"))
	require.Empty(t, undocumentedGo,
		"Go 新增端点既未在新增登记表，也没有在 domain 报告中提供依据:\n%s",
		strings.Join(undocumentedGo, "\n"))
}

func loadPythonBusinessOperations(t *testing.T, path string) map[string]struct{} {
	t.Helper()
	var document openAPIDocument
	require.NoError(t, json.Unmarshal([]byte(readText(t, path)), &document))
	operations := make(map[string]struct{})
	for path, pathItem := range document.Paths {
		if !strings.HasPrefix(path, "/api/v1/") {
			continue
		}
		for method := range pathItem {
			method = strings.ToUpper(method)
			if !isHTTPMethod(method) {
				continue
			}
			operation := method + " " + path
			require.NotContains(t, operations, operation, "Python 基线 operation 重复")
			operations[operation] = struct{}{}
		}
	}
	return operations
}

func loadGoBusinessRoutes(t *testing.T) map[string]struct{} {
	t.Helper()
	engine := server.New(
		server.WithDisablePrintRoute(true),
		hertzconfig.Option{F: func(_ *hertzconfig.Options) {}},
	)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router.Register(engine, router.Deps{
		Config: appconfig.Config{
			Profile:      appconfig.ProfileDev,
			JWTSecretKey: "convergence-test-secret-longer-than-thirty-two-bytes",
		},
		Log: log,
	})
	operations := make(map[string]struct{})
	for _, route := range engine.Routes() {
		if !strings.HasPrefix(route.Path, router.APIPrefix+"/") {
			continue
		}
		operation := route.Method + " " + route.Path
		require.NotContains(t, operations, operation, "Go 运行时路由重复")
		operations[operation] = struct{}{}
	}
	return operations
}

func registeredBaselineExceptions(breaking string) map[string]struct{} {
	exceptions := make(map[string]struct{})
	for _, line := range strings.Split(breaking, "\n") {
		if !strings.Contains(line, "删除") && !strings.Contains(line, "移除") &&
			!strings.Contains(line, "改名") && !strings.Contains(line, "重命名") {
			continue
		}
		for _, match := range operationPattern.FindAllStringSubmatch(line, -1) {
			if strings.HasPrefix(match[2], "/api/v1/") {
				exceptions[match[1]+" "+normalizePlaceholders(match[2])] = struct{}{}
			}
		}
	}
	return exceptions
}

func documentedGoOperations(t *testing.T, root string, breaking string) map[string][]string {
	t.Helper()
	documented := make(map[string][]string)
	additionHeading := "## 新增、勘误与行为变更"
	additionIndex := strings.Index(breaking, additionHeading)
	require.NotEqual(t, -1, additionIndex, "BREAKING-CHANGES 缺少新增登记章节")
	collectDocumentedOperations(documented, breaking[additionIndex:], "api/BREAKING-CHANGES.md")

	reports, err := filepath.Glob(filepath.Join(root, ".codex/reports/*.md"))
	require.NoError(t, err)
	for _, report := range reports {
		if !domainReportPattern.MatchString(filepath.Base(report)) {
			continue
		}
		collectDocumentedOperations(documented, readText(t, report), filepath.Base(report))
	}
	return documented
}

func collectDocumentedOperations(target map[string][]string, text string, source string) {
	for _, match := range operationPattern.FindAllStringSubmatch(text, -1) {
		path := normalizePlaceholders(match[2])
		if !strings.HasPrefix(path, "/api/v2/") {
			path = "/api/v2" + path
		}
		operation := match[1] + " " + path
		target[operation] = append(target[operation], source)
	}
}

func mapPythonOperation(operation string) string {
	parts := strings.SplitN(operation, " ", 2)
	path := strings.Replace(parts[1], "/api/v1/", "/api/v2/", 1)
	return parts[0] + " " + normalizePlaceholders(path)
}

func normalizePlaceholders(path string) string {
	return placeholderPattern.ReplaceAllString(path, `:$1`)
}

func isHTTPMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "../.."))
}

func readText(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(content)
}
