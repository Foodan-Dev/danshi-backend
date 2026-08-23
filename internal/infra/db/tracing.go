package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"
)

const dbInstrumentationName = "github.com/jingyijun/danshi_backend_go/internal/infra/db"

type callbackRegister interface {
	Register(string, func(*gorm.DB)) error
}

type tracedDBContext struct {
	context.Context
	parent context.Context
}

// installTracing 只注册 span callback，不注册 OTel metrics。SQL 文本和变量均不写入
// telemetry，避免邮箱、token、密码或任意用户输入进入 trace。
func installTracing(database *gorm.DB, provider trace.TracerProvider) error {
	tracer := provider.Tracer(dbInstrumentationName)
	callbacks := database.Callback()
	registrations := []struct {
		before    callbackRegister
		after     callbackRegister
		operation string
	}{
		{callbacks.Create().Before("gorm:create"), callbacks.Create().After("gorm:create"), "CREATE"},
		{callbacks.Query().Before("gorm:query"), callbacks.Query().After("gorm:query"), "SELECT"},
		{callbacks.Update().Before("gorm:update"), callbacks.Update().After("gorm:update"), "UPDATE"},
		{callbacks.Delete().Before("gorm:delete"), callbacks.Delete().After("gorm:delete"), "DELETE"},
		{callbacks.Row().Before("gorm:row"), callbacks.Row().After("gorm:row"), "ROW"},
		{callbacks.Raw().Before("gorm:raw"), callbacks.Raw().After("gorm:raw"), "RAW"},
	}

	for _, registration := range registrations {
		beforeName := "danshi:trace:before:" + strings.ToLower(registration.operation)
		if err := registration.before.Register(beforeName, beforeDBOperation(tracer, registration.operation)); err != nil {
			return fmt.Errorf("注册 %s before callback: %w", registration.operation, err)
		}
		afterName := "danshi:trace:after:" + strings.ToLower(registration.operation)
		if err := registration.after.Register(afterName, afterDBOperation(registration.operation)); err != nil {
			return fmt.Errorf("注册 %s after callback: %w", registration.operation, err)
		}
	}
	return nil
}

func beforeDBOperation(tracer trace.Tracer, operation string) func(*gorm.DB) {
	return func(tx *gorm.DB) {
		parent := tx.Statement.Context
		ctx, _ := tracer.Start(
			parent,
			"DB "+operation,
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("db.system.name", "postgresql"),
				attribute.String("db.operation.name", operation),
			),
		)
		tx.Statement.Context = tracedDBContext{Context: ctx, parent: parent}
	}
}

func afterDBOperation(operation string) func(*gorm.DB) {
	return func(tx *gorm.DB) {
		wrapped, ok := tx.Statement.Context.(tracedDBContext)
		if !ok {
			return
		}
		defer func() {
			tx.Statement.Context = wrapped.parent
		}()

		span := trace.SpanFromContext(wrapped)
		if table := tx.Statement.Table; table != "" {
			span.SetName(operation + " " + table)
			span.SetAttributes(attribute.String("db.collection.name", table))
		}
		if tx.Error != nil && !errors.Is(tx.Error, gorm.ErrRecordNotFound) {
			span.RecordError(tx.Error)
			span.SetStatus(codes.Error, "database operation failed")
		}
		span.End()
	}
}
