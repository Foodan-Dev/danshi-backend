package legacymigration

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
)

// Command 是严格的单阶段 CLI 请求。inspect 不接受任何审批摘要，plan 必须同时接受
// 外部 dataset 和 manifest 摘要；因此一个 inspect 进程不能顺带批准并生成 plan。
type Command struct {
	Mode                   Mode
	DatasetArtifactPath    string
	ManifestPath           string
	PlanArtifactPath       string
	ApprovalReceiptPath    string
	ApprovalPublicKeyPath  string
	ExpectedDatasetDigest  ArtifactDigest
	ExpectedManifestDigest ManifestDigest
}

// ParseCommand 解析固定的两阶段命令；错误不会回显路径、摘要或未知参数。
func ParseCommand(args []string) (Command, error) {
	if len(args) == 0 {
		return Command{}, gateError("invalid_cli_arguments", "必须指定 inspect 或 plan；当前阶段不提供 apply")
	}
	mode := Mode(args[0])
	if mode != ModeInspect && mode != ModePlan {
		return Command{}, gateError("unsupported_mode", "只支持 inspect 或 plan；当前阶段不提供 apply")
	}
	if len(args[1:])%2 != 0 {
		return Command{}, gateError("invalid_cli_arguments", "CLI 参数必须使用固定 flag/value 对")
	}
	values := make(map[string]string, len(args[1:])/2)
	for index := 1; index < len(args); index += 2 {
		flag := args[index]
		if _, duplicate := values[flag]; duplicate {
			return Command{}, gateError("duplicate_cli_argument", "CLI 参数不得重复")
		}
		values[flag] = args[index+1]
	}
	if mode == ModeInspect {
		if len(values) != 1 || values["--dataset-artifact"] == "" {
			return Command{}, gateError("inspect_arguments_invalid", "inspect 必须且只能指定 --dataset-artifact")
		}
		return Command{Mode: mode, DatasetArtifactPath: values["--dataset-artifact"]}, nil
	}

	required := []string{
		"--dataset-artifact",
		"--manifest",
		"--expected-dataset-sha256",
		"--expected-manifest-sha256",
		"--plan-artifact",
		"--approval-receipt",
		"--approval-public-key",
	}
	if len(values) != len(required) {
		return Command{}, gateError("plan_arguments_invalid", "plan 缺少固定审批参数或包含未知参数")
	}
	for _, flag := range required {
		if values[flag] == "" {
			return Command{}, gateError("plan_arguments_invalid", "plan 的固定审批参数不能为空")
		}
	}
	datasetDigest, err := ParseArtifactDigest(values["--expected-dataset-sha256"], "dataset_digest")
	if err != nil {
		return Command{}, err
	}
	manifestDigest, err := ParseManifestDigest(values["--expected-manifest-sha256"])
	if err != nil {
		return Command{}, err
	}
	return Command{
		Mode:                   mode,
		DatasetArtifactPath:    values["--dataset-artifact"],
		ManifestPath:           values["--manifest"],
		PlanArtifactPath:       values["--plan-artifact"],
		ApprovalReceiptPath:    values["--approval-receipt"],
		ApprovalPublicKeyPath:  values["--approval-public-key"],
		ExpectedDatasetDigest:  datasetDigest,
		ExpectedManifestDigest: manifestDigest,
	}, nil
}

// Execute 对已打开的数据库执行单个只读阶段，并安全写入一个不可覆盖的 canonical artifact。
func Execute(ctx context.Context, source, target *sql.DB, command Command) (ExecutionReport, error) {
	switch command.Mode {
	case ModeInspect:
		return executeInspect(ctx, source, target, command)
	case ModePlan:
		return executePlan(ctx, source, target, command)
	default:
		return ExecutionReport{}, gateError("unsupported_mode", "只支持 inspect 或 plan；当前阶段不提供 apply")
	}
}

func executeInspect(
	ctx context.Context,
	source, target *sql.DB,
	command Command,
) (ExecutionReport, error) {
	if command.DatasetArtifactPath == "" || command.ManifestPath != "" || command.PlanArtifactPath != "" ||
		command.ApprovalReceiptPath != "" || command.ApprovalPublicKeyPath != "" ||
		command.ExpectedDatasetDigest != (ArtifactDigest{}) || command.ExpectedManifestDigest != (ManifestDigest{}) {
		return ExecutionReport{}, gateError("inspect_arguments_invalid", "inspect 只能写出 dataset artifact，不接受审批输入")
	}
	observation, err := inspectMigrationDatabases(ctx, source, target, ModeInspect)
	if err != nil {
		return ExecutionReport{}, err
	}
	artifact := buildDatasetArtifact(observation)
	data, digest, err := canonicalArtifactBytes(artifact)
	if err != nil {
		return ExecutionReport{}, err
	}
	if err := writeExecutionArtifactFile(command.DatasetArtifactPath, data); err != nil {
		return ExecutionReport{}, err
	}
	return ExecutionReport{
		SchemaVersion: ReportSchemaVersion,
		Status:        "ok",
		Mode:          ModeInspect,
		ApplyEnabled:  false,
		Inspection:    observation.report,
		DatasetArtifact: ArtifactReceipt{
			ArtifactType: datasetArtifactType, SchemaVersion: DatasetArtifactSchemaVersion, SHA256: digest.String(),
		},
	}, nil
}

func executePlan(
	ctx context.Context,
	source, target *sql.DB,
	command Command,
) (ExecutionReport, error) {
	if command.DatasetArtifactPath == "" || command.ManifestPath == "" || command.PlanArtifactPath == "" ||
		command.ApprovalReceiptPath == "" || command.ApprovalPublicKeyPath == "" ||
		command.ExpectedDatasetDigest == (ArtifactDigest{}) || command.ExpectedManifestDigest == (ManifestDigest{}) {
		return ExecutionReport{}, gateError("plan_approval_inputs_required", "plan 必须接收外部获批的 dataset 与 manifest SHA-256")
	}
	if sameCleanPath(command.PlanArtifactPath, command.DatasetArtifactPath) ||
		sameCleanPath(command.PlanArtifactPath, command.ManifestPath) ||
		sameCleanPath(command.PlanArtifactPath, command.ApprovalReceiptPath) ||
		sameCleanPath(command.PlanArtifactPath, command.ApprovalPublicKeyPath) {
		return ExecutionReport{}, gateError("plan_output_conflicts_with_input", "plan artifact 输出不得覆盖审批输入")
	}
	approvedDataset, approvedDatasetBytes, err := loadDatasetArtifact(
		command.DatasetArtifactPath,
		command.ExpectedDatasetDigest,
	)
	if err != nil {
		return ExecutionReport{}, err
	}
	approval, err := verifyPlanApprovalReceipt(
		command.ApprovalReceiptPath,
		command.ApprovalPublicKeyPath,
		approvedDataset,
		command.ExpectedDatasetDigest,
		command.ExpectedManifestDigest,
	)
	if err != nil {
		return ExecutionReport{}, err
	}
	return executePlanWithVerifiedApproval(
		ctx, source, target, command, approvedDataset, approvedDatasetBytes, approval,
	)
}

func executePlanWithVerifiedApproval(
	ctx context.Context,
	source, target *sql.DB,
	command Command,
	approvedDataset DatasetArtifact,
	approvedDatasetBytes []byte,
	approval verifiedPlanApproval,
) (ExecutionReport, error) {
	approvedManifest, err := LoadManifest(command.ManifestPath, command.ExpectedManifestDigest)
	if err != nil {
		return ExecutionReport{}, err
	}
	manifestSummary, err := approvedManifest.Summary(command.ExpectedManifestDigest)
	if err != nil {
		return ExecutionReport{}, err
	}

	observation, err := inspectMigrationDatabases(ctx, source, target, ModePlan)
	if err != nil {
		return ExecutionReport{}, err
	}
	liveDataset := buildDatasetArtifact(observation)
	liveDatasetBytes, _, err := canonicalArtifactBytes(liveDataset)
	if err != nil {
		return ExecutionReport{}, err
	}
	if !artifactsEqual(liveDatasetBytes, approvedDatasetBytes) ||
		!sameDatasetBindings(approvedDataset, liveDataset) {
		return ExecutionReport{}, gateError("dataset_snapshot_changed", "当前数据库状态与外部获批 dataset artifact 不一致")
	}

	planArtifact := buildPlanArtifact(
		observation,
		command.ExpectedDatasetDigest,
		command.ExpectedManifestDigest,
		manifestSummary,
		approval,
	)
	planBytes, planDigest, err := canonicalArtifactBytes(planArtifact)
	if err != nil {
		return ExecutionReport{}, err
	}
	if err := validatePlanArtifact(planArtifact); err != nil {
		return ExecutionReport{}, err
	}
	if err := writeExecutionArtifactFile(command.PlanArtifactPath, planBytes); err != nil {
		return ExecutionReport{}, err
	}
	planReceipt := ArtifactReceipt{
		ArtifactType: planArtifactType, SchemaVersion: PlanArtifactSchemaVersion, SHA256: planDigest.String(),
	}
	return ExecutionReport{
		SchemaVersion: ReportSchemaVersion,
		Status:        "ok",
		Mode:          ModePlan,
		ApplyEnabled:  false,
		Inspection:    observation.report,
		DatasetArtifact: ArtifactReceipt{
			ArtifactType:  datasetArtifactType,
			SchemaVersion: DatasetArtifactSchemaVersion,
			SHA256:        command.ExpectedDatasetDigest.String(),
		},
		PlanArtifact: &planReceipt,
	}, nil
}

func sameCleanPath(left, right string) bool {
	leftAbsolute, leftErr := filepath.Abs(left)
	rightAbsolute, rightErr := filepath.Abs(right)
	return leftErr == nil && rightErr == nil && filepath.Clean(leftAbsolute) == filepath.Clean(rightAbsolute)
}

func sameDatasetBindings(left, right DatasetArtifact) bool {
	return left.SourceDatabaseIdentitySHA256 == right.SourceDatabaseIdentitySHA256 &&
		left.SourceSnapshotSHA256 == right.SourceSnapshotSHA256 &&
		left.TargetDatabaseIdentitySHA256 == right.TargetDatabaseIdentitySHA256 &&
		left.TargetSchemaFingerprint == right.TargetSchemaFingerprint &&
		left.TargetGooseVersion == right.TargetGooseVersion
}

// FutureApplyInputs 固定未来 apply 必须由外部传入的三个审批摘要和三个受保护文件。
// 当前 v1 plan 在检查三摘要后即因 coverage=false 被永久拒绝，不执行任何写入。
type FutureApplyInputs struct {
	DatasetArtifactPath    string
	ManifestPath           string
	PlanArtifactPath       string
	ExpectedDatasetDigest  ArtifactDigest
	ExpectedManifestDigest ManifestDigest
	ExpectedPlanDigest     ArtifactDigest
}

// ValidateFutureApplyInputs 证明未来 apply 的最低入口不能省略 plan/dataset/manifest 任一摘要，
// 并显式拒绝把当前不可执行 v1 plan 复用为写入入口。它不连接数据库。
func ValidateFutureApplyInputs(inputs FutureApplyInputs) error {
	if inputs.ExpectedPlanDigest == (ArtifactDigest{}) {
		return gateError("plan_digest_required", "未来 apply 必须接收外部获批的 plan SHA-256")
	}
	if inputs.ExpectedDatasetDigest == (ArtifactDigest{}) {
		return gateError("dataset_digest_required", "未来 apply 必须接收外部获批的 dataset SHA-256")
	}
	if inputs.ExpectedManifestDigest == (ManifestDigest{}) {
		return gateError("manifest_digest_required", "未来 apply 必须接收外部获批的 manifest SHA-256")
	}
	plan, err := loadPlanArtifact(inputs.PlanArtifactPath, inputs.ExpectedPlanDigest)
	if err != nil {
		return err
	}
	if !plan.ApplyEnabled || !plan.Plan.Executable || !plan.ManifestCoverageValidated ||
		!plan.SourceSchemaContractValidated || !plan.Plan.FullSourceReviewComplete {
		return gateError("current_plan_not_executable", "当前 v1 plan 未完成 manifest coverage/source schema contract，永远不得进入 apply")
	}
	dataset, _, err := loadDatasetArtifact(inputs.DatasetArtifactPath, inputs.ExpectedDatasetDigest)
	if err != nil {
		return err
	}
	manifest, err := LoadManifest(inputs.ManifestPath, inputs.ExpectedManifestDigest)
	if err != nil {
		return err
	}
	summary, err := manifest.Summary(inputs.ExpectedManifestDigest)
	if err != nil {
		return err
	}
	if plan.DatasetArtifactSHA256 != inputs.ExpectedDatasetDigest.String() ||
		plan.ManifestSHA256 != inputs.ExpectedManifestDigest.String() ||
		!reflect.DeepEqual(plan.ManifestSummary, summary) ||
		plan.SourceDatabaseIdentitySHA256 != dataset.SourceDatabaseIdentitySHA256 ||
		plan.SourceSnapshotSHA256 != dataset.SourceSnapshotSHA256 ||
		plan.TargetDatabaseIdentitySHA256 != dataset.TargetDatabaseIdentitySHA256 ||
		plan.TargetSchemaFingerprint != dataset.TargetSchemaFingerprint ||
		plan.TargetGooseVersion != dataset.TargetGooseVersion {
		return gateError("apply_artifact_binding_mismatch", "未来 apply 的审批 artifact 绑定不一致")
	}
	return gateError("apply_not_implemented", "当前二进制没有任何数据写入能力")
}

// ExecuteFromEnvironment 只从固定数据库环境变量建立连接；artifact 路径和摘要来自已解析命令。
func ExecuteFromEnvironment(
	ctx context.Context,
	getenv func(string) string,
	command Command,
) (ExecutionReport, error) {
	env, err := loadEnvironment(getenv)
	if err != nil {
		return ExecutionReport{}, err
	}
	pair, err := openDatabasePair(ctx, env)
	if err != nil {
		return ExecutionReport{}, err
	}
	defer pair.close()
	return Execute(ctx, pair.source, pair.target, command)
}
