// Command danshi-server 是 HTTP 服务（镜像 1）。
//
// 启动顺序刻意如此，每一步都是一道闸：
//  1. 加载并校验配置——错配置绝不带上线，宁可起不来
//  2. 连数据库
//  3. **核对 schema 版本**——迁移没跑完就拒绝启动，
//     否则会连上旧 schema，报出「字段不存在」这类难查的运行时错误
//  4. 装配路由并监听
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"

	"github.com/jingyijun/danshi_backend_go/internal/config"
	"github.com/jingyijun/danshi_backend_go/internal/infra/db"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/obs"
	"github.com/jingyijun/danshi_backend_go/internal/router"

	_ "time/tzdata" // 内置时区库：distroless 镜像里没有 /usr/share/zoneinfo
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "danshi-server 启动失败: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := obs.NewLogger(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	tracing, err := obs.NewTracing(ctx, cfg.OTLPEndpoint)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tracing.Shutdown(shutdownCtx); err != nil {
			log.Error("关闭 tracing 失败", slog.Any("err", err))
		}
	}()

	database, err := db.Open(ctx, cfg, log, db.WithTracerProvider(tracing.TracerProvider()))
	if err != nil {
		return err
	}
	defer func() {
		if err := database.Close(); err != nil {
			log.Error("关闭数据库失败", slog.Any("err", err))
		}
	}()

	sqlDB, err := database.DB.DB()
	if err != nil {
		return err
	}
	if err := db.AssertVersion(ctx, sqlDB); err != nil {
		return err
	}
	log.Info("schema 版本核对通过", slog.Int64("version", db.ExpectedVersion))
	metrics, err := obs.NewMetrics(sqlDB)
	if err != nil {
		return err
	}

	h := server.New(
		server.WithHostPorts(fmt.Sprintf(":%d", cfg.Port)),
		server.WithExitWaitTime(10*time.Second),
		server.WithReadTimeout(30*time.Second),
		server.WithDisablePrintRoute(cfg.IsProd()),
		// 默认关闭时 405 会退化成 404，前端分不清「路径不存在」和「方法不对」
		server.WithHandleMethodNotAllowed(true),
	)
	h.Use(metrics.Middleware())
	if tracing.Enabled() {
		h.Use(tracing.Middleware())
	}
	metrics.Register(h)
	router.Register(h, router.Deps{Config: cfg, DB: database, Log: log})

	go func() {
		<-ctx.Done()
		log.Info("收到退出信号，开始优雅关闭")
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if err := h.Shutdown(shutdownCtx); err != nil {
			log.Error("优雅关闭失败", slog.Any("err", err))
		}
	}()

	log.Info("服务启动", slog.Int("port", cfg.Port), slog.String("prefix", router.APIPrefix))
	h.Spin()
	return nil
}
