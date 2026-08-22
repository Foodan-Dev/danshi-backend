package testutil

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	dbinfra "github.com/jingyijun/danshi_backend_go/internal/infra/db"
)

const defaultPostgresImage = "postgres:18"

// PostgresOption 覆写测试数据库容器参数。
type PostgresOption func(*postgresOptions)

type postgresOptions struct {
	database string
	image    string
	timeout  time.Duration
}

// WithDatabaseName 覆写容器内测试数据库名。
func WithDatabaseName(name string) PostgresOption {
	return func(options *postgresOptions) { options.database = name }
}

// WithPostgresImage 覆写 PostgreSQL 镜像；默认固定为 postgres:18。
func WithPostgresImage(image string) PostgresOption {
	return func(options *postgresOptions) { options.image = image }
}

// WithPostgresStartupTimeout 覆写容器启动与迁移总超时。
func WithPostgresStartupTimeout(timeout time.Duration) PostgresOption {
	return func(options *postgresOptions) { options.timeout = timeout }
}

// TestDatabase 是已经执行正式 migrations 的 PostgreSQL 测试实例。
type TestDatabase struct {
	GORM *gorm.DB
	DB   *dbinfra.DB
	SQL  *sql.DB
	DSN  string
}

// OpenPostgres 启动 PostgreSQL 18、执行正式迁移并返回统一 GORM/UoW 入口。
func OpenPostgres(t testing.TB, options ...PostgresOption) *TestDatabase {
	t.Helper()
	settings := postgresOptions{
		database: "danshi_test",
		image:    defaultPostgresImage,
		timeout:  3 * time.Minute,
	}
	for _, option := range options {
		option(&settings)
	}
	ctx, cancel := context.WithTimeout(context.Background(), settings.timeout)
	t.Cleanup(cancel)

	container, err := tcpostgres.Run(
		ctx,
		settings.image,
		tcpostgres.WithDatabase(settings.database),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("启动 PostgreSQL 测试容器失败: %v", err)
	}
	testcontainers.CleanupContainer(t, container)

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("读取 PostgreSQL 测试连接串失败: %v", err)
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("打开 PostgreSQL 测试连接失败: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := sqlDB.Close(); closeErr != nil {
			t.Errorf("关闭 PostgreSQL 测试连接失败: %v", closeErr)
		}
	})
	if err := sqlDB.PingContext(ctx); err != nil {
		t.Fatalf("PostgreSQL 测试连接 ping 失败: %v", err)
	}
	if err := dbinfra.Up(ctx, sqlDB); err != nil {
		t.Fatalf("执行正式数据库迁移失败: %v", err)
	}

	gdb, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		DisableAutomaticPing:                     true,
		DisableForeignKeyConstraintWhenMigrating: true,
		SkipDefaultTransaction:                   true,
		Logger:                                   logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("创建测试 GORM 入口失败: %v", err)
	}
	return &TestDatabase{GORM: gdb, DB: &dbinfra.DB{DB: gdb}, SQL: sqlDB, DSN: dsn}
}
