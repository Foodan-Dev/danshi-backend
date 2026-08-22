package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudwego/hertz/pkg/common/hlog"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/require"
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
}

func TestDriftGateDetectsChangedSpec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "openapi.json")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o600))
	require.ErrorContains(t, writeOrCheck(path, []byte("new"), true), "已漂移")
	require.NoError(t, writeOrCheck(path, []byte("old"), true))
}
