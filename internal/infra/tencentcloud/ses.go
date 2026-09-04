package tencentcloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"unicode"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	ses "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/ses/v20201002"

	"github.com/Foodan-Dev/danshi-backend/internal/config"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/obs"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

const (
	sesEndpoint              = "ses.tencentcloudapi.com"
	sesRequestTimeoutSeconds = 2
	sesInstrumentationName   = "github.com/Foodan-Dev/danshi-backend/internal/infra/tencentcloud/ses"
)

type sesClient interface {
	SendEmailWithContext(
		ctx context.Context,
		request *ses.SendEmailRequest,
	) (*ses.SendEmailResponse, error)
}

// SESVerificationEmailSender 通过腾讯云 SES 模板邮件投递注册验证码。
type SESVerificationEmailSender struct {
	client           sesClient
	fromEmailAddress string
	subject          string
	templateID       uint64
	resetSubject     string
	resetTemplateID  uint64
	redactor         knownSecretRedactor
}

// NewSESVerificationEmailSender 从已校验配置创建生产投递器。
func NewSESVerificationEmailSender(cfg config.Config) (*SESVerificationEmailSender, error) {
	if err := validateSESConfig(cfg); err != nil {
		return nil, newKnownSecretRedactor(cfg).redact(err)
	}
	clientProfile := profile.NewClientProfile()
	clientProfile.HttpProfile.Endpoint = sesEndpoint
	clientProfile.HttpProfile.ReqTimeout = sesRequestTimeoutSeconds
	client, err := ses.NewClient(
		common.NewCredential(cfg.TencentSecretID, cfg.TencentSecretKey),
		cfg.TencentRegion,
		clientProfile,
	)
	if err != nil {
		return nil, newKnownSecretRedactor(cfg).redact(
			fmt.Errorf("创建腾讯云 SES 客户端: %w", err),
		)
	}
	return newSESVerificationEmailSender(cfg, client), nil
}

func newSESVerificationEmailSender(
	cfg config.Config,
	client sesClient,
) *SESVerificationEmailSender {
	return &SESVerificationEmailSender{
		client: client,
		fromEmailAddress: fmt.Sprintf(
			"%s <%s>",
			strings.TrimSpace(cfg.TencentSESFromName),
			strings.TrimSpace(cfg.TencentSESFromEmail),
		),
		subject:         strings.TrimSpace(cfg.TencentSESSubject),
		templateID:      cfg.TencentSESTemplateID,
		resetSubject:    strings.TrimSpace(cfg.TencentSESResetSubject),
		resetTemplateID: cfg.TencentSESResetTemplateID,
		redactor:        newKnownSecretRedactor(cfg),
	}
}

// SendRegistrationCode 构造触发类模板邮件并同步等待供应商受理。
func (s *SESVerificationEmailSender) SendRegistrationCode(
	ctx context.Context,
	email string,
	code string,
) (err error) {
	return s.sendCode(ctx, email, map[string]string{"code": code}, s.subject, s.templateID, "注册")
}

// SendPasswordResetCode 使用独立主题和模板，避免把找回密码邮件误标为注册邮件。
func (s *SESVerificationEmailSender) SendPasswordResetCode(ctx context.Context, email, code string) error {
	return s.sendCode(ctx, email, map[string]string{
		"code":               code,
		"expires_in_minutes": "10",
		"security_notice":    "非本人操作请忽略",
	}, s.resetSubject, s.resetTemplateID, "密码重置")
}

// Configured 报告 SES 适配器已通过配置校验并完成初始化。
func (s *SESVerificationEmailSender) Configured() bool { return s != nil && s.client != nil }

func (s *SESVerificationEmailSender) sendCode(
	ctx context.Context, email string, templateData map[string]string,
	subject string, templateID uint64, purpose string,
) (err error) {
	defer func() { err = s.redactor.redact(err) }()
	encodedTemplateData, err := json.Marshal(templateData)
	if err != nil {
		return fmt.Errorf("编码腾讯云 SES 模板参数: %w", err)
	}
	request := ses.NewSendEmailRequest()
	request.FromEmailAddress = common.StringPtr(s.fromEmailAddress)
	request.Subject = common.StringPtr(subject)
	request.Destination = common.StringPtrs([]string{email})
	request.Template = &ses.Template{
		TemplateID:   common.Uint64Ptr(templateID),
		TemplateData: common.StringPtr(string(encodedTemplateData)),
	}
	request.TriggerType = common.Uint64Ptr(1)

	ctx, span := obs.StartExternalCall(
		ctx, sesInstrumentationName, "tencent_ses", "SendEmail",
	)
	defer func() { obs.EndExternalCall(span, s.redactor.redact(err)) }()

	response, err := s.client.SendEmailWithContext(ctx, request)
	if err != nil {
		return fmt.Errorf("腾讯云 SES 投递%s验证码: %w", purpose, err)
	}
	if response == nil || response.Response == nil ||
		response.Response.MessageId == nil || strings.TrimSpace(*response.Response.MessageId) == "" {
		return fmt.Errorf("腾讯云 SES 投递%s验证码: 响应缺少 MessageId", purpose)
	}
	return nil
}

func validateSESConfig(cfg config.Config) error {
	if !cfg.TencentSESConfigured() {
		return errors.New("腾讯云 SES 配置不完整")
	}
	fromEmail := strings.TrimSpace(cfg.TencentSESFromEmail)
	address, err := mail.ParseAddress(fromEmail)
	if err != nil || address.Address != fromEmail {
		return errors.New("TENCENT_SES_FROM_EMAIL 必须是裸邮箱地址")
	}
	if strings.Contains(cfg.TencentSESFromName, ":") {
		return errors.New("TENCENT_SES_FROM_NAME 不能包含冒号")
	}
	if strings.TrimSpace(cfg.TencentSESSubject) == "" ||
		strings.ContainsFunc(cfg.TencentSESSubject, unicode.IsControl) {
		return errors.New("TENCENT_SES_SUBJECT 不能为空且不能包含控制字符")
	}
	if strings.TrimSpace(cfg.TencentSESResetSubject) == "" ||
		strings.ContainsFunc(cfg.TencentSESResetSubject, unicode.IsControl) {
		return errors.New("TENCENT_SES_RESET_SUBJECT 不能为空且不能包含控制字符")
	}
	return nil
}

var _ service.VerificationEmailSender = (*SESVerificationEmailSender)(nil)
