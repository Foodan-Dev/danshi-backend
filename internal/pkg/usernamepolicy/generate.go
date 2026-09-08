//go:build ignore

// 在尚未发布 migration 18 时运行：go run ./internal/pkg/usernamepolicy/generate.go
// 已发布后禁止重写旧迁移；Unicode 策略升级必须新增迁移。
package main

import (
	"os"
	"strings"

	"github.com/Foodan-Dev/danshi-backend/internal/pkg/usernamepolicy"
)

func main() {
	const path = "migrations/00018_user_name_identity.sql"
	content, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	text := string(content)
	const start = "-- BEGIN GENERATED USERNAME POLICY\n"
	const end = "-- END GENERATED USERNAME POLICY"
	a, b := strings.Index(text, start), strings.Index(text, end)
	if a < 0 || b < a {
		panic("missing username policy markers")
	}
	sql := "CREATE FUNCTION danshi_valid_username_characters(value text)\nRETURNS boolean LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE\nRETURN (value COLLATE \"C\") ~ U&'" + usernamepolicy.SQLPattern() + "';\n"
	if err := os.WriteFile(path, []byte(text[:a]+start+sql+text[b:]), 0o644); err != nil {
		panic(err)
	}
}
