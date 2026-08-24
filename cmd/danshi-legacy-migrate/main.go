// Command danshi-legacy-migrate 提供旧 FastAPI 数据库迁入前的两进程 inspect/plan 门禁。
// 当前版本不实现 apply；报告只输出固定枚举、摘要与聚合计数。
package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Foodan-Dev/danshi-backend/internal/legacymigration"
)

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout); err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(legacymigration.ErrorReport(err))
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string, output io.Writer) error {
	command, err := legacymigration.ParseCommand(args)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	report, err := legacymigration.ExecuteFromEnvironment(ctx, getenv, command)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(report)
}
