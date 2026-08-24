package testutil

import (
	"io"
	"log/slog"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	hertzconfig "github.com/cloudwego/hertz/pkg/common/config"

	appconfig "github.com/Foodan-Dev/danshi-backend/internal/config"
	"github.com/Foodan-Dev/danshi-backend/internal/router"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

const testingSecret = "danshi-test-secret-longer-than-thirty-two-bytes"

// HarnessOption 覆写统一测试环境的依赖。
type HarnessOption func(*harnessOptions)

type harnessOptions struct {
	config                appconfig.Config
	email                 *MockEmailSender
	moderation            *MockModeration
	storage               *MockImageStorage
	callbackDecoder       service.ImageCallbackDecoder
	moderationAlerter     service.ModerationAlerter
	userModerationAlerter service.UserModerationAlerter
	postgresOptions       []PostgresOption
}

// WithConfig 覆写统一 harness 的应用配置。
func WithConfig(config appconfig.Config) HarnessOption {
	return func(options *harnessOptions) { options.config = config }
}

// WithEmailMock 注入调用方预先编程的验证码 Mock。
func WithEmailMock(email *MockEmailSender) HarnessOption {
	return func(options *harnessOptions) { options.email = email }
}

// WithModerationMock 注入调用方预先编程的审核 Mock。
func WithModerationMock(moderation *MockModeration) HarnessOption {
	return func(options *harnessOptions) { options.moderation = moderation }
}

// WithStorageMock 注入调用方预先编程的对象存储 Mock。
func WithStorageMock(storage *MockImageStorage) HarnessOption {
	return func(options *harnessOptions) { options.storage = storage }
}

// WithImageCallbackDecoder 注入图片回调解码器。
func WithImageCallbackDecoder(decoder service.ImageCallbackDecoder) HarnessOption {
	return func(options *harnessOptions) { options.callbackDecoder = decoder }
}

// WithModerationAlerter 注入审核告警接收端。
func WithModerationAlerter(alerter service.ModerationAlerter) HarnessOption {
	return func(options *harnessOptions) { options.moderationAlerter = alerter }
}

// WithUserModerationAlerter 注入用户字段审核告警接收端。
func WithUserModerationAlerter(alerter service.UserModerationAlerter) HarnessOption {
	return func(options *harnessOptions) { options.userModerationAlerter = alerter }
}

// WithPostgresOptions 透传 PostgreSQL testcontainer 选项。
func WithPostgresOptions(options ...PostgresOption) HarnessOption {
	return func(settings *harnessOptions) {
		settings.postgresOptions = append(settings.postgresOptions, options...)
	}
}

// Harness 是后续域测试使用的数据库、engine、Mock 和夹具集合。
type Harness struct {
	Database   *TestDatabase
	Engine     *server.Hertz
	Config     appconfig.Config
	Email      *MockEmailSender
	Moderation *MockModeration
	Storage    *MockImageStorage
	Fixtures   *Fixtures
}

// DefaultConfig 返回统一且不含外部凭证的测试配置。
func DefaultConfig() appconfig.Config {
	return appconfig.Config{
		Profile:                                appconfig.ProfileDev,
		JWTSecretKey:                           testingSecret,
		JWTExpireMinutes:                       60,
		JWTRefreshExpireDay:                    30,
		EmailVerificationRequired:              true,
		EmailVerificationSecret:                testingSecret,
		AllowedEmailDomains:                    "fdueat.com,m.fudan.edu.cn,fudan.edu.cn",
		COSMaxImageBytes:                       10 * 1024 * 1024,
		COSPresignTTLS:                         600,
		COSPresignGetTTLS:                      3600,
		ModerationCallbackToken:                "testing-callback-token",
		ModerationCallbackAuthFailureThreshold: 5,
		ModerationCallbackAuthFailureWindowS:   60,
		ModerationReviewBacklogThreshold:       100,
		ModerationReviewBacklogCooldownS:       3600,
	}
}

// NewHarness 启动数据库、执行迁移并装配全部路由与默认 Mock。
func NewHarness(t testing.TB, options ...HarnessOption) *Harness {
	t.Helper()
	settings := harnessOptions{config: DefaultConfig()}
	for _, option := range options {
		option(&settings)
	}
	if settings.email == nil {
		settings.email = NewMockEmailSender()
	}
	if settings.moderation == nil {
		settings.moderation = NewMockModeration()
	}
	if settings.storage == nil {
		settings.storage = NewMockImageStorage()
		settings.storage.SetAutoMaterialize(true)
	}
	database := OpenPostgres(t, settings.postgresOptions...)
	engine := NewEngine(t, router.Deps{
		Config:                settings.config,
		DB:                    database.DB,
		EmailSender:           settings.email,
		ContentModerator:      settings.moderation,
		ImageStorage:          settings.storage,
		ImageModerator:        settings.moderation,
		ImageCallbackDecoder:  settings.callbackDecoder,
		ModerationAlerter:     settings.moderationAlerter,
		UserModerationAlerter: settings.userModerationAlerter,
	})
	return &Harness{
		Database:   database,
		Engine:     engine,
		Config:     settings.config,
		Email:      settings.email,
		Moderation: settings.moderation,
		Storage:    settings.storage,
		Fixtures:   NewFixtures(t, database.GORM),
	}
}

// NewEngine 按仓库统一 Hertz 选项装配完整路由。
func NewEngine(t testing.TB, dependencies router.Deps) *server.Hertz {
	t.Helper()
	if dependencies.DB == nil {
		t.Fatal("测试 engine 必须注入数据库")
	}
	if dependencies.Log == nil {
		dependencies.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	engine := server.New(
		server.WithHandleMethodNotAllowed(true),
		hertzconfig.Option{F: func(_ *hertzconfig.Options) {}},
	)
	router.Register(engine, dependencies)
	return engine
}
