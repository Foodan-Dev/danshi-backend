package legacymigration

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"unicode/utf8"
)

const (
	// DatasetArtifactSchemaVersion 是 dataset artifact 当前唯一支持的结构版本。
	DatasetArtifactSchemaVersion = 1
	// PlanArtifactSchemaVersion 是 plan artifact 当前唯一支持的结构版本。
	PlanArtifactSchemaVersion = 1
	datasetArtifactType       = "danshi_legacy_dataset"
	planArtifactType          = "danshi_legacy_plan"
)

// ArtifactDigest 是 canonical artifact 原始字节的 SHA-256。
type ArtifactDigest [sha256.Size]byte

func (digest ArtifactDigest) String() string {
	return hex.EncodeToString(digest[:])
}

func (digest ArtifactDigest) equal(other ArtifactDigest) bool {
	return subtle.ConstantTimeCompare(digest[:], other[:]) == 1
}

// ParseArtifactDigest 解析审批渠道提供的 artifact SHA-256。
func ParseArtifactDigest(value string, code string) (ArtifactDigest, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return ArtifactDigest{}, gateError(code+"_invalid", "获批 artifact SHA-256 格式无效")
	}
	var digest ArtifactDigest
	copy(digest[:], decoded)
	if digest == (ArtifactDigest{}) {
		return ArtifactDigest{}, gateError(code+"_required", "获批 artifact SHA-256 不能为空")
	}
	return digest, nil
}

// DatasetArtifact 是 inspect 进程输出的脱敏、确定性来源数据集证明。
type DatasetArtifact struct {
	SchemaVersion                 int              `json:"schema_version"`
	ArtifactType                  string           `json:"artifact_type"`
	DatasetContentVersion         int              `json:"dataset_content_version"`
	InspectionLevel               string           `json:"inspection_level"`
	SourceSchemaContractValidated bool             `json:"source_schema_contract_validated"`
	SourceDatabaseIdentitySHA256  string           `json:"source_database_identity_sha256"`
	SourceSnapshotSHA256          string           `json:"source_snapshot_sha256"`
	SourcePostgresMajor           int              `json:"source_postgres_major"`
	SourceTableRows               []AggregateCount `json:"source_table_rows"`
	SourceMetrics                 []AggregateCount `json:"source_metrics"`
	SourceBlockers                []AggregateCount `json:"source_blockers"`
	TargetDatabaseIdentitySHA256  string           `json:"target_database_identity_sha256"`
	TargetPostgresMajor           int              `json:"target_postgres_major"`
	TargetGooseVersion            int64            `json:"target_goose_version"`
	TargetSchemaFingerprint       string           `json:"target_schema_fingerprint"`
	TargetSeedRows                []AggregateCount `json:"target_seed_rows"`
}

// PlanArtifact 绑定外部批准的 dataset/manifest 与同一时刻重新勘察的数据库状态。
type PlanArtifact struct {
	SchemaVersion                 int             `json:"schema_version"`
	ArtifactType                  string          `json:"artifact_type"`
	ApplyEnabled                  bool            `json:"apply_enabled"`
	DatasetArtifactSHA256         string          `json:"dataset_artifact_sha256"`
	ManifestSHA256                string          `json:"manifest_sha256"`
	ApprovalReceiptSHA256         string          `json:"approval_receipt_sha256"`
	ApprovalPublicKeySHA256       string          `json:"approval_public_key_sha256"`
	SourceDatabaseIdentitySHA256  string          `json:"source_database_identity_sha256"`
	SourceSnapshotSHA256          string          `json:"source_snapshot_sha256"`
	TargetDatabaseIdentitySHA256  string          `json:"target_database_identity_sha256"`
	TargetSchemaFingerprint       string          `json:"target_schema_fingerprint"`
	TargetGooseVersion            int64           `json:"target_goose_version"`
	ManifestSummary               ManifestSummary `json:"manifest_summary"`
	ManifestCoverageValidated     bool            `json:"manifest_coverage_validated"`
	SourceSchemaContractValidated bool            `json:"source_schema_contract_validated"`
	Plan                          MigrationPlan   `json:"plan"`
}

// ArtifactReceipt 只公开类型、版本和 digest，不公开本地路径。
type ArtifactReceipt struct {
	ArtifactType  string `json:"artifact_type"`
	SchemaVersion int    `json:"schema_version"`
	SHA256        string `json:"sha256"`
}

// ExecutionReport 是 inspect/plan 命令的公开脱敏输出。
type ExecutionReport struct {
	SchemaVersion   int              `json:"schema_version"`
	Status          string           `json:"status"`
	Mode            Mode             `json:"mode"`
	ApplyEnabled    bool             `json:"apply_enabled"`
	Inspection      Report           `json:"inspection"`
	DatasetArtifact ArtifactReceipt  `json:"dataset_artifact"`
	PlanArtifact    *ArtifactReceipt `json:"plan_artifact,omitempty"`
}

func buildDatasetArtifact(observation migrationObservation) DatasetArtifact {
	report := observation.report
	return DatasetArtifact{
		SchemaVersion:                 DatasetArtifactSchemaVersion,
		ArtifactType:                  datasetArtifactType,
		DatasetContentVersion:         sourceDatasetContentVersion,
		InspectionLevel:               report.InspectionLevel,
		SourceSchemaContractValidated: false,
		SourceDatabaseIdentitySHA256:  observation.sourceDatabaseIdentitySHA256,
		SourceSnapshotSHA256:          observation.sourceSnapshotSHA256,
		SourcePostgresMajor:           report.Source.PostgresMajor,
		SourceTableRows:               cloneAggregateCounts(report.Source.TableRows),
		SourceMetrics:                 cloneAggregateCounts(report.Source.Metrics),
		SourceBlockers:                cloneAggregateCounts(report.Source.Blockers),
		TargetDatabaseIdentitySHA256:  observation.targetDatabaseIdentitySHA256,
		TargetPostgresMajor:           report.Target.PostgresMajor,
		TargetGooseVersion:            report.Target.GooseVersion,
		TargetSchemaFingerprint:       report.Target.SchemaFingerprint,
		TargetSeedRows:                cloneAggregateCounts(report.Target.SeedRows),
	}
}

func buildPlanArtifact(
	observation migrationObservation,
	datasetDigest ArtifactDigest,
	manifestDigest ManifestDigest,
	manifestSummary ManifestSummary,
	approval verifiedPlanApproval,
) PlanArtifact {
	dataset := buildDatasetArtifact(observation)
	plan := *observation.report.Plan
	plan.Stages = append([]PlanStage(nil), plan.Stages...)
	plan.SafetyRules = append([]string(nil), plan.SafetyRules...)
	return PlanArtifact{
		SchemaVersion:                 PlanArtifactSchemaVersion,
		ArtifactType:                  planArtifactType,
		ApplyEnabled:                  false,
		DatasetArtifactSHA256:         datasetDigest.String(),
		ManifestSHA256:                manifestDigest.String(),
		ApprovalReceiptSHA256:         approval.receiptSHA256,
		ApprovalPublicKeySHA256:       approval.publicKeySHA256,
		SourceDatabaseIdentitySHA256:  dataset.SourceDatabaseIdentitySHA256,
		SourceSnapshotSHA256:          dataset.SourceSnapshotSHA256,
		TargetDatabaseIdentitySHA256:  dataset.TargetDatabaseIdentitySHA256,
		TargetSchemaFingerprint:       dataset.TargetSchemaFingerprint,
		TargetGooseVersion:            dataset.TargetGooseVersion,
		ManifestSummary:               cloneManifestSummary(manifestSummary),
		ManifestCoverageValidated:     false,
		SourceSchemaContractValidated: false,
		Plan:                          plan,
	}
}

func canonicalArtifactBytes(value any) ([]byte, ArtifactDigest, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, ArtifactDigest{}, gateError("artifact_encode_failed", "无法生成 canonical artifact")
	}
	digest := sha256.Sum256(data)
	return data, ArtifactDigest(digest), nil
}

func loadDatasetArtifact(path string, expected ArtifactDigest) (DatasetArtifact, []byte, error) {
	if expected == (ArtifactDigest{}) {
		return DatasetArtifact{}, nil, gateError("dataset_digest_required", "必须提供外部获批的 dataset artifact SHA-256")
	}
	data, err := readExecutionArtifactFile(path)
	if err != nil {
		return DatasetArtifact{}, nil, err
	}
	actual := ArtifactDigest(sha256.Sum256(data))
	if !actual.equal(expected) {
		return DatasetArtifact{}, nil, gateError("dataset_digest_mismatch", "dataset artifact 与外部获批摘要不一致")
	}
	var artifact DatasetArtifact
	if err := decodeCanonicalArtifact(data, &artifact); err != nil {
		return DatasetArtifact{}, nil, err
	}
	if err := validateDatasetArtifact(artifact); err != nil {
		return DatasetArtifact{}, nil, err
	}
	return artifact, data, nil
}

func loadPlanArtifact(path string, expected ArtifactDigest) (PlanArtifact, error) {
	if expected == (ArtifactDigest{}) {
		return PlanArtifact{}, gateError("plan_digest_required", "必须提供外部获批的 plan artifact SHA-256")
	}
	data, err := readExecutionArtifactFile(path)
	if err != nil {
		return PlanArtifact{}, err
	}
	actual := ArtifactDigest(sha256.Sum256(data))
	if !actual.equal(expected) {
		return PlanArtifact{}, gateError("plan_digest_mismatch", "plan artifact 与外部获批摘要不一致")
	}
	var artifact PlanArtifact
	if err := decodeCanonicalArtifact(data, &artifact); err != nil {
		return PlanArtifact{}, err
	}
	if err := validatePlanArtifact(artifact); err != nil {
		return PlanArtifact{}, err
	}
	return artifact, nil
}

func decodeCanonicalArtifact(data []byte, target any) error {
	if !utf8.Valid(data) {
		return gateError("artifact_invalid_json", "审批 artifact 必须是合法 UTF-8 JSON")
	}
	if err := validateJSONSurrogatePairs(data); err != nil {
		return gateError("artifact_invalid_json", "审批 artifact 含无效 Unicode")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return gateError("artifact_invalid_json", "审批 artifact 不是唯一键 JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return gateError("artifact_schema_invalid", "审批 artifact 不符合已知结构")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return gateError("artifact_invalid_json", "审批 artifact 只能包含一个 JSON object")
	}
	canonical, err := json.Marshal(target)
	if err != nil || !bytes.Equal(canonical, data) {
		return gateError("artifact_not_canonical", "审批 artifact 不是 canonical JSON")
	}
	return nil
}

func validateDatasetArtifact(artifact DatasetArtifact) error {
	if artifact.SchemaVersion != DatasetArtifactSchemaVersion ||
		artifact.ArtifactType != datasetArtifactType ||
		artifact.DatasetContentVersion != sourceDatasetContentVersion ||
		artifact.InspectionLevel != "foundation_preflight" || artifact.SourceSchemaContractValidated {
		return gateError("dataset_artifact_version_mismatch", "dataset artifact 版本或类型不受支持")
	}
	for _, digest := range []string{
		artifact.SourceDatabaseIdentitySHA256,
		artifact.SourceSnapshotSHA256,
		artifact.TargetDatabaseIdentitySHA256,
		artifact.TargetSchemaFingerprint,
	} {
		if !isCanonicalSHA256(digest) {
			return gateError("dataset_artifact_digest_invalid", "dataset artifact 含无效摘要")
		}
	}
	if artifact.SourcePostgresMajor < MinimumSourceMajor ||
		artifact.TargetPostgresMajor != ExpectedTargetMajor ||
		artifact.TargetGooseVersion != ExpectedGooseVersion ||
		artifact.TargetSchemaFingerprint != expectedTargetSchemaFingerprint {
		return gateError("dataset_artifact_contract_mismatch", "dataset artifact 不符合迁移数据库契约")
	}
	if !aggregateCodesEqual(artifact.SourceTableRows, []string{
		"users", "image_assets", "posts", "comments", "follows", "favorites", "likes", "notifications",
		"email_verification_codes",
	}) {
		return gateError("dataset_artifact_table_counts_invalid", "dataset artifact 来源表计数无效")
	}
	if !aggregateCodesEqual(artifact.SourceMetrics, []string{
		"inactive_users", "admin_users", "super_admin_users", "legacy_comment_moderation_rows",
		"legacy_image_moderation_rows", "posts_with_view_count", "view_count_total",
		"current_post_rows_not_histories", "current_comment_rows_not_histories",
	}) {
		return gateError("dataset_artifact_metrics_invalid", "dataset artifact 来源指标无效")
	}
	if !validAggregateCounts(artifact.SourceBlockers) || artifact.SourceBlockers == nil {
		return gateError("dataset_artifact_blockers_invalid", "dataset artifact 来源阻断项无效")
	}
	if !aggregateCodesEqual(artifact.TargetSeedRows, []string{"canteens", "cuisines", "flavors"}) {
		return gateError("dataset_artifact_seed_counts_invalid", "dataset artifact 目标 seed 计数无效")
	}
	return nil
}

func validatePlanArtifact(artifact PlanArtifact) error {
	if artifact.SchemaVersion != PlanArtifactSchemaVersion || artifact.ArtifactType != planArtifactType {
		return gateError("plan_artifact_version_mismatch", "plan artifact 版本或类型不受支持")
	}
	for _, digest := range []string{
		artifact.DatasetArtifactSHA256,
		artifact.ManifestSHA256,
		artifact.ApprovalReceiptSHA256,
		artifact.ApprovalPublicKeySHA256,
		artifact.SourceDatabaseIdentitySHA256,
		artifact.SourceSnapshotSHA256,
		artifact.TargetDatabaseIdentitySHA256,
		artifact.TargetSchemaFingerprint,
	} {
		if !isCanonicalSHA256(digest) {
			return gateError("plan_artifact_digest_invalid", "plan artifact 含无效摘要")
		}
	}
	if artifact.TargetGooseVersion != ExpectedGooseVersion ||
		artifact.TargetSchemaFingerprint != expectedTargetSchemaFingerprint ||
		artifact.ApplyEnabled || artifact.Plan.Executable || artifact.ManifestCoverageValidated ||
		artifact.SourceSchemaContractValidated || artifact.Plan.FullSourceReviewComplete || !artifact.Plan.TargetReady {
		return gateError("plan_artifact_not_currently_executable", "当前 plan artifact 不得开放 apply")
	}
	expected := buildPlan(SourceInspection{}, TargetInspection{SeedOnly: artifact.Plan.TargetReady})
	if !reflect.DeepEqual(artifact.Plan.Stages, expected.Stages) ||
		!reflect.DeepEqual(artifact.Plan.SafetyRules, expected.SafetyRules) ||
		artifact.Plan.LockKey != AdvisoryLockKey || artifact.ManifestSummary.SchemaVersion != ManifestSchemaVersion {
		return gateError("plan_artifact_contract_mismatch", "plan artifact 迁移语义与当前实现不一致")
	}
	if !aggregateCodesEqual(artifact.ManifestSummary.Sections, requiredManifestSections) ||
		artifact.ManifestSummary.TotalEntries != sumAggregateCounts(artifact.ManifestSummary.Sections) {
		return gateError("plan_artifact_manifest_summary_invalid", "plan artifact 的 manifest 摘要无效")
	}
	return nil
}

func isCanonicalSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func validAggregateCounts(values []AggregateCount) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value.Code == "" || value.Count < 0 {
			return false
		}
		if _, exists := seen[value.Code]; exists {
			return false
		}
		seen[value.Code] = struct{}{}
	}
	return true
}

func aggregateCodesEqual(values []AggregateCount, codes []string) bool {
	if values == nil || len(values) != len(codes) || !validAggregateCounts(values) {
		return false
	}
	for index, code := range codes {
		if values[index].Code != code {
			return false
		}
	}
	return true
}

func sumAggregateCounts(values []AggregateCount) int64 {
	var result int64
	for _, value := range values {
		if value.Count > 0 && result > int64(^uint64(0)>>1)-value.Count {
			return -1
		}
		result += value.Count
	}
	return result
}

func cloneAggregateCounts(values []AggregateCount) []AggregateCount {
	if values == nil {
		return nil
	}
	result := make([]AggregateCount, len(values))
	copy(result, values)
	return result
}

func cloneManifestSummary(summary ManifestSummary) ManifestSummary {
	summary.Sections = cloneAggregateCounts(summary.Sections)
	return summary
}

func artifactsEqual(left, right []byte) bool {
	return subtle.ConstantTimeCompare(left, right) == 1
}

// Format 防止意外打印包含数据库绑定信息的 artifact。
func (DatasetArtifact) Format(state fmt.State, _ rune) {
	_, _ = fmt.Fprint(state, "<dataset-artifact:redacted>")
}

// Format 防止意外打印完整 plan artifact；公开输出只使用 ArtifactReceipt。
func (PlanArtifact) Format(state fmt.State, _ rune) {
	_, _ = fmt.Fprint(state, "<plan-artifact:redacted>")
}
