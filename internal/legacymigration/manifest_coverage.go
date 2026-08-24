package legacymigration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// ManifestEntity 是 coverage key 的固定实体类型。
type ManifestEntity string

const (
	// ManifestEntityUser 表示来源用户 UUID。
	ManifestEntityUser ManifestEntity = "user"
	// ManifestEntityPost 表示来源帖子 UUID。
	ManifestEntityPost ManifestEntity = "post"
	// ManifestEntityComment 表示来源评论 UUID。
	ManifestEntityComment ManifestEntity = "comment"
	// ManifestEntityDictionaryMapping 表示来源词表类别和值。
	ManifestEntityDictionaryMapping ManifestEntity = "dictionary_mapping"
	// ManifestEntityPostImageReference 表示来源帖子 UUID 和原始图片引用。
	ManifestEntityPostImageReference ManifestEntity = "post_image_reference"
	// ManifestEntityDuplicateImageAsset 表示重复组 key 和来源图片资产 UUID。
	ManifestEntityDuplicateImageAsset ManifestEntity = "duplicate_image_asset"
	// ManifestEntityLike 表示来源点赞 UUID。
	ManifestEntityLike ManifestEntity = "like"
	// ManifestEntityNotification 表示来源通知 UUID。
	ManifestEntityNotification ManifestEntity = "notification"
)

// CanonicalManifestKey 是规范化 anomaly identity 的 SHA-256；它不暴露来源值。
type CanonicalManifestKey [sha256.Size]byte

// String 返回固定长度的小写十六进制 key。
func (key CanonicalManifestKey) String() string {
	return hex.EncodeToString(key[:])
}

// ManifestRequirement 是 dataset 发现的一条必须被指定 section 精确覆盖的 anomaly。
type ManifestRequirement struct {
	Section string
	Entity  ManifestEntity
	Key     CanonicalManifestKey
}

// ManifestRequirements 是 dataset adapter 交给 manifest coverage 门禁的 canonical key set。
type ManifestRequirements struct {
	Entries []ManifestRequirement
	Source  ManifestSourceContext
}

// ManifestCoverageSummary 只输出固定 code 的聚合计数，不输出 anomaly key 或来源值。
type ManifestCoverageSummary struct {
	Counts []AggregateCount `json:"counts"`
}

// NewManifestRequirement 规范化标识并创建不含原始值的 requirement。
// UUID entity 接受兼容表示后统一为标准小写带连字符；opaque value 必须已无首尾空白。
func NewManifestRequirement(
	section string,
	entity ManifestEntity,
	identifiers ...string,
) (ManifestRequirement, error) {
	parts, err := canonicalRequirementParts(entity, identifiers)
	if err != nil {
		return ManifestRequirement{}, err
	}
	if !manifestSectionAcceptsEntity(section, entity) {
		return ManifestRequirement{}, gateError("manifest_requirement_category_invalid", "manifest requirement category 不受支持")
	}
	key := hashCanonicalManifestKey(string(entity), parts...)
	return ManifestRequirement{Section: section, Entity: entity, Key: key}, nil
}

// ValidateCoverage 要求 dataset anomaly 和 manifest 决议 exact-set 相等。
// wrong category 不重复计入 missing/unused；返回值只含固定 code 和数量。
func ValidateCoverage(
	manifest Manifest,
	requirements ManifestRequirements,
) (ManifestCoverageSummary, error) {
	normalized, err := cloneAndCanonicalizeManifest(manifest)
	if err != nil {
		return ManifestCoverageSummary{}, err
	}
	actual, err := manifestRequirementEntries(normalized)
	if err != nil {
		return ManifestCoverageSummary{}, err
	}
	summary, duplicateRequirements, duplicateManifest, err := compareCoverage(actual, requirements.Entries)
	if err != nil {
		return ManifestCoverageSummary{}, err
	}
	if duplicateRequirements > 0 || duplicateManifest > 0 {
		return summary, gateError("manifest_coverage_duplicate", "manifest coverage key set 含重复项")
	}
	if err := normalized.validate(); err != nil {
		return summary, err
	}
	if coverageCount(summary, "missing") > 0 ||
		coverageCount(summary, "unused") > 0 ||
		coverageCount(summary, "wrong_category") > 0 {
		return summary, gateError("manifest_coverage_mismatch", "manifest 未精确覆盖 dataset anomaly key set")
	}
	if err := validateManifestAgainstSourceContext(normalized, requirements.Source); err != nil {
		return summary, err
	}
	return summary, nil
}

func hashCanonicalManifestKey(entity string, parts ...string) CanonicalManifestKey {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(entity))
	for _, part := range parts {
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write([]byte(part))
	}
	var key CanonicalManifestKey
	copy(key[:], hasher.Sum(nil))
	return key
}

type manifestRequirementIdentity struct {
	section string
	entity  ManifestEntity
	key     CanonicalManifestKey
}

type manifestEntityIdentity struct {
	entity ManifestEntity
	key    CanonicalManifestKey
}

func compareCoverage(
	actual []ManifestRequirement,
	required []ManifestRequirement,
) (ManifestCoverageSummary, int64, int64, error) {
	requiredSet, duplicateRequirements, err := indexRequirements(required)
	if err != nil {
		return ManifestCoverageSummary{}, 0, 0, err
	}
	actualSet, duplicateManifest, err := indexRequirements(actual)
	if err != nil {
		return ManifestCoverageSummary{}, 0, 0, err
	}
	var matched int64
	unmatchedActual := make(map[manifestEntityIdentity]int64)
	for identity := range actualSet {
		if _, exists := requiredSet[identity]; exists {
			matched++
			continue
		}
		entity := manifestEntityIdentity{entity: identity.entity, key: identity.key}
		unmatchedActual[entity]++
	}
	unmatchedRequired := make(map[manifestEntityIdentity]int64)
	for identity := range requiredSet {
		if _, exists := actualSet[identity]; exists {
			continue
		}
		entity := manifestEntityIdentity{entity: identity.entity, key: identity.key}
		unmatchedRequired[entity]++
	}
	var missing, unused, wrongCategory int64
	for entity, actualCount := range unmatchedActual {
		requiredCount := unmatchedRequired[entity]
		wrong := min(actualCount, requiredCount)
		wrongCategory += wrong
		unused += actualCount - wrong
		unmatchedRequired[entity] -= wrong
	}
	for _, requiredCount := range unmatchedRequired {
		missing += requiredCount
	}
	return newCoverageSummary(
		int64(len(required)), int64(len(actual)), matched, missing, unused, wrongCategory,
		duplicateRequirements, duplicateManifest,
	), duplicateRequirements, duplicateManifest, nil
}

func indexRequirements(entries []ManifestRequirement) (
	map[manifestRequirementIdentity]struct{},
	int64,
	error,
) {
	exact := make(map[manifestRequirementIdentity]struct{}, len(entries))
	var duplicates int64
	for _, entry := range entries {
		if !manifestSectionAcceptsEntity(entry.Section, entry.Entity) || entry.Key == (CanonicalManifestKey{}) {
			return nil, 0, gateError("manifest_requirements_invalid", "manifest requirements 含无效 canonical key")
		}
		identity := manifestRequirementIdentity{section: entry.Section, entity: entry.Entity, key: entry.Key}
		if _, exists := exact[identity]; exists {
			duplicates++
		} else {
			exact[identity] = struct{}{}
		}
	}
	return exact, duplicates, nil
}

func canonicalRequirementParts(entity ManifestEntity, identifiers []string) ([]string, error) {
	switch entity {
	case ManifestEntityUser, ManifestEntityPost, ManifestEntityComment,
		ManifestEntityLike, ManifestEntityNotification:
		if len(identifiers) != 1 {
			return nil, gateError("manifest_requirement_identity_invalid", "manifest requirement identity 数量不正确")
		}
		value := identifiers[0]
		if err := canonicalizeUUID(&value, false); err != nil {
			return nil, err
		}
		return []string{value}, nil
	case ManifestEntityDictionaryMapping:
		if len(identifiers) != 2 || !validDictionary(identifiers[0]) || !isIdentifier(identifiers[1]) {
			return nil, gateError("manifest_requirement_identity_invalid", "manifest requirement 词表 identity 无效")
		}
		return []string{identifiers[0], identifiers[1]}, nil
	case ManifestEntityPostImageReference:
		if len(identifiers) != 2 || !isIdentifier(identifiers[1]) {
			return nil, gateError("manifest_requirement_identity_invalid", "manifest requirement 帖子图片 identity 无效")
		}
		postID := identifiers[0]
		if err := canonicalizeUUID(&postID, false); err != nil {
			return nil, err
		}
		return []string{postID, identifiers[1]}, nil
	case ManifestEntityDuplicateImageAsset:
		if len(identifiers) != 2 || !isIdentifier(identifiers[0]) {
			return nil, gateError("manifest_requirement_identity_invalid", "manifest requirement 重复图片 identity 无效")
		}
		assetID := identifiers[1]
		if err := canonicalizeUUID(&assetID, false); err != nil {
			return nil, err
		}
		return []string{identifiers[0], assetID}, nil
	default:
		return nil, gateError("manifest_requirement_entity_invalid", "manifest requirement entity 不受支持")
	}
}

func manifestSectionAcceptsEntity(section string, entity ManifestEntity) bool {
	switch section {
	case "excluded_users", "email_rewrites", "avatar_resolutions":
		return entity == ManifestEntityUser
	case "excluded_content":
		return entity == ManifestEntityPost || entity == ManifestEntityComment
	case "post_type_resolutions":
		return entity == ManifestEntityPost
	case "dictionary_mappings":
		return entity == ManifestEntityDictionaryMapping
	case "post_image_resolutions":
		return entity == ManifestEntityPostImageReference
	case "duplicate_image_asset_resolutions":
		return entity == ManifestEntityDuplicateImageAsset
	case "comment_reparent_resolutions":
		return entity == ManifestEntityComment
	case "orphan_like_exclusions":
		return entity == ManifestEntityLike
	case "orphan_notification_exclusions":
		return entity == ManifestEntityNotification
	default:
		return false
	}
}

func manifestRequirementEntries(manifest Manifest) ([]ManifestRequirement, error) {
	entries := make([]ManifestRequirement, 0, manifest.Summary().TotalEntries)
	appendEntry := func(section string, entity ManifestEntity, identifiers ...string) error {
		entry, err := NewManifestRequirement(section, entity, identifiers...)
		if err != nil {
			return err
		}
		entries = append(entries, entry)
		return nil
	}
	for _, decision := range manifest.ExcludedUsers {
		if err := appendEntry("excluded_users", ManifestEntityUser, decision.UserID); err != nil {
			return nil, err
		}
	}
	for _, decision := range manifest.ExcludedContent {
		if err := appendEntry("excluded_content", ManifestEntity(decision.ContentType), decision.ContentID); err != nil {
			return nil, err
		}
	}
	if err := appendSimpleManifestRequirements(manifest, appendEntry); err != nil {
		return nil, err
	}
	return entries, nil
}

func appendSimpleManifestRequirements(
	manifest Manifest,
	appendEntry func(string, ManifestEntity, ...string) error,
) error {
	for _, decision := range manifest.EmailRewrites {
		if err := appendEntry("email_rewrites", ManifestEntityUser, decision.UserID); err != nil {
			return err
		}
	}
	for _, decision := range manifest.PostTypeResolutions {
		if err := appendEntry("post_type_resolutions", ManifestEntityPost, decision.PostID); err != nil {
			return err
		}
	}
	for _, decision := range manifest.DictionaryMappings {
		if err := appendEntry(
			"dictionary_mappings", ManifestEntityDictionaryMapping, decision.Dictionary, decision.Source,
		); err != nil {
			return err
		}
	}
	for _, decision := range manifest.PostImageResolutions {
		if err := appendEntry(
			"post_image_resolutions", ManifestEntityPostImageReference, decision.PostID, decision.SourceReference,
		); err != nil {
			return err
		}
	}
	for _, decision := range manifest.AvatarResolutions {
		if err := appendEntry("avatar_resolutions", ManifestEntityUser, decision.UserID); err != nil {
			return err
		}
	}
	return appendRemainingManifestRequirements(manifest, appendEntry)
}

func appendRemainingManifestRequirements(
	manifest Manifest,
	appendEntry func(string, ManifestEntity, ...string) error,
) error {
	for _, decision := range manifest.DuplicateImageAssetResolutions {
		if err := appendEntry(
			"duplicate_image_asset_resolutions", ManifestEntityDuplicateImageAsset,
			decision.GroupKey, decision.ImageAssetID,
		); err != nil {
			return err
		}
	}
	for _, decision := range manifest.CommentReparentResolutions {
		if err := appendEntry("comment_reparent_resolutions", ManifestEntityComment, decision.CommentID); err != nil {
			return err
		}
	}
	for _, decision := range manifest.OrphanLikeExclusions {
		if err := appendEntry("orphan_like_exclusions", ManifestEntityLike, decision.LikeID); err != nil {
			return err
		}
	}
	for _, decision := range manifest.OrphanNotificationExclusions {
		if err := appendEntry("orphan_notification_exclusions", ManifestEntityNotification, decision.NotificationID); err != nil {
			return err
		}
	}
	return nil
}

func cloneAndCanonicalizeManifest(manifest Manifest) (Manifest, error) {
	if !manifest.validated || manifest.SchemaVersion != ManifestSchemaVersion {
		return Manifest{}, gateError("manifest_not_validated", "coverage 只能使用经过严格文件门禁加载的 manifest")
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return Manifest{}, gateError("manifest_internal_clone_failed", "无法建立 manifest coverage 副本")
	}
	var clone Manifest
	if err := json.Unmarshal(data, &clone); err != nil {
		return Manifest{}, gateError("manifest_internal_clone_failed", "无法建立 manifest coverage 副本")
	}
	if err := clone.canonicalize(); err != nil {
		return Manifest{}, err
	}
	clone.validated = true
	return clone, nil
}

func validDictionary(value string) bool {
	return value == "canteen" || value == "cuisine" || value == "flavor"
}

func newCoverageSummary(
	required, actual, matched, missing, unused, wrongCategory, duplicateRequirements, duplicateManifest int64,
) ManifestCoverageSummary {
	return ManifestCoverageSummary{Counts: []AggregateCount{
		{Code: "requirements", Count: required},
		{Code: "manifest_decisions", Count: actual},
		{Code: "matched", Count: matched},
		{Code: "missing", Count: missing},
		{Code: "unused", Count: unused},
		{Code: "wrong_category", Count: wrongCategory},
		{Code: "duplicate_requirements", Count: duplicateRequirements},
		{Code: "duplicate_manifest_decisions", Count: duplicateManifest},
	}}
}

func coverageCount(summary ManifestCoverageSummary, code string) int64 {
	for _, count := range summary.Counts {
		if count.Code == code {
			return count.Count
		}
	}
	return 0
}
