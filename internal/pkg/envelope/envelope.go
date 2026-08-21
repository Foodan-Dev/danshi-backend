// Package envelope 是所有 HTTP 响应的统一外壳，对应 Python 侧的
// src/app/types/response.py。契约形态保持不变：{code, message, data}。
package envelope

import "github.com/jingyijun/danshi_backend_go/internal/apierr"

// Envelope 是响应体。code 与 HTTP 状态码一致（历史契约如此，不做改动）。
type Envelope[T any] struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

// ErrorData 是 422 时 data 的形态。其余错误状态 data 为 null。
type ErrorData struct {
	Errors []apierr.FieldError `json:"errors"`
}

// OK 构造成功响应。
func OK[T any](message string, data T) Envelope[T] {
	if message == "" {
		message = "success"
	}
	return Envelope[T]{Code: 200, Message: message, Data: data}
}

// Created 构造资源创建成功响应。
func Created[T any](message string, data T) Envelope[T] {
	return Envelope[T]{Code: 201, Message: message, Data: data}
}

// FromError 把业务错误渲染成响应体。
// 只有 422 会带 data.errors，其余一律 data: null——这样前端的窄化判断很简单。
func FromError(e *apierr.Error) (int, any) {
	if len(e.Fields) > 0 {
		return e.Status, Envelope[ErrorData]{
			Code: e.Status, Message: e.Message, Data: ErrorData{Errors: e.Fields},
		}
	}
	return e.Status, Envelope[any]{Code: e.Status, Message: e.Message, Data: nil}
}
