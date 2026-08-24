package legacymigration

import (
	"context"
	"database/sql"
)

const (
	sourceDatabaseEnv = "SOURCE_DATABASE_URL"
	targetDatabaseEnv = "TARGET_DATABASE_URL"
)

type environment struct {
	sourceURL string
	targetURL string
}

type databasePair struct {
	source *sql.DB
	target *sql.DB
}

func loadEnvironment(getenv func(string) string) (environment, error) {
	sourceURL := getenv(sourceDatabaseEnv)
	targetURL := getenv(targetDatabaseEnv)
	if sourceURL == "" {
		return environment{}, gateError("source_database_url_missing", "必须通过 SOURCE_DATABASE_URL 提供来源库连接")
	}
	if targetURL == "" {
		return environment{}, gateError("target_database_url_missing", "必须通过 TARGET_DATABASE_URL 提供目标库连接")
	}
	if sourceURL == targetURL {
		return environment{}, gateError("database_urls_identical", "来源库与目标库不得使用同一连接")
	}
	return environment{sourceURL: sourceURL, targetURL: targetURL}, nil
}

func openDatabasePair(ctx context.Context, env environment) (databasePair, error) {
	source, err := sql.Open("pgx", env.sourceURL)
	if err != nil {
		return databasePair{}, gateError("source_open_failed", "无法初始化来源数据库连接")
	}
	if err := source.PingContext(ctx); err != nil {
		_ = source.Close()
		return databasePair{}, gateError("source_unreachable", "来源数据库不可达")
	}
	target, err := sql.Open("pgx", env.targetURL)
	if err != nil {
		_ = source.Close()
		return databasePair{}, gateError("target_open_failed", "无法初始化目标数据库连接")
	}
	if err := target.PingContext(ctx); err != nil {
		_ = source.Close()
		_ = target.Close()
		return databasePair{}, gateError("target_unreachable", "目标数据库不可达")
	}
	return databasePair{source: source, target: target}, nil
}

func (pair databasePair) close() {
	_ = pair.source.Close()
	_ = pair.target.Close()
}

// RunFromEnvironment 只从指定 getter 读取两个固定数据库环境变量并运行只读门禁。
func RunFromEnvironment(ctx context.Context, getenv func(string) string, mode Mode) (Report, error) {
	env, err := loadEnvironment(getenv)
	if err != nil {
		return Report{}, err
	}
	pair, err := openDatabasePair(ctx, env)
	if err != nil {
		return Report{}, err
	}
	defer pair.close()
	return Run(ctx, pair.source, pair.target, mode)
}
