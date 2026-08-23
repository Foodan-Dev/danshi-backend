// Package config 加载并校验配置，对应 Python 侧的 src/config/settings.py。
//
// 用 viper，但刻意收着用（docs/go-rewrite-plan.md §3.1）：
//   - 逐项 BindEnv，**不用 AutomaticEnv**。显式绑定才能保证变量名不被
//     「下划线 ↔ 点号」的转换规则悄悄改掉，也让配置项清单在代码里一目了然。
//   - Unmarshal 到 Config 之后**不再读全局 viper**。全局单例读取点扩散
//     是 viper 最容易被滥用的地方，业务代码只接受注入的 Config 值。
//
// 环境变量名沿用 Python 侧，唯一例外是 §4.9 删除的 IMG_HOSTS。
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Profile 是应用运行环境。
type Profile string

// 应用运行环境枚举值。
const (
	ProfileDev  Profile = "dev"
	ProfileProd Profile = "prod"
)

// Config 是启动后注入各层的完整应用配置。
type Config struct {
	Profile Profile `mapstructure:"APP_PROFILE"`
	Port    int     `mapstructure:"PORT"`

	DatabaseURL    string `mapstructure:"DATABASE_URL"`
	DBMaxOpenConns int    `mapstructure:"DB_MAX_OPEN_CONNS"`
	DBMaxIdleConns int    `mapstructure:"DB_MAX_IDLE_CONNS"`
	DBConnMaxLifeS int    `mapstructure:"DB_CONN_MAX_LIFETIME_SECONDS"`

	JWTSecretKey        string `mapstructure:"JWT_SECRET_KEY"`
	JWTExpireMinutes    int    `mapstructure:"JWT_EXPIRE_MINUTES"`
	JWTRefreshExpireDay int    `mapstructure:"JWT_REFRESH_EXPIRE_DAYS"`

	EmailVerificationRequired bool   `mapstructure:"EMAIL_VERIFICATION_REQUIRED"`
	EmailVerificationSecret   string `mapstructure:"EMAIL_VERIFICATION_SECRET"`
	AllowedEmailDomains       string `mapstructure:"ALLOWED_EMAIL_DOMAINS"`

	CORSAllowOrigins     string `mapstructure:"CORS_ALLOW_ORIGINS"`
	CORSAllowCredentials bool   `mapstructure:"CORS_ALLOW_CREDENTIALS"`

	TencentSecretID      string `mapstructure:"TENCENT_CLOUD_SECRET_ID"`
	TencentSecretKey     string `mapstructure:"TENCENT_CLOUD_SECRET_KEY"`
	TencentRegion        string `mapstructure:"TENCENT_CLOUD_REGION"`
	TencentSESFromEmail  string `mapstructure:"TENCENT_SES_FROM_EMAIL"`
	TencentSESFromName   string `mapstructure:"TENCENT_SES_FROM_NAME"`
	TencentSESTemplateID uint64 `mapstructure:"TENCENT_SES_TEMPLATE_ID"`
	COSBucket            string `mapstructure:"COS_BUCKET"`
	COSRegion            string `mapstructure:"COS_REGION"`
	COSImageDomain       string `mapstructure:"COS_IMG_DOMAIN"`
	COSMaxImageBytes     int64  `mapstructure:"COS_MAX_IMAGE_BYTES"`
	COSPresignTTLS       int    `mapstructure:"COS_PRESIGN_TTL_SECONDS"`
	COSPresignGetTTLS    int    `mapstructure:"COS_PRESIGN_GET_TTL_SECONDS"`

	TencentCIBizType                       string `mapstructure:"TENCENT_CI_BIZ_TYPE"`
	TencentCICallbackURL                   string `mapstructure:"TENCENT_CI_CALLBACK_URL"`
	ModerationCallbackToken                string `mapstructure:"MODERATION_CALLBACK_TOKEN"`
	FeishuModerationWebhook                string `mapstructure:"FEISHU_MODERATION_WEBHOOK_URL"`
	ModerationCallbackAuthFailureThreshold int    `mapstructure:"MODERATION_CALLBACK_AUTH_FAILURE_THRESHOLD"`
	ModerationCallbackAuthFailureWindowS   int    `mapstructure:"MODERATION_CALLBACK_AUTH_FAILURE_WINDOW_SECONDS"`
	ModerationReviewBacklogThreshold       int    `mapstructure:"MODERATION_REVIEW_BACKLOG_THRESHOLD"`
	ModerationReviewBacklogCooldownS       int    `mapstructure:"MODERATION_REVIEW_BACKLOG_COOLDOWN_SECONDS"`

	OTLPEndpoint string `mapstructure:"OTLP_ENDPOINT"`
	LogLevel     string `mapstructure:"LOG_LEVEL"`
}

// IsProd 报告当前是否为生产环境。
func (c Config) IsProd() bool { return c.Profile == ProfileProd }

// AccessTokenTTL 返回访问令牌有效期。
func (c Config) AccessTokenTTL() time.Duration {
	return time.Duration(c.JWTExpireMinutes) * time.Minute
}

// RefreshTokenTTL 返回刷新令牌有效期。
func (c Config) RefreshTokenTTL() time.Duration {
	return time.Duration(c.JWTRefreshExpireDay) * 24 * time.Hour
}

// DBConnMaxLifetime 返回数据库连接最长复用时间。
func (c Config) DBConnMaxLifetime() time.Duration {
	return time.Duration(c.DBConnMaxLifeS) * time.Second
}

// COSPresignTTL 返回直传凭证有效期。
func (c Config) COSPresignTTL() time.Duration {
	return time.Duration(c.COSPresignTTLS) * time.Second
}

// COSPresignGetTTL 返回私有图片读取凭证有效期。
func (c Config) COSPresignGetTTL() time.Duration {
	return time.Duration(c.COSPresignGetTTLS) * time.Second
}

// ModerationCallbackAuthFailureWindow 返回错误回调令牌的聚合窗口。
func (c Config) ModerationCallbackAuthFailureWindow() time.Duration {
	return time.Duration(c.ModerationCallbackAuthFailureWindowS) * time.Second
}

// ModerationReviewBacklogCooldown 返回待复核积压告警的最短重复间隔。
func (c Config) ModerationReviewBacklogCooldown() time.Duration {
	return time.Duration(c.ModerationReviewBacklogCooldownS) * time.Second
}

// COSConfigured 报告对象存储是否具备完整的运行配置。
func (c Config) COSConfigured() bool {
	return c.TencentSecretID != "" && c.TencentSecretKey != "" &&
		c.COSBucket != "" && c.COSRegion != "" && c.COSImageDomain != ""
}

// TencentCIConfigured 报告腾讯 CI 文本与图片审核是否可安全启用。
func (c Config) TencentCIConfigured() bool {
	return c.COSConfigured() && c.TencentCICallbackURL != "" && c.ModerationCallbackToken != ""
}

// TencentSESConfigured 报告注册验证码是否具备完整的腾讯云 SES 配置。
func (c Config) TencentSESConfigured() bool {
	return c.TencentSecretID != "" && c.TencentSecretKey != "" &&
		c.TencentRegion != "" && c.TencentSESFromEmail != "" &&
		c.TencentSESFromName != "" && c.TencentSESTemplateID > 0
}

// EmailDomains 把逗号分隔的白名单拆开并小写化。
func (c Config) EmailDomains() []string {
	return splitCSV(strings.ToLower(c.AllowedEmailDomains))
}

// CORSOrigins 返回规范化后的跨域来源列表。
func (c Config) CORSOrigins() []string { return splitCSV(c.CORSAllowOrigins) }

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// bindings 是配置项的**唯一清单**。加配置项就往这里加，别的地方不许读环境变量。
var bindings = map[string]any{
	"APP_PROFILE": string(ProfileDev),
	"PORT":        8000,

	"DATABASE_URL":                 "",
	"DB_MAX_OPEN_CONNS":            20,
	"DB_MAX_IDLE_CONNS":            10,
	"DB_CONN_MAX_LIFETIME_SECONDS": 1800,

	"JWT_SECRET_KEY":          "",
	"JWT_EXPIRE_MINUTES":      60,
	"JWT_REFRESH_EXPIRE_DAYS": 30,

	"EMAIL_VERIFICATION_REQUIRED": true,
	"EMAIL_VERIFICATION_SECRET":   "",
	"ALLOWED_EMAIL_DOMAINS":       "example.edu",

	"CORS_ALLOW_ORIGINS":     "",
	"CORS_ALLOW_CREDENTIALS": false,

	"TENCENT_CLOUD_SECRET_ID":     "",
	"TENCENT_CLOUD_SECRET_KEY":    "",
	"TENCENT_CLOUD_REGION":        "ap-guangzhou",
	"TENCENT_SES_FROM_EMAIL":      "",
	"TENCENT_SES_FROM_NAME":       "旦食",
	"TENCENT_SES_TEMPLATE_ID":     uint64(0),
	"COS_BUCKET":                  "",
	"COS_REGION":                  "ap-shanghai",
	"COS_IMG_DOMAIN":              "",
	"COS_MAX_IMAGE_BYTES":         int64(10 * 1024 * 1024),
	"COS_PRESIGN_TTL_SECONDS":     600,
	"COS_PRESIGN_GET_TTL_SECONDS": 3600,

	"TENCENT_CI_BIZ_TYPE":                             "",
	"TENCENT_CI_CALLBACK_URL":                         "",
	"MODERATION_CALLBACK_TOKEN":                       "",
	"FEISHU_MODERATION_WEBHOOK_URL":                   "",
	"MODERATION_CALLBACK_AUTH_FAILURE_THRESHOLD":      5,
	"MODERATION_CALLBACK_AUTH_FAILURE_WINDOW_SECONDS": 60,
	"MODERATION_REVIEW_BACKLOG_THRESHOLD":             100,
	"MODERATION_REVIEW_BACKLOG_COOLDOWN_SECONDS":      3600,

	"OTLP_ENDPOINT": "",
	"LOG_LEVEL":     "info",
}

// Load 读取配置并做全部启动期校验。任何一项不合法都返回错误，由 main 打印后退出。
func Load() (Config, error) {
	v := viper.New()
	for key, def := range bindings {
		v.SetDefault(key, def)
		if err := v.BindEnv(key); err != nil {
			return Config{}, fmt.Errorf("绑定环境变量 %s 失败: %w", key, err)
		}
	}
	var c Config
	if err := v.Unmarshal(&c); err != nil {
		return Config{}, fmt.Errorf("解析配置失败: %w", err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}
