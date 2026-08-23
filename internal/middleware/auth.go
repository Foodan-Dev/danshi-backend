package middleware

import (
	"context"
	"errors"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/authz"
	"github.com/jingyijun/danshi_backend_go/internal/httpx"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

var errMalformedAuthorization = errors.New("authorization header malformed")

// Authenticator 是鉴权中间件所需的 service 边界。
type Authenticator interface {
	Authenticate(ctx context.Context, accessToken string) (*service.Principal, error)
}

// RequireAuth 校验标准 Bearer header、JWT 与数据库会话状态。
func RequireAuth(auth Authenticator) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		token, err := bearerToken(string(c.GetHeader("Authorization")))
		if err != nil {
			httpx.Fail(ctx, c, apierr.Unauthorized().WithCause(err))
			return
		}
		principal, err := auth.Authenticate(ctx, token)
		if err != nil {
			httpx.Fail(ctx, c, err)
			return
		}
		httpx.SetCurrentPrincipal(c, principal)
		c.Next(ctx)
	}
}

// RequireCapability 要求已认证身份的角色并集包含指定业务能力。
func RequireCapability(capability authz.Capability) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		principal, err := httpx.CurrentPrincipal(c)
		if err != nil {
			httpx.Fail(ctx, c, err)
			return
		}
		if !authz.HasCapability(principal.User.Roles, capability) {
			httpx.Fail(ctx, c, apierr.Forbidden(apierr.BizPermissionDenied, "没有执行该操作的权限"))
			return
		}
		c.Next(ctx)
	}
}

func bearerToken(header string) (string, error) {
	header = strings.TrimSpace(header)
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "bearer") {
		return "", errMalformedAuthorization
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", errMalformedAuthorization
	}
	return token, nil
}
