package usernamepolicy_test

import (
	"strings"
	"testing"

	"github.com/Foodan-Dev/danshi-backend/internal/pkg/usernamepolicy"
	"github.com/Foodan-Dev/danshi-backend/migrations"
)

func TestMigrationUsesApplicationUnicodePolicy(t *testing.T) {
	content, err := migrations.FS.ReadFile("00018_user_name_identity.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "U&'"+usernamepolicy.SQLPattern()+"'") {
		t.Fatal("SQL 与 Go Unicode 字符集不一致；未发布迁移可重新生成，已发布策略升级必须新增迁移")
	}
}
