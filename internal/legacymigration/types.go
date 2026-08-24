// Package legacymigration 提供旧 FastAPI 数据库到 Go v2 schema 的只读勘察与计划门禁。
//
// 本包当前刻意不提供 apply 能力。所有对外报告只包含固定枚举和聚合计数，禁止输出
// 连接串、邮箱、正文、URL 或来源 UUID。
package legacymigration

import "errors"

const (
	// ReportSchemaVersion 是脱敏 JSON 报告的结构版本。
	ReportSchemaVersion = 1
	// MinimumSourceMajor 是本工具支持的来源 PostgreSQL 最低主版本。
	// 生产旧库是 16；隔离恢复演练允许使用更新主版本。
	MinimumSourceMajor = 16
	// ExpectedTargetMajor 是 Go v2 目标 schema 要求的 PostgreSQL 主版本。
	ExpectedTargetMajor = 18
	// ExpectedGooseVersion 是本迁移骨架唯一接受的目标 goose 版本。
	ExpectedGooseVersion = 11

	// AdvisoryLockKey 是 legacy migration 全生命周期共享的固定事务锁。
	// 后续 apply 必须复用该值，不能另起一把互不排斥的锁。
	AdvisoryLockKey int64 = 0x44414e5348494d47
)

// Mode 是迁移工具当前支持的只读操作。
type Mode string

const (
	// ModeInspect 只执行来源和目标安全勘察。
	ModeInspect Mode = "inspect"
	// ModePlan 在勘察外获取固定 advisory lock 并生成不可执行计划。
	ModePlan Mode = "plan"
)

// Report 是不含行级值和数据库标识的结构化聚合报告。
type Report struct {
	SchemaVersion   int              `json:"schema_version"`
	InspectionLevel string           `json:"inspection_level"`
	Mode            Mode             `json:"mode"`
	ApplyEnabled    bool             `json:"apply_enabled"`
	Source          SourceInspection `json:"source"`
	Target          TargetInspection `json:"target"`
	Plan            *MigrationPlan   `json:"plan,omitempty"`
}

// TransactionInspection 是数据库实际报告的事务安全属性。
type TransactionInspection struct {
	Isolation  string `json:"isolation"`
	ReadOnly   bool   `json:"read_only"`
	Deferrable bool   `json:"deferrable"`
	SearchPath string `json:"search_path"`
}

// AggregateCount 用固定 code 表示一个脱敏聚合计数。
type AggregateCount struct {
	Code  string `json:"code"`
	Count int64  `json:"count"`
}

// SourceInspection 描述来源引擎、快照模式、表规模和阻断项。
type SourceInspection struct {
	PostgresMajor int                   `json:"postgres_major"`
	Transaction   TransactionInspection `json:"transaction"`
	TableRows     []AggregateCount      `json:"table_rows"`
	Metrics       []AggregateCount      `json:"metrics"`
	Blockers      []AggregateCount      `json:"blockers"`
}

// TargetInspection 描述目标版本与 seed-only 门禁结果。
type TargetInspection struct {
	PostgresMajor        int                   `json:"postgres_major"`
	GooseVersion         int64                 `json:"goose_version"`
	SchemaFingerprint    string                `json:"schema_fingerprint"`
	Transaction          TransactionInspection `json:"transaction"`
	SeedRows             []AggregateCount      `json:"seed_rows"`
	BusinessRows         int64                 `json:"business_rows"`
	UnexpectedTableCount int64                 `json:"unexpected_table_count"`
	SeedOnly             bool                  `json:"seed_only"`
}

// MigrationPlan 是第一阶段生成的不可执行迁移拓扑。
type MigrationPlan struct {
	LockKey                  int64       `json:"advisory_lock_key"`
	Executable               bool        `json:"executable"`
	BaselineBlockersClear    bool        `json:"baseline_blockers_clear"`
	FullSourceReviewComplete bool        `json:"full_source_review_complete"`
	TargetReady              bool        `json:"target_ready"`
	Stages                   []PlanStage `json:"stages"`
	SafetyRules              []string    `json:"safety_rules"`
}

// PlanStage 以固定枚举描述一个后续 apply 阶段及其语义。
type PlanStage struct {
	Code      string `json:"code"`
	Semantics string `json:"semantics"`
}

// ErrorEnvelope 是 CLI 可公开输出的脱敏错误结构。
type ErrorEnvelope struct {
	SchemaVersion int         `json:"schema_version"`
	Status        string      `json:"status"`
	Error         PublicError `json:"error"`
}

// PublicError 只包含固定错误码和不携带底层值的消息。
type PublicError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// GateError 是安全门禁失败；Error 不包含底层数据库错误。
type GateError struct {
	public PublicError
}

func (e *GateError) Error() string {
	return e.public.Code
}

// Public 返回允许写入 CLI JSON 的脱敏错误。
func (e *GateError) Public() PublicError {
	return e.public
}

func gateError(code, message string) error {
	return &GateError{public: PublicError{Code: code, Message: message}}
}

// ErrorReport 将任意错误收敛成不会回显底层错误文本的结构。
func ErrorReport(err error) ErrorEnvelope {
	public := PublicError{Code: "internal_error", Message: "迁移勘察失败；详细原因仅保留在受控调试环境"}
	var migrationErr *GateError
	if errors.As(err, &migrationErr) {
		public = migrationErr.Public()
	}
	return ErrorEnvelope{SchemaVersion: ReportSchemaVersion, Status: "error", Error: public}
}
