package usernamepolicy_test

import (
	"strings"
	"testing"

	"github.com/Foodan-Dev/danshi-backend/internal/pkg/usernamepolicy"
	"github.com/Foodan-Dev/danshi-backend/migrations"
)

func TestMigrationUsesApplicationUnicodePolicy(t *testing.T) {
	for _, file := range []string{"00018_user_name_identity.sql", "00022_username_spaces.sql"} {
		content, err := migrations.FS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), "U&'"+usernamepolicy.SQLPattern()+"'") {
			t.Fatal("SQL 与 Go Unicode 字符集不一致；未发布迁移可重新生成，已发布策略升级必须新增迁移")
		}
	}
}

func TestNormalizeSpaces(t *testing.T) {
	for input, want := range map[string]string{"  Ut   dolore  ": "Ut dolore", "　Ｕt　　美食　": "Ut 美食", "   ": "", "a\tb": "a\tb", "a\nb": "a\nb"} {
		if got := usernamepolicy.Normalize(input); got != want {
			t.Errorf("Normalize(%q)=%q, want %q", input, got, want)
		}
	}
	for _, value := range []string{"Ut dolore", "新的 名称"} {
		if !usernamepolicy.ValidCharacters(value) {
			t.Errorf("rejected %q", value)
		}
	}
	for _, value := range []string{"a\tb", "a\nb", "a-b"} {
		if usernamepolicy.ValidCharacters(value) {
			t.Errorf("accepted %q", value)
		}
	}
}
