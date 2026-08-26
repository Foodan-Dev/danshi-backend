package tencentcloud

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	ses "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/ses/v20201002"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Foodan-Dev/danshi-backend/internal/config"
)

type fakeSESClient struct {
	ctx      context.Context
	request  *ses.SendEmailRequest
	response *ses.SendEmailResponse
	err      error
}

type sesTestContextKey struct{}

func (c *fakeSESClient) SendEmailWithContext(
	ctx context.Context,
	request *ses.SendEmailRequest,
) (*ses.SendEmailResponse, error) {
	c.ctx = ctx
	c.request = request
	return c.response, c.err
}

func TestSESVerificationEmailSenderBuildsRegistrationRequest(t *testing.T) {
	response := ses.NewSendEmailResponse()
	response.Response = &ses.SendEmailResponseParams{MessageId: common.StringPtr("message-1")}
	client := &fakeSESClient{response: response}
	cfg := sesTestConfig()
	cfg.TencentSESSubject = "[测试] 旦食注册验证码"
	sender := newSESVerificationEmailSender(cfg, client)
	ctx := context.WithValue(context.Background(), sesTestContextKey{}, "request")

	err := sender.SendRegistrationCode(ctx, "student@fdueat.com", "123456")

	require.NoError(t, err)
	require.Equal(t, ctx, client.ctx)
	require.NotNil(t, client.request)
	require.Equal(t, "旦食 <no-reply@danshi.fdueat.com>", *client.request.FromEmailAddress)
	require.Equal(t, "[测试] 旦食注册验证码", *client.request.Subject)
	require.Len(t, client.request.Destination, 1)
	require.Equal(t, "student@fdueat.com", *client.request.Destination[0])
	require.NotNil(t, client.request.Template)
	require.Equal(t, uint64(9876), *client.request.Template.TemplateID)
	require.Equal(t, `{"code":"123456"}`, *client.request.Template.TemplateData)
	require.Equal(t, uint64(1), *client.request.TriggerType)
}

func TestSESVerificationEmailSenderFailsClosed(t *testing.T) {
	_, err := NewSESVerificationEmailSender(config.Config{})
	require.ErrorContains(t, err, "配置不完整")

	invalidSubjectConfig := sesTestConfig()
	invalidSubjectConfig.TencentSESSubject = "旦食注册验证码\r\nBcc: attacker@example.com"
	_, err = NewSESVerificationEmailSender(invalidSubjectConfig)
	require.ErrorContains(t, err, "TENCENT_SES_SUBJECT")

	sender := newSESVerificationEmailSender(sesTestConfig(), &fakeSESClient{
		err: errors.New("provider rejected"),
	})
	err = sender.SendRegistrationCode(context.Background(), "student@fdueat.com", "123456")
	require.ErrorContains(t, err, "provider rejected")

	sender = newSESVerificationEmailSender(sesTestConfig(), &fakeSESClient{})
	err = sender.SendRegistrationCode(context.Background(), "student@fdueat.com", "123456")
	require.ErrorContains(t, err, "MessageId")
}

func TestSESVerificationEmailSenderCreatesSanitizedClientSpan(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	ctx, parent := provider.Tracer("test-parent").Start(context.Background(), "request")
	response := ses.NewSendEmailResponse()
	response.Response = &ses.SendEmailResponseParams{MessageId: common.StringPtr("message-1")}
	client := &fakeSESClient{response: response}
	sender := newSESVerificationEmailSender(sesTestConfig(), client)

	require.NoError(t, sender.SendRegistrationCode(ctx, "private@fdueat.com", "123456"))
	parent.End()

	spans := recorder.Ended()
	require.Len(t, spans, 2)
	require.Equal(t, "EXTERNAL tencent_ses SendEmail", spans[0].Name())
	require.Equal(t, spans[1].SpanContext().SpanID(), spans[0].Parent().SpanID())
	for _, item := range spans[0].Attributes() {
		require.NotContains(t, item.Value.String(), "private@fdueat.com")
		require.NotContains(t, item.Value.String(), "123456")
	}
}

func sesTestConfig() config.Config {
	return config.Config{
		TencentSecretID: "secret-id", TencentSecretKey: "secret-key",
		TencentRegion: "ap-guangzhou", TencentSESFromEmail: "no-reply@danshi.fdueat.com",
		TencentSESFromName: "旦食", TencentSESSubject: "旦食注册验证码",
		TencentSESTemplateID: 9876,
	}
}
