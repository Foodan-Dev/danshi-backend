// Package apierr 定义业务错误与它到 HTTP 状态码的映射。
//
// 对应 Python 侧的 src/app/middlewares/error_handlers.py。
// 设计要点见 docs/go-rewrite-plan.md §4.1、§4.5：
//   - 校验错误体按接口语义重设计，不复刻 Pydantic 结构
//   - 401 与 403 按语义划分：未登录/令牌失效 401，已登录但权限不足 403
package apierr

import (
	"errors"
	"fmt"
	"net/http"
)

// Code 是稳定的机读错误码，前端据此做多语言，不依赖 message 文案。
type Code string

// 稳定的字段校验错误码。
const (
	CodeRequired      Code = "required"
	CodeTooLong       Code = "too_long"
	CodeTooShort      Code = "too_short"
	CodeOutOfRange    Code = "out_of_range"
	CodeInvalidFormat Code = "invalid_format"
	CodeInvalidEnum   Code = "invalid_enum"
	CodeInvalidDomain Code = "invalid_domain"
	CodeConflict      Code = "conflict"
)

// FieldError 是单个字段的校验失败。Field 用点号路径（budget_range.max、images.3），
// 不用 Pydantic 的 ["body","title"] 数组形式。
type FieldError struct {
	Field   string `json:"field"`
	Code    Code   `json:"code"`
	Message string `json:"message"`
}

// Error 是所有业务错误的统一载体。
//
// 注意 Message 会原样返回给客户端，因此绝不能把用户输入的原值或任何内部细节写进去
// ——这是 Python 侧既有的脱敏边界，Go 侧继续保持。
type Error struct {
	Status  int          // HTTP 状态码
	Message string       // 面向用户的中文文案
	Fields  []FieldError // 仅 422 使用
	cause   error        // 内部原因，只进日志不出响应
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.cause)
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.cause }

// WithCause 附加内部原因。原因只用于日志与 trace，不会出现在响应体里。
func (e *Error) WithCause(err error) *Error {
	clone := *e
	clone.cause = err
	return &clone
}

func newf(status int, format string, args ...any) *Error {
	return &Error{Status: status, Message: fmt.Sprintf(format, args...)}
}

// Unauthorized 表示「不知道你是谁」：未登录、令牌缺失、格式错误、过期、类型不对、
// 会话已撤销。一律用同一句文案，不泄露具体失败原因。
func Unauthorized() *Error {
	return newf(http.StatusUnauthorized, "未登录或登录已失效")
}

// Forbidden 表示「知道你是谁，但你不能做这件事」：权限不足、账号被封禁。
func Forbidden(message string) *Error {
	if message == "" {
		message = "没有权限执行该操作"
	}
	return newf(http.StatusForbidden, "%s", message)
}

// NotFound 表示请求的业务对象不存在。
func NotFound(what string) *Error {
	return newf(http.StatusNotFound, "%s不存在", what)
}

// Conflict 表示请求与当前资源状态冲突。
func Conflict(message string) *Error {
	return newf(http.StatusConflict, "%s", message)
}

// Invalid 构造 422 校验错误。顶层 message 取第一条字段错误的文案。
func Invalid(fields ...FieldError) *Error {
	e := &Error{Status: http.StatusUnprocessableEntity, Fields: fields}
	if len(fields) > 0 {
		e.Message = fields[0].Message
	} else {
		e.Message = "请求参数不合法"
	}
	return e
}

// InvalidField 是单字段校验失败的快捷构造。
func InvalidField(field string, code Code, format string, args ...any) *Error {
	return Invalid(FieldError{Field: field, Code: code, Message: fmt.Sprintf(format, args...)})
}

// Internal 用于一切非预期错误。响应文案固定，真实原因只进日志。
func Internal(cause error) *Error {
	return (&Error{Status: http.StatusInternalServerError, Message: "服务器内部错误"}).WithCause(cause)
}

// As 从错误链中提取 *Error；不是业务错误时归为 500。
func As(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return Internal(err)
}
