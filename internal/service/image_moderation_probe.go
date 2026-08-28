package service

import (
	"context"
	"errors"
)

// ImageModerationProbeErrorKind 决定生产启动探测失败时是拒绝启动还是告警降级。
type ImageModerationProbeErrorKind string

const (
	// ImageModerationProbeAuthorization 表示凭据或权限错误，必须拒绝启动。
	ImageModerationProbeAuthorization ImageModerationProbeErrorKind = "authorization"
	// ImageModerationProbeConfiguration 表示服务没有启用等确定性配置错误。
	ImageModerationProbeConfiguration ImageModerationProbeErrorKind = "configuration"
	// ImageModerationProbeTransient 表示网络、限流或服务端暂时不可用。
	ImageModerationProbeTransient ImageModerationProbeErrorKind = "transient"
)

// ImageModerationProber 对图片审核能力执行无副作用启动探测。
type ImageModerationProber interface {
	ProbeImageModeration(context.Context) error
}

type imageModerationProbeError struct {
	kind  ImageModerationProbeErrorKind
	cause error
}

func (e imageModerationProbeError) Error() string {
	return "image moderation startup probe failed"
}

func (e imageModerationProbeError) Unwrap() error { return e.cause }

// NewImageModerationProbeError 创建不暴露供应商错误正文的启动探测分类错误。
func NewImageModerationProbeError(
	kind ImageModerationProbeErrorKind,
	cause error,
) error {
	return imageModerationProbeError{kind: kind, cause: cause}
}

// ClassifyImageModerationProbeError 返回启动门禁使用的稳定错误类别。
func ClassifyImageModerationProbeError(err error) ImageModerationProbeErrorKind {
	var classified imageModerationProbeError
	if errors.As(err, &classified) {
		return classified.kind
	}
	return ImageModerationProbeTransient
}
