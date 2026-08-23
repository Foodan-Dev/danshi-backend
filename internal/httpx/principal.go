package httpx

import (
	"errors"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

const principalCtxKey = "danshi.auth.principal"

var errMalformedPrincipal = errors.New("authorization header malformed")

// SetCurrentPrincipal 写入当前已认证身份。
func SetCurrentPrincipal(c *app.RequestContext, principal *service.Principal) {
	c.Set(principalCtxKey, principal)
}

// CurrentPrincipal 读取 RequireAuth 写入的当前身份。
func CurrentPrincipal(c *app.RequestContext) (*service.Principal, error) {
	raw, ok := c.Get(principalCtxKey)
	if !ok {
		return nil, apierr.Unauthorized().WithCause(errMalformedPrincipal)
	}
	principal, ok := raw.(*service.Principal)
	if !ok || principal == nil {
		return nil, apierr.Unauthorized().WithCause(errMalformedPrincipal)
	}
	return principal, nil
}
