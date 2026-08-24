package legacymigration

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	planApprovalSchemaVersion = 1
	planApprovalArtifactType  = "danshi_legacy_plan_approval"
	maxApprovalLifetime       = 7 * 24 * time.Hour
	approvalClockSkew         = 5 * time.Minute
)

type planApprovalPayload struct {
	SchemaVersion                int    `json:"schema_version"`
	ArtifactType                 string `json:"artifact_type"`
	ApprovalID                   string `json:"approval_id"`
	IssuedAtUnix                 int64  `json:"issued_at_unix"`
	ExpiresAtUnix                int64  `json:"expires_at_unix"`
	DatasetArtifactSHA256        string `json:"dataset_artifact_sha256"`
	ManifestSHA256               string `json:"manifest_sha256"`
	DatasetContentVersion        int    `json:"dataset_content_version"`
	SourceDatabaseIdentitySHA256 string `json:"source_database_identity_sha256"`
	SourceSnapshotSHA256         string `json:"source_snapshot_sha256"`
	TargetDatabaseIdentitySHA256 string `json:"target_database_identity_sha256"`
	TargetSchemaFingerprint      string `json:"target_schema_fingerprint"`
	TargetGooseVersion           int64  `json:"target_goose_version"`
}

type signedPlanApproval struct {
	Payload   planApprovalPayload `json:"payload"`
	Signature string              `json:"signature"`
}

type verifiedPlanApproval struct {
	receiptSHA256   string
	publicKeySHA256 string
}

func verifyPlanApprovalReceipt(
	receiptPath, publicKeyPath string,
	dataset DatasetArtifact,
	datasetDigest ArtifactDigest,
	manifestDigest ManifestDigest,
) (verifiedPlanApproval, error) {
	publicKey, err := readTrustedApprovalPublicKey(publicKeyPath)
	if err != nil {
		return verifiedPlanApproval{}, err
	}
	return verifyPlanApprovalReceiptWithKey(
		receiptPath, publicKey, dataset, datasetDigest, manifestDigest, time.Now().UTC(),
	)
}

func verifyPlanApprovalReceiptWithKey(
	receiptPath string,
	publicKey ed25519.PublicKey,
	dataset DatasetArtifact,
	datasetDigest ArtifactDigest,
	manifestDigest ManifestDigest,
	now time.Time,
) (verifiedPlanApproval, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return verifiedPlanApproval{}, gateError("approval_public_key_invalid", "审批公钥不是有效 Ed25519 公钥")
	}
	data, err := readExecutionArtifactFile(receiptPath)
	if err != nil {
		return verifiedPlanApproval{}, err
	}
	var receipt signedPlanApproval
	if err := decodeCanonicalArtifact(data, &receipt); err != nil {
		return verifiedPlanApproval{}, err
	}
	payload := receipt.Payload
	if payload.SchemaVersion != planApprovalSchemaVersion || payload.ArtifactType != planApprovalArtifactType ||
		!validApprovalID(payload.ApprovalID) {
		return verifiedPlanApproval{}, gateError("approval_receipt_contract_invalid", "审批 receipt 版本、类型或 ID 无效")
	}
	issuedAt := time.Unix(payload.IssuedAtUnix, 0).UTC()
	expiresAt := time.Unix(payload.ExpiresAtUnix, 0).UTC()
	if issuedAt.After(now.Add(approvalClockSkew)) {
		return verifiedPlanApproval{}, gateError("approval_receipt_not_yet_valid", "审批 receipt 签发时间超出允许时钟偏差")
	}
	if !expiresAt.After(issuedAt) || expiresAt.Sub(issuedAt) > maxApprovalLifetime {
		return verifiedPlanApproval{}, gateError("approval_receipt_lifetime_invalid", "审批 receipt 签发有效期无效")
	}
	if !expiresAt.After(now) {
		return verifiedPlanApproval{}, gateError("approval_receipt_expired", "审批 receipt 已过期")
	}
	if payload.DatasetArtifactSHA256 != datasetDigest.String() ||
		payload.ManifestSHA256 != manifestDigest.String() ||
		payload.DatasetContentVersion != dataset.DatasetContentVersion ||
		payload.SourceDatabaseIdentitySHA256 != dataset.SourceDatabaseIdentitySHA256 ||
		payload.SourceSnapshotSHA256 != dataset.SourceSnapshotSHA256 ||
		payload.TargetDatabaseIdentitySHA256 != dataset.TargetDatabaseIdentitySHA256 ||
		payload.TargetSchemaFingerprint != dataset.TargetSchemaFingerprint ||
		payload.TargetGooseVersion != dataset.TargetGooseVersion {
		return verifiedPlanApproval{}, gateError("approval_receipt_binding_mismatch", "审批 receipt 与 dataset/manifest 绑定不一致")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(receipt.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return verifiedPlanApproval{}, gateError("approval_signature_invalid", "审批 receipt 签名格式无效")
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return verifiedPlanApproval{}, gateError("approval_receipt_encode_failed", "无法规范化审批 receipt")
	}
	signedBytes := append([]byte("danshi-legacy-plan-approval-v1\n"), payloadBytes...)
	if !ed25519.Verify(publicKey, signedBytes, signature) {
		return verifiedPlanApproval{}, gateError("approval_signature_invalid", "审批 receipt 签名验证失败")
	}
	receiptDigest := sha256.Sum256(data)
	keyDigest := sha256.Sum256(publicKey)
	return verifiedPlanApproval{
		receiptSHA256:   hex.EncodeToString(receiptDigest[:]),
		publicKeySHA256: hex.EncodeToString(keyDigest[:]),
	}, nil
}

func validApprovalID(value string) bool {
	if len(value) < 16 || len(value) > 128 {
		return false
	}
	for _, current := range value {
		if (current >= 'a' && current <= 'z') || (current >= 'A' && current <= 'Z') ||
			(current >= '0' && current <= '9') || current == '-' || current == '_' {
			continue
		}
		return false
	}
	return true
}

func readTrustedApprovalPublicKey(path string) (ed25519.PublicKey, error) {
	if os.Geteuid() == 0 {
		return nil, gateError("approval_executor_must_be_unprivileged", "plan 必须由非 root 迁移执行用户运行")
	}
	if strings.TrimSpace(path) == "" {
		return nil, gateError("approval_public_key_path_missing", "必须提供 root 管理的审批公钥路径")
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, gateError("approval_public_key_path_invalid", "无法解析审批公钥路径")
	}
	parentFD, err := openDirectoryWithoutSymlinks(filepath.Dir(absolutePath))
	if err != nil {
		return nil, gateError("approval_public_key_parent_unsafe", "无法安全打开审批公钥父目录")
	}
	defer func() { _ = unix.Close(parentFD) }()
	var parentStat unix.Stat_t
	if err := unix.Fstat(parentFD, &parentStat); err != nil ||
		parentStat.Mode&unix.S_IFMT != unix.S_IFDIR || parentStat.Uid != 0 || parentStat.Mode&0o022 != 0 {
		return nil, gateError("approval_public_key_parent_unsafe", "审批公钥父目录必须由 root 持有且不可被非 root 写入")
	}
	fileFD, err := unix.Openat(parentFD, filepath.Base(absolutePath),
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, gateError("approval_public_key_unsafe", "审批公钥不得是符号链接")
		}
		return nil, gateError("approval_public_key_open_failed", "无法安全打开审批公钥")
	}
	file := os.NewFile(uintptr(fileFD), "trusted-approval-public-key")
	if file == nil {
		_ = unix.Close(fileFD)
		return nil, gateError("approval_public_key_open_failed", "无法安全打开审批公钥")
	}
	defer func() { _ = file.Close() }()
	var before unix.Stat_t
	if err := unix.Fstat(fileFD, &before); err != nil || before.Mode&unix.S_IFMT != unix.S_IFREG ||
		before.Uid != 0 || before.Mode&0o022 != 0 || before.Size <= 0 || before.Size > 1024 {
		return nil, gateError("approval_public_key_unsafe", "审批公钥必须由 root 持有且不可被非 root 写入")
	}
	data, err := io.ReadAll(io.LimitReader(file, 1025))
	if err != nil || len(data) > 1024 {
		return nil, gateError("approval_public_key_read_failed", "无法读取审批公钥")
	}
	var after unix.Stat_t
	if err := unix.Fstat(fileFD, &after); err != nil || manifestStatChanged(before, after) ||
		after.Size != int64(len(data)) {
		return nil, gateError("approval_public_key_changed", "审批公钥在加载期间发生变化")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(string(bytes.TrimSpace(data)))
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, gateError("approval_public_key_invalid", "审批公钥格式无效")
	}
	return ed25519.PublicKey(append([]byte(nil), decoded...)), nil
}
