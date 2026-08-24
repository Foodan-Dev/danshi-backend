package legacymigration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestParseCommandEnforcesTwoProcessApprovalBoundary(t *testing.T) {
	inspect, err := ParseCommand([]string{"inspect", "--dataset-artifact", "private-output.json"})
	if err != nil || inspect.Mode != ModeInspect || inspect.DatasetArtifactPath == "" {
		t.Fatalf("合法 inspect 解析失败：%+v %v", inspect, err)
	}
	if _, err := ParseCommand([]string{"inspect", "--dataset-artifact", "out", "--manifest", "secret"}); err == nil || err.Error() != "inspect_arguments_invalid" {
		t.Fatalf("inspect 不得接收 manifest：%v", err)
	}
	if _, err := ParseCommand([]string{"plan", "--dataset-artifact", "dataset"}); err == nil || err.Error() != "plan_arguments_invalid" {
		t.Fatalf("plan 缺少外部摘要必须失败：%v", err)
	}

	dataset := ArtifactDigest(sha256.Sum256([]byte("approved-dataset")))
	manifest := ManifestDigest(sha256.Sum256([]byte("approved-manifest")))
	plan, err := ParseCommand([]string{
		"plan",
		"--dataset-artifact", "dataset.json",
		"--manifest", "manifest.json",
		"--expected-dataset-sha256", dataset.String(),
		"--expected-manifest-sha256", manifest.String(),
		"--plan-artifact", "plan.json",
		"--approval-receipt", "approval.json",
		"--approval-public-key", "/etc/danshi-migration/approval.ed25519.pub",
	})
	if err != nil {
		t.Fatalf("合法 plan 解析失败：%v", err)
	}
	if !plan.ExpectedDatasetDigest.equal(dataset) || !plan.ExpectedManifestDigest.equal(manifest) {
		t.Fatal("plan 没有保留外部获批摘要")
	}
}

func TestParseCommandErrorsNeverEchoArguments(t *testing.T) {
	secret := "postgres://user:password@private.invalid/database"
	_, err := ParseCommand([]string{"plan", "--unknown", secret})
	if err == nil {
		t.Fatal("未知参数必须失败")
	}
	encoded, marshalErr := json.Marshal(ErrorReport(err))
	if marshalErr != nil {
		t.Fatalf("json.Marshal: %v", marshalErr)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "password") {
		t.Fatalf("CLI 错误泄露了参数：%s", encoded)
	}
}

func TestExecutionArtifactFileIsExclusiveAndOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact.json")
	data := []byte(`{"schema_version":1}`)
	if err := writeExecutionArtifactFile(path, data); err != nil {
		t.Fatalf("writeExecutionArtifactFile: %v", err)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stat.Mode().Perm() != 0o600 {
		t.Fatalf("artifact mode=%#o, want 0600", stat.Mode().Perm())
	}
	if err := writeExecutionArtifactFile(path, []byte("replacement")); err == nil || err.Error() != "artifact_output_exists" {
		t.Fatalf("artifact 禁止覆盖，实际 %v", err)
	}
	loaded, err := readExecutionArtifactFile(path)
	if err != nil {
		t.Fatalf("readExecutionArtifactFile: %v", err)
	}
	if string(loaded) != string(data) {
		t.Fatalf("artifact 被覆盖：%q", loaded)
	}
}

func TestExecutionArtifactFileRejectsUnsafeTypesAndPermissions(t *testing.T) {
	root := t.TempDir()
	realPath := filepath.Join(root, "real.json")
	if err := os.WriteFile(realPath, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	linkPath := filepath.Join(root, "link.json")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if _, err := readExecutionArtifactFile(linkPath); err == nil || err.Error() != "artifact_not_regular" {
		t.Fatalf("symlink artifact 必须失败：%v", err)
	}
	if err := os.Chmod(realPath, 0o640); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if _, err := readExecutionArtifactFile(realPath); err == nil || err.Error() != "artifact_permissions_too_open" {
		t.Fatalf("过宽 artifact 权限必须失败：%v", err)
	}

	fifoPath := filepath.Join(root, "artifact.fifo")
	if err := unix.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := readExecutionArtifactFile(fifoPath)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || err.Error() != "artifact_not_regular" {
			t.Fatalf("FIFO artifact 必须失败：%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO artifact 在类型门禁前阻塞")
	}
}

func TestCanonicalArtifactRejectsEquivalentButNonCanonicalJSON(t *testing.T) {
	artifact := sampleDatasetArtifact()
	canonical, _, err := canonicalArtifactBytes(artifact)
	if err != nil {
		t.Fatalf("canonicalArtifactBytes: %v", err)
	}
	var decoded DatasetArtifact
	if err := decodeCanonicalArtifact(canonical, &decoded); err != nil {
		t.Fatalf("canonical artifact 被拒绝：%v", err)
	}
	withNewline := append(append([]byte(nil), canonical...), '\n')
	if err := decodeCanonicalArtifact(withNewline, &decoded); err == nil || err.Error() != "artifact_not_canonical" {
		t.Fatalf("带空白 artifact 必须拒绝：%v", err)
	}
	duplicate := []byte(`{"schema_version":1,"schema_version":1}`)
	if err := decodeCanonicalArtifact(duplicate, &decoded); err == nil || err.Error() != "artifact_invalid_json" {
		t.Fatalf("重复 key artifact 必须拒绝：%v", err)
	}
}

func TestArtifactFormattingAndFutureApplyInputsAreRedactedAndClosed(t *testing.T) {
	formatted := fmt.Sprintf("%+v / %#v", sampleDatasetArtifact(), PlanArtifact{})
	if strings.Contains(formatted, expectedTargetSchemaFingerprint) ||
		!strings.Contains(formatted, "<dataset-artifact:redacted>") ||
		!strings.Contains(formatted, "<plan-artifact:redacted>") {
		t.Fatalf("artifact fmt 未脱敏：%s", formatted)
	}
	if err := ValidateFutureApplyInputs(FutureApplyInputs{}); err == nil || err.Error() != "plan_digest_required" {
		t.Fatalf("未来 apply 缺少 plan digest 应拒绝：%v", err)
	}
	planDigest := ArtifactDigest(sha256.Sum256([]byte("plan")))
	if err := ValidateFutureApplyInputs(FutureApplyInputs{ExpectedPlanDigest: planDigest}); err == nil || err.Error() != "dataset_digest_required" {
		t.Fatalf("未来 apply 缺少 dataset digest 应拒绝：%v", err)
	}
	datasetDigest := ArtifactDigest(sha256.Sum256([]byte("dataset")))
	if err := ValidateFutureApplyInputs(FutureApplyInputs{
		ExpectedPlanDigest: planDigest, ExpectedDatasetDigest: datasetDigest,
	}); err == nil || err.Error() != "manifest_digest_required" {
		t.Fatalf("未来 apply 缺少 manifest digest 应拒绝：%v", err)
	}
}

func TestSignedPlanApprovalRejectsTamperingExpiryAndUntrustedKeyPath(t *testing.T) {
	dataset := sampleDatasetArtifact()
	datasetDigest := ArtifactDigest(sha256.Sum256([]byte("dataset-artifact")))
	manifestDigest := ManifestDigest(sha256.Sum256([]byte("manifest")))
	path, publicKey, _ := writeSignedPlanApproval(t, dataset, datasetDigest, manifestDigest)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile receipt: %v", err)
	}
	var receipt signedPlanApproval
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatalf("Unmarshal receipt: %v", err)
	}
	receipt.Signature = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	tamperedBytes, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("Marshal tampered receipt: %v", err)
	}
	tamperedPath := filepath.Join(t.TempDir(), "tampered.json")
	if err := writeExecutionArtifactFile(tamperedPath, tamperedBytes); err != nil {
		t.Fatalf("write tampered receipt: %v", err)
	}
	if _, err := verifyPlanApprovalReceiptWithKey(
		tamperedPath, publicKey, dataset, datasetDigest, manifestDigest, time.Now().UTC(),
	); err == nil || err.Error() != "approval_signature_invalid" {
		t.Fatalf("被篡改签名必须拒绝：%v", err)
	}

	if _, err := verifyPlanApprovalReceiptWithKey(
		path, publicKey, dataset, datasetDigest, manifestDigest, time.Now().Add(2*time.Hour),
	); err == nil || err.Error() != "approval_receipt_expired" {
		t.Fatalf("过期 receipt 必须拒绝：%v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	tests := []struct {
		name      string
		issuedAt  time.Time
		expiresAt time.Time
		code      string
	}{
		{
			name: "future issued", issuedAt: now.Add(approvalClockSkew + time.Second),
			expiresAt: now.Add(time.Hour), code: "approval_receipt_not_yet_valid",
		},
		{
			name: "lifetime too long", issuedAt: now,
			expiresAt: now.Add(maxApprovalLifetime + time.Second), code: "approval_receipt_lifetime_invalid",
		},
		{
			name: "old delayed activation", issuedAt: now.Add(-365 * 24 * time.Hour),
			expiresAt: now.Add(time.Hour), code: "approval_receipt_lifetime_invalid",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			receiptPath, key := writeSignedPlanApprovalWindow(
				t, dataset, datasetDigest, manifestDigest, test.issuedAt, test.expiresAt,
			)
			if _, err := verifyPlanApprovalReceiptWithKey(
				receiptPath, key, dataset, datasetDigest, manifestDigest, now,
			); err == nil || err.Error() != test.code {
				t.Fatalf("期望 %s，实际 %v", test.code, err)
			}
		})
	}
	skewedPath, skewedKey := writeSignedPlanApprovalWindow(
		t, dataset, datasetDigest, manifestDigest,
		now.Add(approvalClockSkew-time.Second), now.Add(time.Hour),
	)
	if _, err := verifyPlanApprovalReceiptWithKey(
		skewedPath, skewedKey, dataset, datasetDigest, manifestDigest, now,
	); err != nil {
		t.Fatalf("允许时钟偏差内的 receipt 应通过：%v", err)
	}

	keyPath := filepath.Join(t.TempDir(), "approval.pub")
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(publicKey)), 0o600); err != nil {
		t.Fatalf("WriteFile key: %v", err)
	}
	if _, err := readTrustedApprovalPublicKey(keyPath); err == nil ||
		err.Error() != "approval_public_key_parent_unsafe" {
		t.Fatalf("当前用户可写公钥信任根必须拒绝：%v", err)
	}
}

func TestPlanOutputCannotAliasApprovalInput(t *testing.T) {
	digest := ArtifactDigest(sha256.Sum256([]byte("dataset")))
	manifest := ManifestDigest(sha256.Sum256([]byte("manifest")))
	_, err := Execute(context.Background(), nil, nil, Command{
		Mode:                   ModePlan,
		DatasetArtifactPath:    "/safe/dataset.json",
		ManifestPath:           "/safe/manifest.json",
		PlanArtifactPath:       "/safe/../safe/dataset.json",
		ApprovalReceiptPath:    "/safe/approval.json",
		ApprovalPublicKeyPath:  "/etc/danshi-migration/approval.ed25519.pub",
		ExpectedDatasetDigest:  digest,
		ExpectedManifestDigest: manifest,
	})
	if err == nil || err.Error() != "plan_output_conflicts_with_input" {
		t.Fatalf("plan 输出与输入别名必须在访问数据库前失败：%v", err)
	}
}

func sampleDatasetArtifact() DatasetArtifact {
	sha := strings.Repeat("a", sha256.Size*2)
	return DatasetArtifact{
		SchemaVersion:                DatasetArtifactSchemaVersion,
		ArtifactType:                 datasetArtifactType,
		DatasetContentVersion:        sourceDatasetContentVersion,
		InspectionLevel:              "foundation_preflight",
		SourceDatabaseIdentitySHA256: sha,
		SourceSnapshotSHA256:         sha,
		SourcePostgresMajor:          MinimumSourceMajor,
		SourceTableRows:              []AggregateCount{},
		SourceMetrics:                []AggregateCount{},
		SourceBlockers:               []AggregateCount{},
		TargetDatabaseIdentitySHA256: sha,
		TargetPostgresMajor:          ExpectedTargetMajor,
		TargetGooseVersion:           ExpectedGooseVersion,
		TargetSchemaFingerprint:      expectedTargetSchemaFingerprint,
		TargetSeedRows:               []AggregateCount{},
	}
}

func writeSignedPlanApproval(
	t *testing.T,
	dataset DatasetArtifact,
	datasetDigest ArtifactDigest,
	manifestDigest ManifestDigest,
) (string, ed25519.PublicKey, verifiedPlanApproval) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	path, publicKey := writeSignedPlanApprovalWindow(
		t, dataset, datasetDigest, manifestDigest, now, now.Add(time.Hour),
	)
	verified, err := verifyPlanApprovalReceiptWithKey(
		path, publicKey, dataset, datasetDigest, manifestDigest, now,
	)
	if err != nil {
		t.Fatalf("verifyPlanApprovalReceiptWithKey: %v", err)
	}
	return path, publicKey, verified
}

func writeSignedPlanApprovalWindow(
	t *testing.T,
	dataset DatasetArtifact,
	datasetDigest ArtifactDigest,
	manifestDigest ManifestDigest,
	issuedAt, expiresAt time.Time,
) (string, ed25519.PublicKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	payload := planApprovalPayload{
		SchemaVersion:                planApprovalSchemaVersion,
		ArtifactType:                 planApprovalArtifactType,
		ApprovalID:                   "test-approval-0123456789",
		IssuedAtUnix:                 issuedAt.Unix(),
		ExpiresAtUnix:                expiresAt.Unix(),
		DatasetArtifactSHA256:        datasetDigest.String(),
		ManifestSHA256:               manifestDigest.String(),
		DatasetContentVersion:        dataset.DatasetContentVersion,
		SourceDatabaseIdentitySHA256: dataset.SourceDatabaseIdentitySHA256,
		SourceSnapshotSHA256:         dataset.SourceSnapshotSHA256,
		TargetDatabaseIdentitySHA256: dataset.TargetDatabaseIdentitySHA256,
		TargetSchemaFingerprint:      dataset.TargetSchemaFingerprint,
		TargetGooseVersion:           dataset.TargetGooseVersion,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal payload: %v", err)
	}
	signedBytes := append([]byte("danshi-legacy-plan-approval-v1\n"), payloadBytes...)
	receipt := signedPlanApproval{
		Payload: payload, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, signedBytes)),
	}
	receiptBytes, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("json.Marshal receipt: %v", err)
	}
	path := filepath.Join(t.TempDir(), "approval.json")
	if err := writeExecutionArtifactFile(path, receiptBytes); err != nil {
		t.Fatalf("write approval receipt: %v", err)
	}
	return path, publicKey
}
