//go:build ignore

// 本文件仅用于开发时生成数据库字符约束，不参与服务构建或请求处理。
// 迁移 18 尚未发布时运行：go run ./internal/pkg/usernamepolicy/generate.go
// 输出由 policy.go 的同一字符集合生成；发布后禁止重写旧迁移，规则升级必须新增迁移。
package main

import (
	"os"
	"strings"

	"github.com/Foodan-Dev/danshi-backend/internal/pkg/usernamepolicy"
)

func main() {
	for _, path := range []string{"migrations/00018_user_name_identity.sql", "migrations/00022_username_spaces.sql"} {
		generate(path)
	}
}

func generate(path string) {
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
	verb := "CREATE FUNCTION"
	if strings.Contains(path, "00022") {
		verb = "CREATE OR REPLACE FUNCTION"
	}
	sql := verb + " danshi_valid_username_characters(value text)\nRETURNS boolean LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE\nRETURN (value COLLATE \"C\") ~ U&'" + usernamepolicy.SQLPattern() + "';\n"
	if err := os.WriteFile(path, []byte(text[:a]+start+sql+text[b:]), 0o644); err != nil {
		panic(err)
	}
}
