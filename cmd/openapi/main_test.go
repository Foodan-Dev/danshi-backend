package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/common/hlog"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/require"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
)

func TestRepositoryRegistryGenerates79ValidOperations(t *testing.T) {
	hlog.SetOutput(io.Discard)
	encoded, err := generateSpec()
	require.NoError(t, err)
	document, err := openapi3.NewLoader().LoadFromData(encoded)
	require.NoError(t, err)
	require.NoError(t, document.Validate(context.Background()))

	operations := 0
	for _, item := range document.Paths.Map() {
		operations += len(item.Operations())
	}
	require.Equal(t, 79, operations)
	require.NotNil(t, document.Paths.Value("/api/v2/posts").Post.RequestBody)
	require.NotNil(t, document.Paths.Value("/api/v2/posts/{post_id}").Get.Security)
	require.Nil(t, document.Paths.Value("/api/v2/config").Get.Security)
	require.Equal(t,
		[]string{"200", "400", "401", "403", "404", "409", "422", "500", "503"},
		document.Paths.Value("/api/v2/posts").Post.Responses.Keys(),
	)
	postConflict := document.Paths.Value("/api/v2/posts").Post.Responses.Value("409")
	require.Equal(t, codeEnums(apierr.AllBizCodes()),
		postConflict.Value.Content.Get("application/json").Schema.Value.Properties["error_code"].Value.Enum)
	fieldError := document.Components.Schemas["FieldError"].Value
	require.Equal(t, codeEnums(apierr.AllFieldCodes()), fieldError.Properties["code"].Value.Enum)
	assertInferredResponses(t, document)
}

func TestDriftGateDetectsChangedSpec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openapi.json")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o600))
	require.ErrorContains(t, writeOrCheck(path, []byte("new"), true), "已漂移")
	require.NoError(t, writeOrCheck(path, []byte("old"), true))
}

func codeEnums[T ~string](codes []T) []any {
	values := make([]any, 0, len(codes))
	for _, code := range codes {
		values = append(values, string(code))
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
			if len(operation.Parameters) > 0 {
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
