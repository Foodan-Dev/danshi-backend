package legacymigration

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestLoadEnvironmentOnlyAcceptsTwoNamedVariables(t *testing.T) {
	values := map[string]string{
		sourceDatabaseEnv: "source-secret-dsn",
		targetDatabaseEnv: "target-secret-dsn",
	}
	env, err := loadEnvironment(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("loadEnvironment: %v", err)
	}
	if env.sourceURL != values[sourceDatabaseEnv] || env.targetURL != values[targetDatabaseEnv] {
		t.Fatal("环境变量没有原样进入私有连接配置")
	}
}

func TestLoadEnvironmentRejectsMissingOrIdenticalConnections(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
		code   string
	}{
		{name: "missing source", values: map[string]string{targetDatabaseEnv: "target"}, code: "source_database_url_missing"},
		{name: "missing target", values: map[string]string{sourceDatabaseEnv: "source"}, code: "target_database_url_missing"},
		{name: "identical", values: map[string]string{sourceDatabaseEnv: "same", targetDatabaseEnv: "same"}, code: "database_urls_identical"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := loadEnvironment(func(key string) string { return test.values[key] })
			if err == nil || err.Error() != test.code {
				t.Fatalf("期望 %s，实际 %v", test.code, err)
			}
		})
	}
}

func TestParseModeRejectsConnectionFlagsWithoutEchoingThem(t *testing.T) {
	secretFlag := "--source=postgres://user:password@private/db"
	_, err := ParseMode([]string{secretFlag})
	if err == nil {
		t.Fatal("连接参数不得从命令行接受")
	}
	encoded, marshalErr := json.Marshal(ErrorReport(err))
	if marshalErr != nil {
		t.Fatalf("json.Marshal: %v", marshalErr)
	}
	if strings.Contains(string(encoded), secretFlag) || strings.Contains(string(encoded), "password") {
		t.Fatalf("参数错误回显了敏感 flag：%s", encoded)
	}
}

func TestErrorReportNeverEchoesUnknownError(t *testing.T) {
	secret := "postgres://user:password@host/db?token=do-not-print"
	report := ErrorReport(errors.New(secret))
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "password") {
		t.Fatalf("错误报告泄露了底层错误：%s", encoded)
	}
}

func TestPlanPinsV11MigrationSemantics(t *testing.T) {
	plan := buildPlan(SourceInspection{}, TargetInspection{SeedOnly: true})
	expectedStages := []PlanStage{
		{Code: "users_and_rbac", Semantics: "admin 仅授予 moderator；super_admin 授予 super_admin；同步追加角色与封禁起点记录"},
		{Code: "images_and_tags", Semantics: "历史图片与标签 grandfather 为 pass，并各自追加 legacy_migration 审核记录"},
		{Code: "posts", Semantics: "派生互动计数从关系重建；view_count 不可重建，插入后原值更新且不得改 updated_at"},
		{Code: "comments", Semantics: "历史评论 grandfather 为 moderation=pass，并逐条追加 legacy_migration 文本审核记录"},
		{Code: "relations", Semantics: "评论父链先拓扑化；关注收藏与多态点赞通过 ID 映射装载"},
		{Code: "notifications", Semantics: "通知目标与缺失预览只能唯一映射，禁止取第一条或最近一条"},
		{Code: "histories", Semantics: "来源只有当前版本时 post_histories 与 comment_histories 保持为空，不伪造 revision=1"},
		{Code: "verify", Semantics: "映射、外键、审核、计数、sequence、时间戳与显式排除清单全部 fail closed"},
	}
	if !reflect.DeepEqual(plan.Stages, expectedStages) {
		t.Fatalf("v11 迁移阶段语义漂移：\n实际 %#v\n期望 %#v", plan.Stages, expectedStages)
	}
	if plan.Executable || plan.FullSourceReviewComplete {
		t.Fatal("第一阶段计划不得标记为可执行或已完成完整来源评审")
	}
	if plan.LockKey != AdvisoryLockKey {
		t.Fatalf("计划未使用固定 advisory lock：%d", plan.LockKey)
	}
}
