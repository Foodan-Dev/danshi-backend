package passwordx_test

import (
	"strings"
	"testing"

	"github.com/Foodan-Dev/danshi-backend/internal/pkg/passwordx"
)

// 由本机 Python bcrypt 实际生成：bcrypt.hashpw(b"danshi-test-password", bcrypt.gensalt())
// 迁移后存量用户必须能用原密码登录，这条断言就是那个保证。
const pythonHash = "$2b$12$.tR4UmM4YnDt97LElAniw.6SCzecEr7vDX9lNteF5bDqWoJNW2.wq"

func TestVerifyPythonGeneratedHash(t *testing.T) {
	if !passwordx.Verify("danshi-test-password", pythonHash) {
		t.Fatal("Python 生成的 bcrypt 哈希在 Go 侧校验失败——存量密码将无法登录")
	}
	if passwordx.Verify("wrong-password", pythonHash) {
		t.Fatal("错误密码不应通过")
	}
}

func TestRoundTrip(t *testing.T) {
	h, err := passwordx.Hash("s3cret-pass")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$2a$12$") && !strings.HasPrefix(h, "$2b$12$") {
		t.Fatalf("cost 应为 12，实际哈希: %s", h)
	}
	if !passwordx.Verify("s3cret-pass", h) {
		t.Fatal("自产哈希校验失败")
	}
}

func TestRejectsOverlongPassword(t *testing.T) {
	// bcrypt 会静默截断超过 72 字节的输入，两个不同长密码可能哈希相同，必须显式拒绝
	long := strings.Repeat("a", 73)
	if _, err := passwordx.Hash(long); err == nil {
		t.Fatal("超长密码应被拒绝而不是静默截断")
	}
}
