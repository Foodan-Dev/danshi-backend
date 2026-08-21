package middleware

import (
	"context"
	"errors"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

const principalCtxKey = "danshi.auth.principal"

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
			Fail(ctx, c, apierr.Unauthorized().WithCause(err))
			return
		}
		principal, err := auth.Authenticate(ctx, token)
		if err != nil {
			Fail(ctx, c, err)
			return
		}
		c.Set(principalCtxKey, principal)
		c.Next(ctx)
	}
}

// CurrentPrincipal 读取 RequireAuth 写入的当前身份。
func CurrentPrincipal(c *app.RequestContext) (*service.Principal, error) {
	raw, ok := c.Get(principalCtxKey)
	if !ok {
		return nil, apierr.Unauthorized().WithCause(errMalformedAuthorization)
	}
	principal, ok := raw.(*service.Principal)
	if !ok || principal == nil {
		return nil, apierr.Unauthorized().WithCause(errMalformedAuthorization)
	}
	return principal, nil
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
