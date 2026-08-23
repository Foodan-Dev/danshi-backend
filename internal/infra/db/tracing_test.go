package db

import (
	"context"
	"fmt"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDatabaseTracingCreatesSanitizedClientSpan(t *testing.T) {
	database, err := gorm.Open(
		postgres.Open("host=localhost user=test password=test dbname=test sslmode=disable"),
		&gorm.Config{
			DisableAutomaticPing: true,
			DryRun:               true,
			Logger:               logger.Default.LogMode(logger.Silent),
		},
	)
	if err != nil {
		t.Fatalf("创建 dry-run GORM: %v", err)
	}
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("关闭测试 trace provider: %v", err)
		}
	})
	if err := installTracing(database, provider); err != nil {
		t.Fatalf("installTracing: %v", err)
	}

	type row struct {
		ID uint64
	}
	const sensitiveValue = "private-user-input@example.com"
	var rows []row
	result := database.WithContext(context.Background()).
		Table("posts").
		Where("author_email = ?", sensitiveValue).
		Find(&rows)
	if result.Error != nil {
		t.Fatalf("执行 dry-run 查询: %v", result.Error)
	}

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("DB span 数 = %d，期望 1", len(spans))
	}
	span := spans[0]
	if span.Name() != "SELECT posts" {
		t.Fatalf("DB span 名 = %q", span.Name())
	}
	serialized := fmt.Sprint(span.Attributes())
	if strings.Contains(serialized, sensitiveValue) || strings.Contains(serialized, "db.query.text") {
		t.Fatalf("DB span 不应包含 SQL 文本或变量值: %s", serialized)
	}
}
