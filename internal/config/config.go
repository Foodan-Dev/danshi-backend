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

	TencentSecretID  string `mapstructure:"TENCENT_CLOUD_SECRET_ID"`
	TencentSecretKey string `mapstructure:"TENCENT_CLOUD_SECRET_KEY"`
	COSBucket        string `mapstructure:"COS_BUCKET"`
	COSRegion        string `mapstructure:"COS_REGION"`

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
	"ALLOWED_EMAIL_DOMAINS":       "fdueat.com,m.fudan.edu.cn,fudan.edu.cn",

	"CORS_ALLOW_ORIGINS":     "",
	"CORS_ALLOW_CREDENTIALS": false,

	"TENCENT_CLOUD_SECRET_ID":  "",
	"TENCENT_CLOUD_SECRET_KEY": "",
	"COS_BUCKET":               "",
	"COS_REGION":               "",

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
