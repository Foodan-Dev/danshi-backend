package tencentcloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	ses "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/ses/v20201002"

	"github.com/Foodan-Dev/danshi-backend/internal/config"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

const (
	sesEndpoint              = "ses.tencentcloudapi.com"
	sesRequestTimeoutSeconds = 2
	registrationEmailSubject = "旦食注册验证码"
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
	templateID       uint64
}

// NewSESVerificationEmailSender 从已校验配置创建生产投递器。
func NewSESVerificationEmailSender(cfg config.Config) (*SESVerificationEmailSender, error) {
	if err := validateSESConfig(cfg); err != nil {
		return nil, err
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
		return nil, fmt.Errorf("创建腾讯云 SES 客户端: %w", err)
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
		templateID: cfg.TencentSESTemplateID,
	}
}

// SendRegistrationCode 构造触发类模板邮件并同步等待供应商受理。
func (s *SESVerificationEmailSender) SendRegistrationCode(
	ctx context.Context,
	email string,
	code string,
) error {
	templateData, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		return fmt.Errorf("编码腾讯云 SES 模板参数: %w", err)
	}
	request := ses.NewSendEmailRequest()
	request.FromEmailAddress = common.StringPtr(s.fromEmailAddress)
	request.Subject = common.StringPtr(registrationEmailSubject)
	request.Destination = common.StringPtrs([]string{email})
	request.Template = &ses.Template{
		TemplateID:   common.Uint64Ptr(s.templateID),
		TemplateData: common.StringPtr(string(templateData)),
	}
	request.TriggerType = common.Uint64Ptr(1)

	response, err := s.client.SendEmailWithContext(ctx, request)
	if err != nil {
		return fmt.Errorf("腾讯云 SES 投递注册验证码: %w", err)
	}
	if response == nil || response.Response == nil ||
		response.Response.MessageId == nil || strings.TrimSpace(*response.Response.MessageId) == "" {
		return errors.New("腾讯云 SES 投递注册验证码: 响应缺少 MessageId")
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
	return nil
}

var _ service.VerificationEmailSender = (*SESVerificationEmailSender)(nil)
