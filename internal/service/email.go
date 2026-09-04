package service

import (
	"context"
	"errors"
	"log/slog"
)

var errVerificationEmailSenderUnconfigured = errors.New("verification email sender is not configured")

// VerificationEmailSender 是验证码投递边界；生产 SES 适配器可在不改业务逻辑的
// 前提下替换本实现。
type VerificationEmailSender interface {
	SendRegistrationCode(ctx context.Context, email, code string) error
	SendPasswordResetCode(ctx context.Context, email, code string) error
}

// SendPasswordResetCode 记录一封本应发送的密码重置验证码邮件，但不记录敏感字段。
func (s *LogVerificationEmailSender) SendPasswordResetCode(ctx context.Context, _, _ string) error {
	s.log.InfoContext(ctx, "开发环境密码重置验证码投递已模拟")
	return nil
}

// Configured 报告开发环境日志投递器可接受任务。
func (s *LogVerificationEmailSender) Configured() bool { return s != nil && s.log != nil }

// LogVerificationEmailSender 是开发环境实现。验证码只写开发日志，不用于生产。
type LogVerificationEmailSender struct {
	log *slog.Logger
}

// NewLogVerificationEmailSender 创建开发日志投递器。
func NewLogVerificationEmailSender(log *slog.Logger) *LogVerificationEmailSender {
	return &LogVerificationEmailSender{log: log}
}

// SendRegistrationCode 记录一封本应发送的注册验证码邮件，但不记录敏感字段。
func (s *LogVerificationEmailSender) SendRegistrationCode(
	ctx context.Context,
	_ string,
	_ string,
) error {
	s.log.InfoContext(ctx, "开发环境注册验证码投递已模拟")
	return nil
}

// UnavailableVerificationEmailSender 是生产适配器尚未注入时的 fail-closed 实现。
// 它绝不把验证码写日志，也不会假装投递成功。
type UnavailableVerificationEmailSender struct{}

// SendRegistrationCode 始终拒绝未配置的生产投递。
func (UnavailableVerificationEmailSender) SendRegistrationCode(
	context.Context,
	string,
	string,
) error {
	return errVerificationEmailSenderUnconfigured
}

// SendPasswordResetCode 始终拒绝未配置的生产投递。
func (UnavailableVerificationEmailSender) SendPasswordResetCode(context.Context, string, string) error {
	return errVerificationEmailSenderUnconfigured
}

// Configured 报告该投递器明确不可用。
func (UnavailableVerificationEmailSender) Configured() bool { return false }
