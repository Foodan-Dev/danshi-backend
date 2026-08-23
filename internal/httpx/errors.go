// Package httpx 定义 HTTP 处理器与中间件共享的请求上下文词汇。
package httpx

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
)

const (
	errCtxKey         = "danshi.error"
	commitErrorCtxKey = "danshi.commit_error"
)

// Fail 是处理器上报错误的唯一入口。
// 处理器不自己写响应体——统一由 ErrorHandler 渲染，保证错误契约只有一处实现。
func Fail(_ context.Context, c *app.RequestContext, err error) {
	c.Set(errCtxKey, err)
	c.Abort()
}

// ReportedError 返回当前请求已写入的错误及非空标记是否存在。
func ReportedError(c *app.RequestContext) (bool, error) {
	raw, ok := c.Get(errCtxKey)
	if !ok || raw == nil {
		return false, nil
	}
	err, _ := raw.(error)
	return true, err
}

// HasError 返回当前请求是否已经写入非空错误标记。
func HasError(c *app.RequestContext) bool {
	reported, _ := ReportedError(c)
	return reported
}

// CommitError 显式允许一次 4xx 业务错误响应提交当前事务。
//
// 只应用于“拒绝请求本身也必须留下安全状态”的场景，例如验证码输错后递增
// failed_attempts。普通 4xx 仍然回滚；调用方必须在全部必要写入成功后才能标记。
// 5xx 表示服务端未能可靠完成请求，无论是否误置本标记都必须回滚。
func CommitError(c *app.RequestContext) {
	c.Set(commitErrorCtxKey, true)
}

// CommitErrorRequested 返回当前请求是否显式要求提交 4xx 错误。
func CommitErrorRequested(c *app.RequestContext) bool {
	value, ok := c.Get(commitErrorCtxKey)
	commit, valid := value.(bool)
	return ok && valid && commit
}
