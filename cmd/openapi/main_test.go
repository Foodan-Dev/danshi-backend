package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/common/hlog"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/codegen/apierrcodes"
)

const repositoryCodesPath = "../../internal/apierr/codes.go"

func TestRepositoryRegistryGenerates84ValidOperations(t *testing.T) {
	hlog.SetOutput(io.Discard)
	encoded, err := generateSpec(repositoryCodesPath)
	require.NoError(t, err)
	catalog, err := apierrcodes.Parse(repositoryCodesPath)
	require.NoError(t, err)
	document, err := openapi3.NewLoader().LoadFromData(encoded)
	require.NoError(t, err)
	require.NoError(t, document.Validate(context.Background()))

	operations := 0
	for _, item := range document.Paths.Map() {
		operations += len(item.Operations())
	}
	require.Equal(t, 98, operations)
	require.NotNil(t, document.Paths.Value("/api/v2/posts").Post.RequestBody)
	require.NotNil(t, document.Paths.Value("/api/v2/posts/{post_id}").Get.Security)
	deleteUser := document.Paths.Value("/api/v2/users/{user_id}").Delete
	require.NotNil(t, deleteUser.Security)
	require.Nil(t, deleteUser.RequestBody)
	require.NotNil(t, deleteUser.Responses.Value("403"))
	require.Nil(t, document.Paths.Value("/api/v2/config").Get.Security)
	require.Equal(t,
		[]string{"200", "400", "401", "403", "404", "409", "422", "500", "503"},
		document.Paths.Value("/api/v2/posts").Post.Responses.Keys(),
	)
	postConflict := document.Paths.Value("/api/v2/posts").Post.Responses.Value("409")
	require.Equal(t, codeEnums(catalog.BizCodes),
		postConflict.Value.Content.Get("application/json").Schema.Value.Properties["error_code"].Value.Enum)
	fieldError := document.Components.Schemas["FieldError"].Value
	require.Equal(t, codeEnums(catalog.FieldCodes), fieldError.Properties["code"].Value.Enum)
	assertRepositoryQueryParameters(t, document)
	assertInferredResponses(t, document)
}

func TestDriftGateDetectsChangedSpec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openapi.json")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o600))
	require.ErrorContains(t, writeOrCheck(path, []byte("new"), true), "已漂移")
	require.NoError(t, writeOrCheck(path, []byte("old"), true))
}

func TestErrorCodeChangeTripsSpecDriftGate(t *testing.T) {
	baseline, err := generateSpec(repositoryCodesPath)
	require.NoError(t, err)
	source, err := os.ReadFile(repositoryCodesPath)
	require.NoError(t, err)

	directory := t.TempDir()
	codesPath := filepath.Join(directory, "codes.go")
	source = append(source, []byte("\nconst BizDriftProbe BizCode = \"drift_probe\"\n")...)
	require.NoError(t, os.WriteFile(codesPath, source, 0o600))
	changed, err := generateSpec(codesPath)
	require.NoError(t, err)
	require.Contains(t, string(changed), "drift_probe")

	specPath := filepath.Join(directory, "openapi.json")
	require.NoError(t, os.WriteFile(specPath, baseline, 0o600))
	require.ErrorContains(t, writeOrCheck(specPath, changed, true), "已漂移")
}

func codeEnums(codes []string) []any {
	values := make([]any, 0, len(codes))
	for _, code := range codes {
		values = append(values, code)
	}
	return values
}

func assertInferredResponses(t *testing.T, document *openapi3.T) {
	t.Helper()
	for path, item := range document.Paths.Map() {
		for method, operation := range item.Operations() {
			key := method + " " + path
			require.NotNil(t, operation.Responses.Value("500"), "%s 缺少全局 500", key)
			if operation.Security != nil {
				require.NotNil(t, operation.Responses.Value("401"), "%s 缺少鉴权 401", key)
			}
			if hasParameterIn(operation, "path") {
				require.NotNil(t, operation.Responses.Value("404"), "%s 缺少资源 404", key)
				require.NotNil(t, operation.Responses.Value("422"), "%s 缺少路径校验 422", key)
			}
			if operation.RequestBody != nil && path != "/api/v2/moderation/tencent-ci/callback" {
				require.NotNil(t, operation.Responses.Value("422"), "%s 缺少请求体校验 422", key)
			}
			if strings.HasPrefix(path, "/api/v2/admin/") {
				require.NotNil(t, operation.Responses.Value("403"), "%s 缺少管理员权限 403", key)
			}
		}
	}
}

func assertRepositoryQueryParameters(t *testing.T, document *openapi3.T) {
	t.Helper()
	posts := document.Paths.Value("/api/v2/posts").Get
	require.Equal(t, []string{
		"post_type", "share_type", "category", "canteen_code", "cuisine", "flavors", "tags",
		"min_price", "max_price", "sort_by", "cursor", "page", "limit",
	}, parameterNames(posts))
	require.Len(t, posts.Parameters, 13)
	require.Equal(t, []any{"share", "seeking"}, parameterByName(t, posts, "post_type").Schema.Value.Enum)
	require.Equal(t, []any{"latest", "hot", "trending", "price"},
		parameterByName(t, posts, "sort_by").Schema.Value.Enum)
	require.Equal(t, "latest", parameterByName(t, posts, "sort_by").Schema.Value.Default)
	page := parameterByName(t, posts, "page")
	require.Equal(t, openapi3.TypeInteger, page.Schema.Value.Type.Slice()[0])
	require.EqualValues(t, 1, page.Schema.Value.Default)
	require.EqualValues(t, 1, *page.Schema.Value.Min)
	limit := parameterByName(t, posts, "limit")
	require.EqualValues(t, 20, limit.Schema.Value.Default)
	require.EqualValues(t, 1, *limit.Schema.Value.Min)
	require.EqualValues(t, 100, *limit.Schema.Value.Max)
	flavors := parameterByName(t, posts, "flavors")
	require.Equal(t, openapi3.TypeArray, flavors.Schema.Value.Type.Slice()[0])
	require.Equal(t, "form", flavors.Style)
	require.NotNil(t, flavors.Explode)
	require.False(t, *flavors.Explode)
	require.Equal(t, `^(?:0|[0-9]{1,8})(?:\.[0-9]{1,2})?$`,
		parameterByName(t, posts, "min_price").Schema.Value.Pattern)
	require.Equal(t, openapi3.TypeString,
		parameterByName(t, posts, "cursor").Schema.Value.Type.Slice()[0])

	searchPosts := document.Paths.Value("/api/v2/search/posts").Get
	require.Len(t, searchPosts.Parameters, 13)
	require.Equal(t, []string{
		"q", "post_type", "share_type", "category", "canteen_code", "cuisine", "flavors", "tags",
		"min_price", "max_price", "sort_by", "page", "limit",
	}, parameterNames(searchPosts))
	require.True(t, parameterByName(t, searchPosts, "q").Required)
	require.Empty(t, parameterByName(t, searchPosts, "sort_by").Schema.Value.Enum)
	require.Nil(t, parameterByName(t, searchPosts, "sort_by").Schema.Value.Default)

	replies := document.Paths.Value("/api/v2/comments/{comment_id}/replies").Get
	require.EqualValues(t, 10, parameterByName(t, replies, "limit").Schema.Value.Default)
	notifications := document.Paths.Value("/api/v2/notifications").Get
	require.Equal(t, []string{"is_read", "type", "cursor", "limit"}, parameterNames(notifications))
	require.Nil(t, findParameter(notifications, "page"))

	hybrid := document.Components.Schemas["HybridMeta"]
	require.NotNil(t, hybrid)
	require.Len(t, hybrid.Value.OneOf, 2, "帖子响应必须显式声明 offset/cursor 分页联合契约")
	require.Contains(t, hybrid.Value.OneOf[0].Value.Properties, "total")
	require.Contains(t, hybrid.Value.OneOf[1].Value.Properties, "next_cursor")
	cursorMeta := document.Components.Schemas["CursorMeta"]
	require.NotNil(t, cursorMeta)
	require.Equal(t, []string{"has_more", "limit", "next_cursor"}, sortedPropertyNames(cursorMeta.Value))
	callback := document.Paths.Value("/api/v2/moderation/tencent-ci/callback").Post
	require.True(t, parameterByName(t, callback, "token").Required)
}

func hasParameterIn(operation *openapi3.Operation, location string) bool {
	for _, parameter := range operation.Parameters {
		if parameter.Value.In == location {
			return true
		}
	}
	return false
}

func parameterNames(operation *openapi3.Operation) []string {
	names := make([]string, 0, len(operation.Parameters))
	for _, parameter := range operation.Parameters {
		if parameter.Value.In == "query" {
			names = append(names, parameter.Value.Name)
		}
	}
	return names
}

func parameterByName(t *testing.T, operation *openapi3.Operation, name string) *openapi3.Parameter {
	t.Helper()
	for _, parameter := range operation.Parameters {
		if parameter.Value.In == "query" && parameter.Value.Name == name {
			return parameter.Value
		}
	}
	require.FailNow(t, "query 参数不存在", "name=%s", name)
	return nil
}

func findParameter(operation *openapi3.Operation, name string) *openapi3.Parameter {
	for _, parameter := range operation.Parameters {
		if parameter.Value.In == "query" && parameter.Value.Name == name {
			return parameter.Value
		}
	}
	return nil
}

func sortedPropertyNames(schema *openapi3.Schema) []string {
	names := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
