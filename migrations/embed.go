// Package migrations 把 SQL 迁移文件 embed 进二进制。
//
// embed 而不是读文件系统，是为了让 danshi-migrate 镜像能用 distroless/static：
// 镜像里除了二进制什么都没有，自然也就不存在「镜像里的 SQL 和仓库里的不一致」。
package migrations

import "embed"

// FS 包含随迁移二进制发布的全部 goose SQL 文件。
//
//go:embed *.sql
var FS embed.FS
