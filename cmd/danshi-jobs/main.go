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
	"github.com/Foodan-Dev/danshi-backend/internal/infra/alerting"
	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/infra/tencentcloud"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/obs"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

// version 由构建时 -ldflags "-X main.version=..." 注入；
// 本地 go build 不注入时保持 dev。
var version = "dev"

const (
	expirePendingCommand             = "expire-pending"
	checkModerationBacklogCommand    = "check-moderation-backlog"
	reconcileImageAccessCommand      = "reconcile-image-access"
	deliverVerificationEmailsCommand = "deliver-verification-emails"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "danshi-jobs 失败: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("必须指定任务 %q、%q、%q 或 %q",
			expirePendingCommand, checkModerationBacklogCommand, reconcileImageAccessCommand,
			deliverVerificationEmailsCommand)
	}
	switch args[0] {
	case expirePendingCommand:
		return runExpirePending(args[1:])
	case checkModerationBacklogCommand:
		if len(args) != 1 {
			return fmt.Errorf("任务 %q 不接受额外参数", checkModerationBacklogCommand)
		}
		return checkModerationBacklog()
	case reconcileImageAccessCommand:
		return runReconcileImageAccess(args[1:])
	case deliverVerificationEmailsCommand:
		return runDeliverVerificationEmails(args[1:])
	default:
		return fmt.Errorf("未知任务 %q；可用任务为 %q、%q、%q、%q",
			args[0], expirePendingCommand, checkModerationBacklogCommand,
			reconcileImageAccessCommand, deliverVerificationEmailsCommand)
	}
}

func runDeliverVerificationEmails(args []string) error {
	flags := flag.NewFlagSet(deliverVerificationEmailsCommand, flag.ContinueOnError)
	batchSize := flags.Int("batch-size", 4, "单次领取数；范围 1..100")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("无法识别的额外参数：%v", flags.Args())
	}
	if *batchSize < 1 || *batchSize > 100 {
		return errors.New("-batch-size 必须在 1..100 之间")
	}
	return deliverVerificationEmails(*batchSize)
}

func deliverVerificationEmails(batchSize int) (runErr error) {
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
	defer func() { runErr = errors.Join(runErr, database.Close()) }()
	if err := assertSchemaVersion(ctx, database); err != nil {
		return err
	}
	var sender service.VerificationEmailSender
	switch {
	case cfg.TencentSESConfigured():
		sender, err = tencentcloud.NewSESVerificationEmailSender(cfg)
		if err != nil {
			return err
		}
	case cfg.IsProd():
		return errors.New("生产环境验证码邮件投递未配置 SES")
	default:
		sender = service.NewLogVerificationEmailSender(log)
	}
	worker := service.NewVerificationEmailDeliveryWorker(
		database, sender, cfg.EmailVerificationSecret,
		service.VerificationEmailDeliveryWorkerOptions{BatchSize: batchSize, Log: log},
	)
	startedAt := time.Now()
	result, err := worker.RunBatch(ctx)
	if err != nil {
		return fmt.Errorf("投递验证码邮件: %w", err)
	}
	log.InfoContext(ctx, "验证码邮件 outbox 批次完成",
		slog.String("build", version), slog.Int("claimed", result.Claimed),
		slog.Int("sent", result.Sent), slog.Int("canceled", result.Canceled),
		slog.Int("rescheduled", result.Rescheduled), slog.Int("dead_lettered", result.DeadLettered),
		slog.Duration("duration", time.Since(startedAt)),
	)
	return nil
}

func runReconcileImageAccess(args []string) error {
	flags := flag.NewFlagSet(reconcileImageAccessCommand, flag.ContinueOnError)
	batchSize := flags.Int("batch-size", 4, "单次 SKIP LOCKED 领取并发数；范围 1..4")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("无法识别的额外参数：%v", flags.Args())
	}
	if *batchSize < 1 || *batchSize > 4 {
		return errors.New("-batch-size 必须在 1..4 之间")
	}
	return reconcileImageAccess(*batchSize)
}

func reconcileImageAccess(batchSize int) (runErr error) {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.COSConfigured() || !cfg.EdgeOneConfigured() {
		return errors.New("图片访问状态 worker 必须完整配置 COS 与 EdgeOne")
	}
	log := obs.NewServiceLogger(cfg, "danshi-jobs")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	database, err := db.Open(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, database.Close()) }()
	if err := assertSchemaVersion(ctx, database); err != nil {
		return err
	}
	storage, err := tencentcloud.NewProvider(cfg, nil)
	if err != nil {
		return fmt.Errorf("初始化腾讯云 COS: %w", err)
	}
	purger, err := tencentcloud.NewEdgeOnePurger(cfg)
	if err != nil {
		return fmt.Errorf("初始化腾讯云 EdgeOne: %w", err)
	}
	worker := service.NewImageAccessWorker(database, storage, purger, service.ImageAccessWorkerOptions{
		BatchSize: batchSize,
	})
	startedAt := time.Now()
	result, err := worker.RunBatch(ctx)
	if err != nil {
		return fmt.Errorf("收敛审核图片访问状态: %w", err)
	}
	log.InfoContext(ctx, "审核图片访问状态批次完成",
		slog.String("build", version),
		slog.Int("claimed", result.Claimed),
		slog.Int("succeeded", result.Succeeded),
		slog.Int("rescheduled", result.Rescheduled),
		slog.Int("dead_lettered", result.DeadLettered),
		slog.Int("superseded", result.Superseded),
		slog.Duration("duration", time.Since(startedAt)),
	)
	return nil
}

func runExpirePending(args []string) error {
	flags := flag.NewFlagSet(expirePendingCommand, flag.ContinueOnError)
	olderThan := flags.Duration("older-than", 0, "必填：只回收创建时间早于该时长的无引用上传")
	batchSize := flags.Int("batch-size", 100, "单次领取的最大资产数")
	dryRun := flags.Bool("dry-run", false, "只统计本批候选，不删除对象或修改数据库")
	if err := flags.Parse(args); err != nil {
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

func checkModerationBacklog() (runErr error) {
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

	var channel service.ModerationAlerter
	if cfg.FeishuModerationWebhook != "" {
		channel = alerting.NewFeishuWebhook(cfg.FeishuModerationWebhook, nil, log)
	} else {
		channel = service.NewLogModerationAlerter(log)
	}
	alerter := alerting.AfterCommit(channel)
	monitor := service.NewModerationAlertService(alerter)
	callbackCtx, afterCommit := db.WithAfterCommitQueue(ctx)
	result := service.ReviewBacklogCheckResult{}
	startedAt := time.Now()
	err = database.RunInTx(callbackCtx, func(txCtx context.Context) error {
		var checkErr error
		result, checkErr = monitor.CheckReviewBacklog(txCtx, service.ReviewBacklogCheckOptions{
			Threshold: cfg.ModerationReviewBacklogThreshold,
			Cooldown:  cfg.ModerationReviewBacklogCooldown(),
			Now:       time.Now().UTC(),
		})
		return checkErr
	})
	if err != nil {
		return fmt.Errorf("检查审核复核积压: %w", err)
	}
	for _, recovered := range afterCommit.Run(context.WithoutCancel(ctx)) {
		log.ErrorContext(ctx, "审核积压告警提交后回调发生 panic", slog.Any("panic", recovered))
	}
	log.InfoContext(ctx, "审核复核积压检查完成",
		slog.String("build", version),
		slog.Int64("queue_depth", result.QueueDepth),
		slog.Int64("threshold", result.Threshold),
		slog.Bool("alert_scheduled", result.AlertScheduled),
		slog.Duration("duration", time.Since(startedAt)),
	)
	return nil
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
