// Command danshi-jobs 提供由 cron 或 CronJob 触发的一次性后台任务入口（镜像 3）。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Foodan-Dev/danshi-backend/internal/config"
	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/infra/tencentcloud"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/obs"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

// version 由构建时 -ldflags "-X main.version=..." 注入；
// 本地 go build 不注入时保持 dev。
var version = "dev"

const expirePendingCommand = "expire-pending"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "danshi-jobs 失败: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] != expirePendingCommand {
		return fmt.Errorf("必须指定任务 %q", expirePendingCommand)
	}
	flags := flag.NewFlagSet(expirePendingCommand, flag.ContinueOnError)
	olderThan := flags.Duration("older-than", 0, "必填：只回收创建时间早于该时长的无引用上传")
	batchSize := flags.Int("batch-size", 100, "单次领取的最大资产数")
	dryRun := flags.Bool("dry-run", false, "只统计本批候选，不删除对象或修改数据库")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("无法识别的额外参数：%v", flags.Args())
	}
	if *olderThan <= 0 {
		return errors.New("-older-than 必须显式指定为正时长，例如 24h")
	}
	if *batchSize <= 0 {
		return errors.New("-batch-size 必须为正数")
	}
	return expirePending(*olderThan, *batchSize, *dryRun)
}

func expirePending(olderThan time.Duration, batchSize int, dryRun bool) (runErr error) {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := obs.NewServiceLogger(cfg, "danshi-jobs")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	database, err := db.Open(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer func() {
		runErr = errors.Join(runErr, database.Close())
	}()
	if err := assertSchemaVersion(ctx, database); err != nil {
		return err
	}

	storage, err := expirationStorage(cfg, dryRun)
	if err != nil {
		return err
	}
	uploads := service.NewUploadService(
		storage, nil, nil, cfg.COSMaxImageBytes, cfg.COSPresignTTL(), cfg.COSPresignGetTTL(),
	)
	before := time.Now().UTC().Add(-olderThan)
	result := service.UploadExpirationResult{}
	startedAt := time.Now()
	err = database.RunInTx(ctx, func(txCtx context.Context) error {
		var expireErr error
		result, expireErr = uploads.ExpirePending(txCtx, service.ExpirePendingOptions{
			Before: before, Limit: batchSize, DryRun: dryRun,
		})
		return expireErr
	})
	if err != nil {
		return fmt.Errorf("执行图片过期回收批次: %w", err)
	}
	logExpirationResult(ctx, log, before, dryRun, result, time.Since(startedAt))
	if len(result.Failures) > 0 {
		return fmt.Errorf("%d 个对象删除失败；成功项已提交，失败项保留 pending 供下次重试",
			len(result.Failures))
	}
	return nil
}

func assertSchemaVersion(ctx context.Context, database *db.DB) error {
	sqlDB, err := database.DB.DB()
	if err != nil {
		return err
	}
	return db.AssertVersion(ctx, sqlDB)
}

func expirationStorage(cfg config.Config, dryRun bool) (service.ImageStorage, error) {
	if dryRun {
		return service.UnavailableImageStorage{}, nil
	}
	if !cfg.COSConfigured() {
		return nil, errors.New("执行回收必须完整配置腾讯云 COS；可先用 -dry-run 只查看候选")
	}
	provider, err := tencentcloud.NewProvider(cfg, nil)
	if err != nil {
		return nil, fmt.Errorf("初始化腾讯云 COS: %w", err)
	}
	return provider, nil
}

func logExpirationResult(
	ctx context.Context,
	log *slog.Logger,
	before time.Time,
	dryRun bool,
	result service.UploadExpirationResult,
	duration time.Duration,
) {
	for _, failure := range result.Failures {
		log.ErrorContext(ctx, "删除过期图片对象失败",
			slog.Uint64("image_asset_id", failure.ImageAssetID),
			slog.String("object_key", failure.ObjectKey),
			slog.Any("err", failure.Err),
		)
	}
	log.InfoContext(ctx, "图片过期回收批次完成",
		slog.String("build", version),
		slog.Time("created_before", before),
		slog.Bool("dry_run", dryRun),
		slog.Int("selected", result.Selected),
		slog.Int("retired", result.Retired),
		slog.Int("failed", len(result.Failures)),
		slog.Duration("duration", duration),
	)
}
