package apierrcodes

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckFailsWhenCodesSourceGainsConstant(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "codes.go")
	output := filepath.Join(directory, "codes_gen.go")
	writeTestCodes(t, input, false)
	if err := Write(input, output, false); err != nil {
		t.Fatalf("生成初始目录: %v", err)
	}
	writeTestCodes(t, input, true)
	if err := Write(input, output, true); err == nil {
		t.Fatal("codes.go 新增 BizCode 后，旧生成物必须触发漂移失败")
	}
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
