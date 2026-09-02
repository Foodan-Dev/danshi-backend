// Package handler 负责 HTTP 绑定、响应组装与调用 service。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/httpx"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/envelope"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/obs"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

// Auth 处理 auth 域的 HTTP 请求。
type Auth struct {
	service *service.AuthService
	metrics obs.BusinessRecorder
}

// NewAuth 创建 auth handler。
func NewAuth(authService *service.AuthService, metrics obs.BusinessRecorder) *Auth {
	return &Auth{service: authService, metrics: metrics}
}

type sendVerificationCodeRequest struct {
	Email string `json:"email"`
}

type registerRequest struct {
	Email            string  `json:"email"`
	Password         string  `json:"password"`
	VerificationCode *string `json:"verification_code"`
	Name             *string `json:"name"`
	Gender           *string `json:"gender"`
	DeviceLabel      string  `json:"device_label"`
}

type loginRequest struct {
	Identifier  string `json:"identifier"`
	Password    string `json:"password"`
	DeviceLabel string `json:"device_label"`
	legacyEmail string
}

// UnmarshalJSON 接受旧客户端的 email 字段，但 OpenAPI 只声明语义正确的 identifier。
func (r *loginRequest) UnmarshalJSON(data []byte) error {
	type wire struct {
		Identifier  string `json:"identifier"`
		Email       string `json:"email"`
		Password    string `json:"password"`
		DeviceLabel string `json:"device_label"`
	}
	var value wire
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	r.Identifier = value.Identifier
	r.legacyEmail = value.Email
	r.Password = value.Password
	r.DeviceLabel = value.DeviceLabel
	return nil
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type sessionsResponse struct {
	Sessions []service.SessionView `json:"sessions"`
}

type currentUserResponse struct {
	User service.UserView `json:"user"`
}

// SendVerificationCode 发送注册验证码。
func (h *Auth) SendVerificationCode(ctx context.Context, c *app.RequestContext) {
	var request sendVerificationCodeRequest
	if err := bindJSON(c, &request); err != nil {
		httpx.Fail(ctx, c, err)
		return
	}
	if err := h.service.SendVerificationCode(ctx, request.Email); err != nil {
		var rateLimit *service.RateLimitError
		if h.metrics != nil && errors.As(err, &rateLimit) {
			h.metrics.RecordVerification(ctx, "none", "rate_limited", "rate_limited")
		}
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK[any]("如果该邮箱可以注册，验证码将发送到该邮箱", nil))
}

// Register 注册并创建首个会话。
func (h *Auth) Register(ctx context.Context, c *app.RequestContext) {
	var request registerRequest
	if err := bindJSON(c, &request); err != nil {
		httpx.Fail(ctx, c, err)
		return
	}
	result, err := h.service.Register(ctx, service.RegisterInput{
		Email: request.Email, Password: request.Password,
		VerificationCode: request.VerificationCode, Name: request.Name, Gender: request.Gender,
	}, clientInfo(c, request.DeviceLabel))
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("注册成功", result))
}

// Login 登录并创建一个新设备会话。
func (h *Auth) Login(ctx context.Context, c *app.RequestContext) {
	var request loginRequest
	if err := bindJSON(c, &request); err != nil {
		httpx.Fail(ctx, c, err)
		return
	}
	identifier, err := loginIdentifier(request)
	if err != nil {
		failService(ctx, c, err)
		return
	}
	result, err := h.service.Login(
		ctx,
		service.LoginInput{Identifier: identifier, Password: request.Password},
		clientInfo(c, request.DeviceLabel),
	)
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("登录成功", result))
}

func loginIdentifier(request loginRequest) (string, error) {
	if request.Identifier != "" && request.legacyEmail != "" {
		return "", apierr.InvalidField("identifier", apierr.FieldConflict, "identifier 与 email 不能同时提供")
	}
	if request.Identifier != "" {
		return request.Identifier, nil
	}
	return request.legacyEmail, nil
}

// Refresh 校验 refresh 会话并换发 access token。
func (h *Auth) Refresh(ctx context.Context, c *app.RequestContext) {
	var request refreshRequest
	if err := bindJSON(c, &request); err != nil {
		httpx.Fail(ctx, c, err)
		return
	}
	result, err := h.service.Refresh(ctx, request.RefreshToken)
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("刷新成功", result))
}

// Me 返回当前用户自己的账号信息。
func (h *Auth) Me(ctx context.Context, c *app.RequestContext) {
	principal, err := httpx.CurrentPrincipal(c)
	var user service.UserView
	if err == nil {
		user, err = h.service.CurrentUser(ctx, principal)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("请求成功", currentUserResponse{User: user}))
}

// Logout 撤销当前会话。
func (h *Auth) Logout(ctx context.Context, c *app.RequestContext) {
	principal, err := httpx.CurrentPrincipal(c)
	if err == nil {
		err = h.service.Logout(ctx, principal)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK[any]("退出成功", nil))
}

// LogoutAll 撤销当前用户全部会话。
func (h *Auth) LogoutAll(ctx context.Context, c *app.RequestContext) {
	principal, err := httpx.CurrentPrincipal(c)
	if err == nil {
		err = h.service.LogoutAll(ctx, principal)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK[any]("已退出所有设备", nil))
}

// Sessions 返回当前有效设备列表。
func (h *Auth) Sessions(ctx context.Context, c *app.RequestContext) {
	principal, err := httpx.CurrentPrincipal(c)
	var sessions []service.SessionView
	if err == nil {
		sessions, err = h.service.Sessions(ctx, principal)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("请求成功", sessionsResponse{Sessions: sessions}))
}

// KickSession 撤销当前用户指定的设备会话。
func (h *Auth) KickSession(ctx context.Context, c *app.RequestContext) {
	sessionID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || sessionID == 0 {
		httpx.Fail(ctx, c, apierr.InvalidField("id", apierr.FieldInvalidFormat, "会话 id 必须是正整数"))
		return
	}
	principal, err := httpx.CurrentPrincipal(c)
	if err == nil {
		err = h.service.KickSession(ctx, principal, sessionID)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK[any]("设备已退出", nil))
}

func bindJSON(c *app.RequestContext, target any) error {
	if err := c.BindJSON(target); err != nil {
		return apierr.InvalidField("body", apierr.FieldInvalidFormat, "请求体必须是合法的 JSON 对象")
	}
	return nil
}

func clientInfo(c *app.RequestContext, label string) service.ClientInfo {
	return service.ClientInfo{
		DeviceLabel: label,
		UserAgent:   string(c.UserAgent()),
		IP:          c.ClientIP(),
	}
}

func failService(ctx context.Context, c *app.RequestContext, err error) {
	var rateLimit *service.RateLimitError
	if errors.As(err, &rateLimit) {
		c.Header("Retry-After", strconv.Itoa(rateLimit.RetryAfterSeconds))
	}
	if service.ShouldCommitError(err) {
		httpx.CommitError(c)
	}
	httpx.Fail(ctx, c, err)
}
