// Command danshi-server 是 HTTP 服务（镜像 1）。
//
// 启动顺序刻意如此，每一步都是一道闸：
//  1. 加载并校验配置——错配置绝不带上线，宁可起不来
//  2. 连数据库
//  3. **核对 schema 版本**——迁移没跑完就拒绝启动，
//     否则会连上旧 schema，报出「字段不存在」这类难查的运行时错误
//  4. 生产环境只读探测图片审核凭据与权限
//  5. 装配路由、启动补审 worker 并监听
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"

	"github.com/Foodan-Dev/danshi-backend/internal/config"
	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/infra/tencentcloud"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/obs"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
	"github.com/Foodan-Dev/danshi-backend/internal/router"
	"github.com/Foodan-Dev/danshi-backend/internal/service"

	_ "time/tzdata" // 内置时区库：distroless 镜像里没有 /usr/share/zoneinfo
)

// version 由构建时 -ldflags "-X main.version=..." 注入；
// 本地 go build 不注入时保持 dev。
var version = "dev"

const (
	reviewQueueStatementTimeout = 2500 * time.Millisecond
	imageModerationProbeTimeout = 5 * time.Second
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
	log.Info("schema 版本核对通过", slog.Int64("version", db.ExpectedVersion),
		slog.String("build", version))
	tencentProvider, err := newTencentProvider(ctx, cfg, log)
	if err != nil {
		return err
	}
	metrics, err := newServerMetrics(sqlDB, database)
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
	deps := router.Deps{
		Config: cfg, DB: database, Log: log, BusinessMetrics: metrics,
	}
	if tencentProvider != nil {
		deps.ImageStorage = tencentProvider
		if cfg.TencentCIConfigured() {
			deps.ContentModerator = tencentProvider
			deps.ImageModerator = tencentProvider
		}
	}
	router.Register(h, deps)

	retryWorkerDone := startImageModerationRetryWorker(
		ctx, cfg, database, tencentProvider, log,
	)

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
	stop()
	if retryWorkerDone != nil {
		<-retryWorkerDone
	}
	return nil
}

func newTencentProvider(
	ctx context.Context,
	cfg config.Config,
	log *slog.Logger,
) (*tencentcloud.Provider, error) {
	var provider *tencentcloud.Provider
	var err error
	if cfg.COSConfigured() {
		provider, err = tencentcloud.NewProvider(cfg, nil)
		if err != nil {
			return nil, fmt.Errorf("初始化腾讯云供应商: %w", err)
		}
	}
	if err := assertImageModerationAvailable(ctx, cfg, provider, log); err != nil {
		return nil, err
	}
	return provider, nil
}

func newServerMetrics(
	sqlDB *sql.DB,
	database *db.DB,
) (*obs.Metrics, error) {
	reviewQueue := repository.ModerationAlertRepository{}
	imageAccessOutbox := repository.ImageAccessOutboxRepository{}
	imageModerationRetries := repository.ImageModerationRetryRepository{}
	return obs.NewMetrics(sqlDB, obs.WithReviewQueueCounter(
		func(counterCtx context.Context) (count int64, counterErr error) {
			counterErr = database.RunInReadOnlyTx(
				counterCtx, reviewQueueStatementTimeout, func(txCtx context.Context) error {
					count, counterErr = reviewQueue.CountPendingReviewQueue(txCtx)
					return counterErr
				},
			)
			return count, counterErr
		},
	), obs.WithImageAccessStateCounter(
		func(counterCtx context.Context) (counts map[string]int64, counterErr error) {
			counterErr = database.RunInReadOnlyTx(
				counterCtx, 750*time.Millisecond, func(txCtx context.Context) error {
					counts, counterErr = imageAccessOutbox.CountByState(txCtx)
					return counterErr
				},
			)
			return counts, counterErr
		},
	), obs.WithImageModerationRetryStateCounter(
		func(counterCtx context.Context) (counts map[string]int64, counterErr error) {
			counterErr = database.RunInReadOnlyTx(
				counterCtx, 750*time.Millisecond, func(txCtx context.Context) error {
					counts, counterErr = imageModerationRetries.CountByState(txCtx)
					return counterErr
				},
			)
			return counts, counterErr
		},
	))
}

func startImageModerationRetryWorker(
	ctx context.Context,
	cfg config.Config,
	database *db.DB,
	provider *tencentcloud.Provider,
	log *slog.Logger,
) <-chan struct{} {
	if provider == nil || !cfg.TencentCIConfigured() {
		return nil
	}
	moderation := service.NewModerationService(
		service.NewLogModerationAlerter(log),
		service.NewDurableImageAccessController(),
	)
	worker := service.NewImageModerationRetryWorker(
		database, provider, moderation, service.ImageModerationRetryWorkerOptions{},
	)
	done := make(chan struct{})
	go runImageModerationRetryLoop(
		ctx, worker, cfg.ImageModerationRetryScanInterval(), log, done,
	)
	return done
}

func assertImageModerationAvailable(
	ctx context.Context,
	cfg config.Config,
	prober service.ImageModerationProber,
	log *slog.Logger,
) error {
	if !cfg.IsProd() {
		return nil
	}
	if prober == nil {
		return fmt.Errorf("生产图片审核启动探测缺少供应商")
	}
	probeCtx, cancel := context.WithTimeout(ctx, imageModerationProbeTimeout)
	err := prober.ProbeImageModeration(probeCtx)
	cancel()
	if err == nil {
		log.InfoContext(ctx, "图片审核服务启动探测通过")
		return nil
	}
	switch service.ClassifyImageModerationProbeError(err) {
	case service.ImageModerationProbeAuthorization,
		service.ImageModerationProbeConfiguration:
		return fmt.Errorf("图片审核服务凭据、权限或开通状态不可用: %w", err)
	default:
		log.WarnContext(ctx, "图片审核服务启动探测遇到暂时性错误，补审队列将兜底",
			slog.Any("err", err))
		return nil
	}
}

type imageModerationRetryBatchWorker interface {
	RunBatch(context.Context) (service.ImageModerationRetryWorkerResult, error)
}

func runImageModerationRetryLoop(
	ctx context.Context,
	worker imageModerationRetryBatchWorker,
	interval time.Duration,
	log *slog.Logger,
	done chan<- struct{},
) {
	defer close(done)
	if interval <= 0 {
		interval = 30 * time.Second
	}
	runBatch := func() {
		startedAt := time.Now()
		result, err := worker.RunBatch(ctx)
		if err != nil {
			if ctx.Err() == nil {
				log.WarnContext(ctx, "图片补审批次失败", slog.Any("err", err))
			}
			return
		}
		if result.Claimed == 0 {
			return
		}
		level := slog.LevelInfo
		message := "图片补审批次完成"
		if result.DeadLettered > 0 {
			level = slog.LevelError
			message = "图片补审进入死信"
		}
		log.LogAttrs(ctx, level, message,
			slog.Int("claimed", result.Claimed),
			slog.Int("submitted", result.Submitted),
			slog.Int("concluded", result.Concluded),
			slog.Int("rescheduled", result.Rescheduled),
			slog.Int("dead_lettered", result.DeadLettered),
			slog.Int("superseded", result.Superseded),
			slog.Duration("duration", time.Since(startedAt)),
		)
	}

	runBatch()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runBatch()
		}
	}
}
