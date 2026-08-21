// Package db 是数据库接入层，对应 Python 侧的 src/db/session.py。
//
// 两条硬规则：
//  1. **不使用 GORM AutoMigrate**。schema 的唯一真源是 migrations/00001_init.sql。
//     AutoMigrate 会悄悄改结构，而我们整个设计都建立在触发器与 CHECK 上，
//     被它改掉一处防线就全线失守。
//  2. 计数列由触发器维护，业务代码不得直接写；非内容变更的写入必须用 UpdateColumn
//     （跳过 autoUpdateTime），否则「有人看了一眼」会被记成「内容被编辑」。
package db

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/jingyijun/danshi_backend_go/internal/config"
)

// DB 包装 GORM 连接并提供项目统一的关闭入口。
type DB struct {
	*gorm.DB
}

// Open 按配置建立并校验 PostgreSQL 连接。
func Open(ctx context.Context, cfg config.Config, log *slog.Logger) (*DB, error) {
	gormLog := logger.New(
		slogWriter{log: log},
		logger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  gormLogLevel(cfg),
			IgnoreRecordNotFoundError: true,
			ParameterizedQueries:      true, // 不把参数值写进日志，避免泄露用户输入
		},
	)

	gdb, err := gorm.Open(postgres.Open(cfg.DatabaseURL), &gorm.Config{
		Logger: gormLog,
		// schema 由 goose 管理，GORM 只做读写
		DisableAutomaticPing:                     false,
		DisableForeignKeyConstraintWhenMigrating: true,
		SkipDefaultTransaction:                   true, // 事务边界由 UoW 中间件统一控制
	})
	if err != nil {
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}

	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(cfg.DBMaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.DBMaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.DBConnMaxLifetime())

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		return nil, fmt.Errorf("数据库 ping 失败: %w", err)
	}
	return &DB{DB: gdb}, nil
}

// Close 关闭底层数据库连接池。
func (d *DB) Close() error {
	sqlDB, err := d.DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func gormLogLevel(cfg config.Config) logger.LogLevel {
	if cfg.IsProd() {
		return logger.Warn
	}
	return logger.Info
}

type slogWriter struct{ log *slog.Logger }

func (w slogWriter) Printf(format string, args ...any) {
	w.log.Info(fmt.Sprintf(format, args...), slog.String("component", "gorm"))
}
