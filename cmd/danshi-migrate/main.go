// Command danshi-migrate 是独立的迁移执行器（镜像 2）。
//
// 与 server 拆成两个镜像（D11）：迁移是一次性任务，跑完就退出；
// 服务是常驻进程。混在一起会导致滚动更新时每个副本都想跑一次迁移。
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Foodan-Dev/danshi-backend/internal/config"
	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
)

// version 由构建时 -ldflags "-X main.version=..." 注入；
// 本地 go build 不注入时保持 dev。
var version = "dev"

func main() {
	cmd := flag.String("cmd", "up", "up | down | status | version")
	flag.Parse()

	if err := run(*cmd); err != nil {
		fmt.Fprintf(os.Stderr, "danshi-migrate 失败: %v\n", err)
		os.Exit(1)
	}
}

func run(cmd string) (runErr error) {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	sqlDB, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("打开数据库失败: %w", err)
	}
	defer func() {
		runErr = errors.Join(runErr, sqlDB.Close())
	}()

	if err := sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("数据库不可达: %w", err)
	}

	switch cmd {
	case "up":
		if err := db.Up(ctx, sqlDB); err != nil {
			return err
		}
		v, _ := db.Version(ctx, sqlDB)
		fmt.Printf("迁移完成，当前版本 %d\n", v)
		return nil
	case "down":
		// 回滚只允许一步。整库回滚太危险，需要就多跑几次并逐次确认。
		if err := db.DownOne(ctx, sqlDB); err != nil {
			return err
		}
		v, _ := db.Version(ctx, sqlDB)
		fmt.Printf("已回滚一个版本，当前版本 %d\n", v)
		return nil
	case "status":
		return db.Status(ctx, sqlDB)
	case "version":
		v, err := db.Version(ctx, sqlDB)
		if err != nil {
			return err
		}
		fmt.Printf("当前 %d，期望 %d（构建版本 %s）\n", v, db.ExpectedVersion, version)
		if v != db.ExpectedVersion {
			return errors.New("版本不符")
		}
		return nil
	default:
		return fmt.Errorf("未知命令 %q，可用：up | down | status | version", cmd)
	}
}
