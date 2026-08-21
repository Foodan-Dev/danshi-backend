package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"

	"github.com/jingyijun/danshi_backend_go/migrations"
)

// ExpectedVersion 是本次构建期望的 schema 版本。
//
// server 启动时会核对，不符就拒绝启动（见 AssertVersion）。
// 这条门禁的意义：滚动更新时如果迁移还没跑完，新版本代码会连上旧 schema，
// 症状往往是「某个字段不存在」这类难查的运行时错误，不如直接起不来。
const ExpectedVersion int64 = 2

func gooseInit() error {
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	// advisory lock：多副本同时启动时只有一个真正执行迁移，其余等待
	goose.SetSequential(true)
	return nil
}

// Up 执行全部未应用的迁移。
func Up(ctx context.Context, sqlDB *sql.DB) error {
	if err := gooseInit(); err != nil {
		return err
	}
	return goose.UpContext(ctx, sqlDB, ".", goose.WithAllowMissing())
}

// DownOne 回滚一个版本。仅供开发与演练使用。
func DownOne(ctx context.Context, sqlDB *sql.DB) error {
	if err := gooseInit(); err != nil {
		return err
	}
	return goose.DownContext(ctx, sqlDB, ".")
}

// Version 返回当前已应用的最高版本。
func Version(ctx context.Context, sqlDB *sql.DB) (int64, error) {
	if err := gooseInit(); err != nil {
		return 0, err
	}
	return goose.GetDBVersionContext(ctx, sqlDB)
}

// Status 打印迁移状态。
func Status(ctx context.Context, sqlDB *sql.DB) error {
	if err := gooseInit(); err != nil {
		return err
	}
	return goose.StatusContext(ctx, sqlDB, ".")
}

// AssertVersion 是 server 的启动门禁。
//
// 只接受「完全相等」：版本偏低说明迁移没跑，偏高说明代码是旧的被回滚漏了。
// 两种情况都不该带着跑。
func AssertVersion(ctx context.Context, sqlDB *sql.DB) error {
	got, err := Version(ctx, sqlDB)
	if err != nil {
		return fmt.Errorf("读取 schema 版本失败: %w", err)
	}
	if got != ExpectedVersion {
		return fmt.Errorf(
			"schema 版本不符：期望 %d，实际 %d。"+
				"请先运行 danshi-migrate up；若实际版本更高，说明本服务镜像过旧",
			ExpectedVersion, got)
	}
	return nil
}
