package apierrcodes

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseIncludesNewCodeWithoutIntermediateArtifact(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "codes.go")
	writeTestCodes(t, input, true)
	catalog, err := Parse(input)
	require.NoError(t, err)
	require.Equal(t, []string{"required"}, catalog.FieldCodes)
	require.Equal(t, []string{"internal_error", "new_code"}, catalog.BizCodes)
}

func writeTestCodes(t *testing.T, path string, addBizCode bool) {
	t.Helper()
	source := `package apierr

type FieldCode string
const FieldRequired FieldCode = "required"

type BizCode string
const BizInternal BizCode = "internal_error"
`
	if addBizCode {
		source += "const BizNew BizCode = \"new_code\"\n"
	}
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("写入测试错误码源文件: %v", err)
	}
}
