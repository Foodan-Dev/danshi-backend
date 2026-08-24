package legacymigration

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// ApprovedManifest 将获批文件摘要与已验证决议封装为不可从包外变异的对象。
type ApprovedManifest struct {
	data       manifestData
	fileDigest ManifestDigest
	seal       ManifestDigest
}

// ParseManifestDigest 解析独立审批渠道提供的 64 位十六进制 SHA-256。
func ParseManifestDigest(value string) (ManifestDigest, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return ManifestDigest{}, gateError("manifest_digest_invalid", "获批 manifest SHA-256 格式无效")
	}
	var digest ManifestDigest
	copy(digest[:], decoded)
	if digest == (ManifestDigest{}) {
		return ManifestDigest{}, gateError("manifest_digest_required", "获批 manifest SHA-256 不能为空")
	}
	return digest, nil
}

// Summary 在再次绑定 expected digest 并复验内部 seal 后返回脱敏聚合摘要。
func (approved ApprovedManifest) Summary(expected ManifestDigest) (ManifestSummary, error) {
	manifest, err := approved.verify(expected)
	if err != nil {
		return ManifestSummary{}, err
	}
	return manifest.Summary(), nil
}

// Format 阻止 fmt 系列函数输出私有决议、canonical payload 或内部 seal。
func (ApprovedManifest) Format(state fmt.State, _ rune) {
	_, _ = fmt.Fprint(state, "<approved-manifest:redacted>")
}

// MarshalJSON 阻止 JSON 序列化输出私有决议。
func (ApprovedManifest) MarshalJSON() ([]byte, error) {
	return []byte(`{"type":"approved_manifest","redacted":true}`), nil
}

func newApprovedManifest(data manifestData, digest ManifestDigest) (ApprovedManifest, error) {
	seal, err := sealManifestData(data, digest)
	if err != nil {
		return ApprovedManifest{}, err
	}
	return ApprovedManifest{data: data, fileDigest: digest, seal: seal}, nil
}

func (approved ApprovedManifest) verify(expected ManifestDigest) (manifestData, error) {
	if expected == (ManifestDigest{}) {
		return manifestData{}, gateError("manifest_digest_required", "必须提供获批 manifest SHA-256")
	}
	if !approved.fileDigest.equal(expected) {
		return manifestData{}, gateError("manifest_digest_mismatch", "ApprovedManifest 与 expected SHA-256 不一致")
	}
	seal, err := sealManifestData(approved.data, approved.fileDigest)
	if err != nil {
		return manifestData{}, err
	}
	if !seal.equal(approved.seal) {
		return manifestData{}, gateError("approved_manifest_tampered", "ApprovedManifest 决议在获批后发生变异")
	}
	clone, err := cloneManifestData(approved.data)
	if err != nil {
		return manifestData{}, err
	}
	if err := clone.validate(); err != nil {
		return manifestData{}, gateError("approved_manifest_tampered", "ApprovedManifest 决议不再满足获批约束")
	}
	return clone, nil
}

func sealManifestData(data manifestData, digest ManifestDigest) (ManifestDigest, error) {
	canonical, err := json.Marshal(data)
	if err != nil {
		return ManifestDigest{}, gateError("manifest_seal_failed", "无法封装 ApprovedManifest")
	}
	hasher := sha256.New()
	_, _ = hasher.Write([]byte("danshi-approved-manifest-v1"))
	_, _ = hasher.Write(digest[:])
	_, _ = hasher.Write(canonical)
	var seal ManifestDigest
	copy(seal[:], hasher.Sum(nil))
	return seal, nil
}

func cloneManifestData(data manifestData) (manifestData, error) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return manifestData{}, gateError("manifest_internal_clone_failed", "无法建立 ApprovedManifest 防御性副本")
	}
	var clone manifestData
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return manifestData{}, gateError("manifest_internal_clone_failed", "无法建立 ApprovedManifest 防御性副本")
	}
	return clone, nil
}

func (digest ManifestDigest) equal(other ManifestDigest) bool {
	return subtle.ConstantTimeCompare(digest[:], other[:]) == 1
}
