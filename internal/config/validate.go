package config

import (
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// 弱密钥黑名单。Python 侧同样有这层检查——历史上真的有人把示例值直接带上线。
var placeholderSecrets = map[string]bool{
	"changeme": true, "change-me": true, "secret": true, "your-secret-key": true,
	"test": true, "dev": true, "danshi": true, "please-change-this": true,
}

const minSecretBytes = 32

var edgeOneZoneIDPattern = regexp.MustCompile(`^zone-[a-z0-9]+$`)

// Validate 是启动期防线：宁可起不来，也不要带着错误配置对外服务。
// 所有问题一次性收集完再返回，免得运维改一条重启一次。
func (c Config) Validate() error {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	switch c.Profile {
	case ProfileDev, ProfileProd:
	default:
		add("APP_PROFILE 必须是 dev 或 prod，当前 %q", c.Profile)
	}

	if c.Port <= 0 || c.Port > 65535 {
		add("PORT 必须在 1-65535 之间，当前 %d", c.Port)
	}

	// --- 数据库 ---
	if c.DatabaseURL == "" {
		add("DATABASE_URL 未配置")
	} else if err := validateDSN(c.DatabaseURL); err != nil {
		add("DATABASE_URL %v", err)
	}
	if c.DBMaxOpenConns <= 0 {
		add("DB_MAX_OPEN_CONNS 必须为正数")
	}
	if c.DBMaxIdleConns < 0 || c.DBMaxIdleConns > c.DBMaxOpenConns {
		add("DB_MAX_IDLE_CONNS 必须在 0 与 DB_MAX_OPEN_CONNS(%d) 之间", c.DBMaxOpenConns)
	}

	// --- JWT ---
	if err := validateSecret("JWT_SECRET_KEY", c.JWTSecretKey); err != nil {
		add("%v", err)
	}
	if c.JWTExpireMinutes <= 0 {
		add("JWT_EXPIRE_MINUTES 必须为正数")
	}
	if c.JWTRefreshExpireDay <= 0 {
		add("JWT_REFRESH_EXPIRE_DAYS 必须为正数")
	}
	if c.AccessTokenTTL() >= c.RefreshTokenTTL() {
		add("access token 有效期不应大于等于 refresh token 有效期")
	}

	// --- 邮箱 ---
	if len(c.EmailDomains()) == 0 {
		add("ALLOWED_EMAIL_DOMAINS 不能为空，否则任何人都能注册")
	}
	for _, d := range c.EmailDomains() {
		if strings.HasPrefix(d, "@") || strings.ContainsAny(d, " \t") {
			add("ALLOWED_EMAIL_DOMAINS 的 %q 格式不正确，应为裸域名如 example.edu", d)
		}
	}
	if c.EmailVerificationRequired {
		if err := validateSecret("EMAIL_VERIFICATION_SECRET", c.EmailVerificationSecret); err != nil {
			add("%v", err)
		}
	}
	validateSESSubject(c, add)

	// --- CORS：通配来源 + 携带凭据是明确的安全错误，浏览器也会拒绝 ---
	origins := c.CORSOrigins()
	if c.CORSAllowCredentials && slices.Contains(origins, "*") {
		add("CORS_ALLOW_CREDENTIALS=true 时 CORS_ALLOW_ORIGINS 不能包含 *")
	}

	// --- 上传与审核 ---
	if c.COSMaxImageBytes <= 0 {
		add("COS_MAX_IMAGE_BYTES 必须为正数")
	}
	if c.COSPresignTTLS <= 0 {
		add("COS_PRESIGN_TTL_SECONDS 必须为正数")
	}
	if c.COSPresignGetTTLS <= 0 {
		add("COS_PRESIGN_GET_TTL_SECONDS 必须为正数")
	}
	if c.COSImageDomain != "" {
		if err := validateHTTPSURL("COS_IMG_DOMAIN", c.COSImageDomain); err != nil {
			add("%v", err)
		}
	}
	validateEdgeOne(c, add)
	if c.TencentCICallbackURL != "" {
		if err := validateHTTPSURL("TENCENT_CI_CALLBACK_URL", c.TencentCICallbackURL); err != nil {
			add("%v", err)
		}
	}
	if c.ModerationCallbackToken != "" {
		if err := validateSecret("MODERATION_CALLBACK_TOKEN", c.ModerationCallbackToken); err != nil {
			add("%v", err)
		}
	}
	if c.FeishuModerationWebhook != "" {
		if err := validateHTTPSURL("FEISHU_MODERATION_WEBHOOK_URL", c.FeishuModerationWebhook); err != nil {
			add("%v", err)
		}
	}
	validateModerationAlerting(c, add)

	// --- 生产环境的额外硬约束 ---
	if c.IsProd() {
		validateProd(c, origins, add)
	}

	if len(problems) > 0 {
		return fmt.Errorf("配置校验失败：\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

func validateModerationAlerting(c Config, add func(string, ...any)) {
	if c.ModerationCallbackAuthFailureThreshold < 2 {
		add("MODERATION_CALLBACK_AUTH_FAILURE_THRESHOLD 必须至少为 2")
	}
	if c.ModerationCallbackAuthFailureWindowS <= 0 {
		add("MODERATION_CALLBACK_AUTH_FAILURE_WINDOW_SECONDS 必须为正数")
	}
	if c.ModerationReviewBacklogThreshold <= 0 {
		add("MODERATION_REVIEW_BACKLOG_THRESHOLD 必须为正数")
	}
	if c.ModerationReviewBacklogCooldownS <= 0 {
		add("MODERATION_REVIEW_BACKLOG_COOLDOWN_SECONDS 必须为正数")
	}
	if c.ImageModerationRetryScanIntervalS <= 0 {
		add("IMAGE_MODERATION_RETRY_SCAN_INTERVAL_SECONDS 必须为正数")
	}
	if c.ImagePendingExpirationScanIntervalS <= 0 {
		add("IMAGE_PENDING_EXPIRATION_SCAN_INTERVAL_SECONDS 必须为正数")
	}
	if c.ImagePendingExpirationRetentionS <= 0 {
		add("IMAGE_PENDING_EXPIRATION_RETENTION_SECONDS 必须为正数")
	}
}

func validateEdgeOne(c Config, add func(string, ...any)) {
	if c.EdgeOneZoneID == "" {
		return
	}
	if !edgeOneZoneIDPattern.MatchString(c.EdgeOneZoneID) {
		add("EDGEONE_ZONE_ID 必须是 zone- 开头的小写字母数字站点 ID")
	}
	if c.TencentSecretID == "" || c.TencentSecretKey == "" || c.COSImageDomain == "" {
		add("配置 EDGEONE_ZONE_ID 时必须同时配置腾讯云密钥与 COS_IMG_DOMAIN")
	}
	if err := validateOriginURL("COS_IMG_DOMAIN", c.COSImageDomain); err != nil {
		add("EdgeOne URL 刷新要求 %v", err)
	}
}

func validateProd(c Config, origins []string, add func(string, ...any)) {
	if !c.EmailVerificationRequired {
		add("生产环境必须开启 EMAIL_VERIFICATION_REQUIRED")
	}
	if len(origins) == 0 {
		add("生产环境必须显式配置 CORS_ALLOW_ORIGINS")
	}
	if slices.Contains(origins, "*") {
		add("生产环境 CORS_ALLOW_ORIGINS 不能是 *")
	}
	if c.TencentSecretID == "" || c.TencentSecretKey == "" {
		add("生产环境必须配置 TENCENT_CLOUD_SECRET_ID / TENCENT_CLOUD_SECRET_KEY")
	}
	if c.EmailVerificationRequired {
		validateProdSES(c, add)
	}
	if c.COSBucket == "" || c.COSRegion == "" {
		add("生产环境必须配置 COS_BUCKET / COS_REGION")
	}
	if c.COSImageDomain == "" {
		add("生产环境必须配置 COS_IMG_DOMAIN")
	}
	if c.EdgeOneZoneID == "" {
		add("生产环境必须配置 EDGEONE_ZONE_ID")
	}
	if c.TencentCICallbackURL == "" || c.ModerationCallbackToken == "" {
		add("生产环境必须配置 TENCENT_CI_CALLBACK_URL / MODERATION_CALLBACK_TOKEN")
	}
}

func validateProdSES(c Config, add func(string, ...any)) {
	if c.TencentRegion == "" {
		add("生产环境必须配置 TENCENT_CLOUD_REGION")
	}
	if !validBareEmail(c.TencentSESFromEmail) {
		add("生产环境 TENCENT_SES_FROM_EMAIL 必须是裸邮箱地址")
	}
	if strings.TrimSpace(c.TencentSESFromName) == "" || strings.Contains(c.TencentSESFromName, ":") {
		add("生产环境 TENCENT_SES_FROM_NAME 不能为空且不能包含冒号")
	}
	if c.TencentSESTemplateID == 0 {
		add("生产环境必须配置正数 TENCENT_SES_TEMPLATE_ID")
	}
	if c.TencentSESResetTemplateID == 0 {
		add("生产环境必须配置正数 TENCENT_SES_RESET_TEMPLATE_ID")
	}
}

func validateSESSubject(c Config, add func(string, ...any)) {
	if !validEmailHeaderValue(c.TencentSESSubject) {
		add("TENCENT_SES_SUBJECT 不能为空且不能包含控制字符")
	}
	if !validEmailHeaderValue(c.TencentSESResetSubject) {
		add("TENCENT_SES_RESET_SUBJECT 不能为空且不能包含控制字符")
	}
}

func validBareEmail(value string) bool {
	value = strings.TrimSpace(value)
	address, err := mail.ParseAddress(value)
	return err == nil && address.Address == value
}

func validEmailHeaderValue(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	return !strings.ContainsFunc(value, unicode.IsControl)
}

func validateHTTPSURL(name, value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%s 必须是带主机名的 https URL", name)
	}
	if u.User != nil {
		return fmt.Errorf("%s 不得包含 URL 用户信息", name)
	}
	return nil
}

func validateOriginURL(name, value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return fmt.Errorf("%s 必须是 HTTPS origin", name)
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%s 只能包含 scheme 与 host，不得包含路径、查询参数或 fragment", name)
	}
	return nil
}

func validateSecret(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s 未配置", name)
	}
	if len(value) < minSecretBytes {
		return fmt.Errorf("%s 长度不足 %d 字节（当前 %d）", name, minSecretBytes, len(value))
	}
	if placeholderSecrets[strings.ToLower(strings.TrimSpace(value))] {
		return fmt.Errorf("%s 是占位值，必须替换为真实密钥", name)
	}
	return nil
}

// validateDSN 只做形态检查，不建连接——启动期校验不该依赖网络。
// TimeZone=UTC 是硬要求：不带它时间偏移会变成 +08:00，与 §4.2 的格式约定冲突。
func validateDSN(dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil {
		return errors.New("不是合法的 URL")
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return fmt.Errorf("scheme 必须是 postgres，当前 %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("缺少主机名")
	}
	if strings.Trim(u.Path, "/") == "" {
		return errors.New("缺少数据库名")
	}
	if tz := u.Query().Get("TimeZone"); !strings.EqualFold(tz, "UTC") {
		return errors.New("必须带 TimeZone=UTC，否则时间戳偏移不是 +00:00")
	}
	return nil
}
