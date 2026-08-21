// Package obs 是可观测性：结构化日志、指标、链路追踪。
package obs

import (
	"log/slog"
	"os"
	"strings"

	"github.com/jingyijun/danshi_backend_go/internal/config"
)

// NewLogger 建结构化日志器。
//
// 生产用 JSON（便于采集），开发用文本（便于人读）。
// 一律输出到 stdout——容器环境里日志归集是编排层的事，进程不该自己写文件。
func NewLogger(cfg config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(cfg.LogLevel)}

	var h slog.Handler
	if cfg.IsProd() {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(h).With(
		slog.String("service", "danshi-server"),
		slog.String("profile", string(cfg.Profile)),
	)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
