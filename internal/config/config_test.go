package config_test

import (
	"strings"
	"testing"

	"github.com/Foodan-Dev/danshi-backend/internal/config"
)

const goodSecret = "a-sufficiently-long-secret-value-1234567890"

func base() config.Config {
	return config.Config{
		Profile: config.ProfileDev, Port: 8000,
		DatabaseURL:    "postgres://u:p@localhost:5432/danshi?sslmode=disable&TimeZone=UTC",
		DBMaxOpenConns: 20, DBMaxIdleConns: 10, DBConnMaxLifeS: 1800,
		JWTSecretKey: goodSecret, JWTExpireMinutes: 60, JWTRefreshExpireDay: 30,
		EmailVerificationRequired: true, EmailVerificationSecret: goodSecret,
		AllowedEmailDomains: "fdueat.com,m.fudan.edu.cn",
		COSMaxImageBytes:    10 * 1024 * 1024, COSPresignTTLS: 600, COSPresignGetTTLS: 3600,
		LogLevel: "info",
	}
}

func TestBaselineIsValid(t *testing.T) {
	if err := base().Validate(); err != nil {
		t.Fatalf("基准配置应通过: %v", err)
	}
}

func TestRejectsBadConfig(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*config.Config)
		expect string
	}{
		{"空 DSN", func(c *config.Config) { c.DatabaseURL = "" }, "DATABASE_URL"},
		{"DSN 缺 TimeZone", func(c *config.Config) {
			c.DatabaseURL = "postgres://u:p@h:5432/db?sslmode=disable"
		}, "TimeZone=UTC"},
		{"DSN 缺库名", func(c *config.Config) {
			c.DatabaseURL = "postgres://u:p@h:5432/?TimeZone=UTC"
		}, "数据库名"},
		{"密钥太短", func(c *config.Config) { c.JWTSecretKey = "short" }, "长度不足"},
		{"密钥是占位值", func(c *config.Config) {
			c.JWTSecretKey = strings.Repeat("x", 0) + "changeme"
		}, "JWT_SECRET_KEY"},
		{"空邮箱白名单", func(c *config.Config) { c.AllowedEmailDomains = "" }, "ALLOWED_EMAIL_DOMAINS"},
		{"白名单带 @", func(c *config.Config) { c.AllowedEmailDomains = "@fdueat.com" }, "格式不正确"},
		{"空闲连接超过最大连接", func(c *config.Config) { c.DBMaxIdleConns = 99 }, "DB_MAX_IDLE_CONNS"},
		{"access 有效期超过 refresh", func(c *config.Config) {
			c.JWTExpireMinutes = 60 * 24 * 40
		}, "access token"},
		{"通配来源 + 携带凭据", func(c *config.Config) {
			c.CORSAllowCredentials = true
			c.CORSAllowOrigins = "*"
		}, "不能包含 *"},
		{"开了验证码却没配密钥", func(c *config.Config) { c.EmailVerificationSecret = "" }, "EMAIL_VERIFICATION_SECRET"},
		{"图片上限非正", func(c *config.Config) { c.COSMaxImageBytes = 0 }, "COS_MAX_IMAGE_BYTES"},
		{"签名时效非正", func(c *config.Config) { c.COSPresignTTLS = 0 }, "COS_PRESIGN_TTL_SECONDS"},
		{"读取签名时效非正", func(c *config.Config) { c.COSPresignGetTTLS = 0 }, "COS_PRESIGN_GET_TTL_SECONDS"},
		{"图片域名不是 HTTPS", func(c *config.Config) { c.COSImageDomain = "http://img.example.com" }, "COS_IMG_DOMAIN"},
		{"回调令牌太短", func(c *config.Config) { c.ModerationCallbackToken = "short" }, "MODERATION_CALLBACK_TOKEN"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := base()
			c.mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("应当校验失败")
			}
			if !strings.Contains(err.Error(), c.expect) {
				t.Fatalf("错误信息应包含 %q，实际:\n%v", c.expect, err)
			}
		})
	}
}

// 生产 profile 的额外硬约束——这些在 dev 下允许，在 prod 下必须拦住。
func TestProdExtraConstraints(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*config.Config)
		expect string
	}{
		{"关闭验证码", func(c *config.Config) { c.EmailVerificationRequired = false }, "EMAIL_VERIFICATION_REQUIRED"},
		{"未配 CORS", func(c *config.Config) { c.CORSAllowOrigins = "" }, "CORS_ALLOW_ORIGINS"},
		{"CORS 通配", func(c *config.Config) { c.CORSAllowOrigins = "*" }, "不能是 *"},
		{"未配腾讯云密钥", func(c *config.Config) { c.TencentSecretID = "" }, "TENCENT_CLOUD_SECRET_ID"},
		{"未配 SES 区域", func(c *config.Config) { c.TencentRegion = "" }, "TENCENT_CLOUD_REGION"},
		{"SES 发件邮箱非法", func(c *config.Config) {
			c.TencentSESFromEmail = "旦食 <sender@example.com>"
		}, "TENCENT_SES_FROM_EMAIL"},
		{"SES 发件人名非法", func(c *config.Config) {
			c.TencentSESFromName = "旦食:验证码"
		}, "TENCENT_SES_FROM_NAME"},
		{"未配 SES 模板", func(c *config.Config) {
			c.TencentSESTemplateID = 0
		}, "TENCENT_SES_TEMPLATE_ID"},
		{"未配图片域名", func(c *config.Config) { c.COSImageDomain = "" }, "COS_IMG_DOMAIN"},
		{"未配审核回调", func(c *config.Config) { c.TencentCICallbackURL = "" }, "TENCENT_CI_CALLBACK_URL"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := base()
			cfg.Profile = config.ProfileProd
			cfg.CORSAllowOrigins = "https://danshi.example.com"
			cfg.TencentSecretID, cfg.TencentSecretKey = "id", "key"
			cfg.TencentRegion = "ap-guangzhou"
			cfg.TencentSESFromEmail = "sender@example.com"
			cfg.TencentSESFromName = "旦食"
			cfg.TencentSESTemplateID = 123
			cfg.COSBucket, cfg.COSRegion = "b", "r"
			cfg.COSImageDomain = "https://img.example.com"
			cfg.TencentCICallbackURL = "https://api.example.com/api/v2/moderation/tencent-ci/callback"
			cfg.ModerationCallbackToken = goodSecret
			c.mutate(&cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), c.expect) {
				t.Fatalf("期望包含 %q，实际: %v", c.expect, err)
			}
		})
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@h:5432/danshi?TimeZone=UTC")
	t.Setenv("JWT_SECRET_KEY", goodSecret)
	t.Setenv("EMAIL_VERIFICATION_SECRET", goodSecret)
	t.Setenv("TENCENT_CLOUD_SECRET_ID", "env-secret-id")
	t.Setenv("TENCENT_CLOUD_SECRET_KEY", "env-secret-key")
	t.Setenv("TENCENT_CLOUD_REGION", "ap-hongkong")
	t.Setenv("TENCENT_SES_FROM_EMAIL", "sender@example.com")
	t.Setenv("TENCENT_SES_FROM_NAME", "测试发件人")
	t.Setenv("TENCENT_SES_TEMPLATE_ID", "456")
	t.Setenv("PORT", "9001")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 9001 {
		t.Fatalf("PORT 未生效: %d", cfg.Port)
	}
	// 默认值应当来自 bindings，而不是零值。具体域名是部署方配置，
	// 这里只断言默认值能被解析成非空列表，不钉住任何一所学校的域名。
	if len(cfg.EmailDomains()) == 0 {
		t.Fatalf("默认邮箱白名单不应为空，实际 %v", cfg.EmailDomains())
	}
	if cfg.TencentSecretID != "env-secret-id" || cfg.TencentSecretKey != "env-secret-key" ||
		cfg.TencentRegion != "ap-hongkong" || cfg.TencentSESFromEmail != "sender@example.com" ||
		cfg.TencentSESFromName != "测试发件人" || cfg.TencentSESTemplateID != 456 {
		t.Fatalf("腾讯云 SES 环境变量未完整加载: %+v", cfg)
	}
}
