// Package apierr 定义业务错误与它到 HTTP 状态码的映射。
//
// 对应 Python 侧的 src/app/middlewares/error_handlers.py。
// 设计要点见 docs/go-rewrite-plan.md §4.1、§4.5：
//   - 校验错误体按接口语义重设计，不复刻 Pydantic 结构
//   - 401 与 403 按语义划分：未登录/令牌失效 401，已登录但权限不足 403
//   - 401 文案一律脱敏，具体原因只进日志（带 error_id）
//
// 错误码清单在 codes.go，那里是唯一真源。
package apierr

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
)

// FieldError 是单个字段的校验失败。Field 用点号路径（budget_range.max、images.3），
// 不用 Pydantic 的 ["body","title"] 数组形式。
type FieldError struct {
	Field   string    `json:"field"`
	Code    FieldCode `json:"code"`
	Message string    `json:"message"`
}

// Error 是所有业务错误的统一载体。
//
// 注意 Message 会原样返回给客户端，因此绝不能把用户输入的原值或任何内部细节写进去
// ——这是 Python 侧既有的脱敏边界，Go 侧继续保持。
type Error struct {
	Status  int          // HTTP 状态码
	Code    BizCode      // 稳定的机读业务码，见 codes.go
	Message string       // 面向用户的中文文案
	Fields  []FieldError // 仅 422 使用
	ErrorID string       // 仅 500 使用，与日志一一对应
	cause   error        // 内部原因，只进日志不出响应
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.cause)
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.cause }

// Cause 返回内部原因，供日志使用。
func (e *Error) Cause() error { return e.cause }

// WithCause 附加内部原因。原因只用于日志与 trace，不会出现在响应体里。
func (e *Error) WithCause(err error) *Error {
	clone := *e
	clone.cause = err
	return &clone
}

// WithCode 覆盖业务码。用于把泛化错误收窄成具体情形，
// 例如把 403 从 permission_denied 收窄成 account_banned。
func (e *Error) WithCode(code BizCode) *Error {
	clone := *e
	clone.Code = code
	return &clone
}

func newf(status int, code BizCode, format string, args ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

// Unauthorized 表示「不知道你是谁」：未登录、令牌缺失、格式错误、过期、类型不对、
// 会话已撤销。**一律用同一句文案**，不泄露具体失败原因——
// Python 侧曾按 7 种原因给不同提示，那是一条信息泄露路径。
func Unauthorized() *Error {
	return newf(http.StatusUnauthorized, BizUnauthorized, "未登录或登录已失效")
}

// BadRequest 构造语法正确但业务凭据无效的 400，例如一次性验证码错误或已过期。
func BadRequest(code BizCode, message string) *Error {
	if code == "" {
		code = BizValidation
	}
	return newf(http.StatusBadRequest, code, "%s", message)
}

// Forbidden 表示「知道你是谁，但你不能做这件事」：权限不足、账号被封禁。
func Forbidden(code BizCode, message string) *Error {
	if message == "" {
		message = "没有权限执行该操作"
	}
	if code == "" {
		code = BizPermissionDenied
	}
	return newf(http.StatusForbidden, code, "%s", message)
}

// NotFound 构造 404。code 传空则用泛化的 not_found。
func NotFound(code BizCode, what string) *Error {
	if code == "" {
		code = BizNotFound
	}
	return newf(http.StatusNotFound, code, "%s不存在", what)
}

// Conflict 构造 409。
func Conflict(code BizCode, message string) *Error {
	if code == "" {
		code = BizConflict
	}
	return newf(http.StatusConflict, code, "%s", message)
}

// TooManyRequests 构造 429；Retry-After 由 HTTP handler 根据领域错误补充。
func TooManyRequests(code BizCode, message string) *Error {
	if code == "" {
		code = BizRateLimited
	}
	return newf(http.StatusTooManyRequests, code, "%s", message)
}

// Invalid 构造 422 校验错误。顶层 message 取第一条字段错误的文案。
func Invalid(fields ...FieldError) *Error {
	e := &Error{Status: http.StatusUnprocessableEntity, Code: BizValidation, Fields: fields}
	if len(fields) > 0 {
		e.Message = fields[0].Message
	} else {
		e.Message = "请求参数不合法"
	}
	return e
}

// InvalidField 是单字段校验失败的快捷构造。
func InvalidField(field string, code FieldCode, format string, args ...any) *Error {
	return Invalid(FieldError{Field: field, Code: code, Message: fmt.Sprintf(format, args...)})
}

// Internal 用于一切非预期错误。
// 响应文案固定、不含任何细节；真实原因只进日志，靠 ErrorID 与响应关联。
// 用户报障时报这个 id，运维就能直接定位到那一条日志。
func Internal(cause error) *Error {
	return &Error{
		Status:  http.StatusInternalServerError,
		Code:    BizInternal,
		Message: "服务器内部错误",
		ErrorID: newErrorID(),
		cause:   cause,
	}
}

// As 从错误链中提取 *Error；不是业务错误时归为 500。
// 顺带兜住业务码为空的情况，保证响应体里 error_code 永远有值。
func As(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		if e.Code == "" {
			clone := *e
			clone.Code = defaultBizCode(e.Status)
			return &clone
		}
		return e
	}
	return Internal(err)
}

func newErrorID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b)
}
