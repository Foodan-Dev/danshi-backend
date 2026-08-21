// Package envelope 是所有 HTTP 响应的统一外壳，对应 Python 侧的
// src/app/types/response.py。
//
// 成功响应形态与 Python 契约保持不变：{code, message, data}。
// 错误响应**新增两个字段**（附加式变更，不破坏既有客户端）：
//
//	error_code  稳定的机读业务码，见 apierr/codes.go。前端据此做分支与多语言，
//	            不要去匹配 message 文案——文案会改，码不会。
//
// error_id 不是新东西：Python 侧 500 就已经在 data.error_id 返回它
// （error_handlers.py:220），位置保持不变，不要挪到顶层。
package envelope

import "github.com/jingyijun/danshi_backend_go/internal/apierr"

// Envelope 是响应体。code 与 HTTP 状态码一致（历史契约如此，不做改动）。
type Envelope[T any] struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`

	// 仅错误响应有值，成功时被 omitempty 省略。
	ErrorCode apierr.BizCode `json:"error_code,omitempty"`
}

// ErrorData 是 422 时 data 的形态。
type ErrorData struct {
	Errors []apierr.FieldError `json:"errors"`
}

// ErrorIDData 是 500 时 data 的形态，与 Python 侧的 ErrorIdData 一致。
type ErrorIDData struct {
	ErrorID string `json:"error_id"`
}

// OK 构造 200 成功响应。
func OK[T any](message string, data T) Envelope[T] {
	if message == "" {
		message = "success"
	}
	return Envelope[T]{Code: 200, Message: message, Data: data}
}

// Created 构造 201 成功响应。
func Created[T any](message string, data T) Envelope[T] {
	return Envelope[T]{Code: 201, Message: message, Data: data}
}

// FromError 把业务错误渲染成响应体。
//
// data 只有三种形态，前端的窄化判断很简单：
//
//	422        {"errors": [...]}
//	500        {"error_id": "..."}
//	其余       null
func FromError(e *apierr.Error) (int, any) {
	switch {
	case len(e.Fields) > 0:
		return e.Status, Envelope[ErrorData]{
			Code:      e.Status,
			Message:   e.Message,
			Data:      ErrorData{Errors: e.Fields},
			ErrorCode: e.Code,
		}
	case e.ErrorID != "":
		return e.Status, Envelope[ErrorIDData]{
			Code:      e.Status,
			Message:   e.Message,
			Data:      ErrorIDData{ErrorID: e.ErrorID},
			ErrorCode: e.Code,
		}
	default:
		return e.Status, Envelope[any]{
			Code:      e.Status,
			Message:   e.Message,
			Data:      nil,
			ErrorCode: e.Code,
		}
	}
}
