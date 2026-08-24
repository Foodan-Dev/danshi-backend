package legacymigration

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// ManifestEntity 是 anomaly identity 的固定实体类型。
type ManifestEntity string

const (
	// ManifestEntityUser 表示来源用户。
	ManifestEntityUser ManifestEntity = "user"
	// ManifestEntityPost 表示来源帖子。
	ManifestEntityPost ManifestEntity = "post"
	// ManifestEntityComment 表示来源评论。
	ManifestEntityComment ManifestEntity = "comment"
	// ManifestEntityDictionaryMapping 表示来源词表项。
	ManifestEntityDictionaryMapping ManifestEntity = "dictionary_mapping"
	// ManifestEntityPostImageReference 表示来源帖子图片引用。
	ManifestEntityPostImageReference ManifestEntity = "post_image_reference"
	// ManifestEntityDuplicateImageAsset 表示来源重复图片资产。
	ManifestEntityDuplicateImageAsset ManifestEntity = "duplicate_image_asset"
	// ManifestEntityLike 表示来源点赞。
	ManifestEntityLike ManifestEntity = "like"
	// ManifestEntityNotification 表示来源通知。
	ManifestEntityNotification ManifestEntity = "notification"
)

// CanonicalManifestKey 是不暴露来源值的 SHA-256 identity。
type CanonicalManifestKey [sha256.Size]byte

func (key CanonicalManifestKey) String() string { return hex.EncodeToString(key[:]) }

// ManifestDecisionOption 描述一种获批的 section/action 解决方式；字段私有以防调用层改写。
type ManifestDecisionOption struct {
	category string
	action   string
}

// NewManifestDecisionOption 创建固定 section/action 决议选项。
func NewManifestDecisionOption(category, action string) (ManifestDecisionOption, error) {
	if !manifestCategoryAcceptsAction(category, action) {
		return ManifestDecisionOption{}, gateError("manifest_requirement_category_invalid", "manifest requirement 决议类别或 action 不受支持")
	}
	return ManifestDecisionOption{category: category, action: action}, nil
}

// ManifestRequirement 是一条 anomaly identity 及其允许的替代决议集合。
type ManifestRequirement struct {
	anomaly   CanonicalManifestKey
	entity    ManifestEntity
	entityKey CanonicalManifestKey
	allowed   []ManifestDecisionOption
}

// ManifestRequirements 只能由完整 dataset adapter 构建并封装。
type ManifestRequirements struct {
	entries []ManifestRequirement
	source  manifestSourceContext
	seal    ManifestDigest
}

// ManifestCoverageSummary 只输出固定 code 和聚合计数。
type ManifestCoverageSummary struct {
	Counts []AggregateCount `json:"counts"`
}

// NewManifestRequirement 构建 anomaly identity；anomalyCode 必须来自 dataset agent，不能由 manifest 反推。
func NewManifestRequirement(anomalyCode string, entity ManifestEntity, identifiers []string, allowed ...ManifestDecisionOption) (ManifestRequirement, error) {
	if !isIdentifier(anomalyCode) || len(allowed) == 0 {
		return ManifestRequirement{}, gateError("manifest_requirement_identity_invalid", "manifest requirement anomaly 或 allowed 为空")
	}
	parts, err := canonicalRequirementParts(entity, identifiers)
	if err != nil {
		return ManifestRequirement{}, err
	}
	seen := make(map[ManifestDecisionOption]struct{}, len(allowed))
	copyAllowed := make([]ManifestDecisionOption, 0, len(allowed))
	for _, option := range allowed {
		if !manifestSectionAcceptsEntity(option.category, entity) || !manifestCategoryAcceptsAction(option.category, option.action) {
			return ManifestRequirement{}, gateError("manifest_requirement_category_invalid", "manifest requirement 决议与实体不兼容")
		}
		if _, duplicate := seen[option]; duplicate {
			return ManifestRequirement{}, gateError("manifest_requirements_duplicate", "manifest requirement allowed 决议重复")
		}
		seen[option] = struct{}{}
		copyAllowed = append(copyAllowed, option)
	}
	entityKey := hashCanonicalManifestKey(string(entity), parts...)
	return ManifestRequirement{
		anomaly: hashCanonicalManifestKey("anomaly", anomalyCode, string(entity), entityKey.String()),
		entity:  entity, entityKey: entityKey, allowed: copyAllowed,
	}, nil
}

// ValidateCoverage 复验同一 ApprovedManifest、expected digest 和独立 dataset exact-set。
func ValidateCoverage(approved ApprovedManifest, expected ManifestDigest, requirements ManifestRequirements) (ManifestCoverageSummary, error) {
	manifest, err := approved.verify(expected)
	if err != nil {
		return ManifestCoverageSummary{}, err
	}
	required, source, err := requirements.verify()
	if err != nil {
		return ManifestCoverageSummary{}, err
	}
	actual, err := manifestDecisionEntries(manifest)
	if err != nil {
		return ManifestCoverageSummary{}, err
	}
	summary, duplicateRequirements, duplicateManifest, err := compareCoverage(actual, required)
	if err != nil {
		return ManifestCoverageSummary{}, err
	}
	if duplicateRequirements > 0 || duplicateManifest > 0 {
		return summary, gateError("manifest_coverage_duplicate", "manifest coverage exact-set 含重复或双重决议")
	}
	if coverageCount(summary, "missing") > 0 || coverageCount(summary, "unused") > 0 || coverageCount(summary, "wrong_category") > 0 {
		return summary, gateError("manifest_coverage_mismatch", "manifest 未精确覆盖 dataset anomaly exact-set")
	}
	if err := validateManifestAgainstSourceContext(manifest, source); err != nil {
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

type manifestDecision struct {
	entity ManifestEntity
	key    CanonicalManifestKey
	option ManifestDecisionOption
}

func compareCoverage(actual []manifestDecision, required []ManifestRequirement) (ManifestCoverageSummary, int64, int64, error) {
	seenRequirements := make(map[CanonicalManifestKey]struct{}, len(required))
	var duplicateRequirements int64
	for _, requirement := range required {
		if err := validateRequirement(requirement); err != nil {
			return ManifestCoverageSummary{}, 0, 0, err
		}
		if _, duplicate := seenRequirements[requirement.anomaly]; duplicate {
			duplicateRequirements++
		} else {
			seenRequirements[requirement.anomaly] = struct{}{}
		}
	}
	seenActual := make(map[manifestDecision]struct{}, len(actual))
	var duplicateManifest int64
	for _, decision := range actual {
		if _, duplicate := seenActual[decision]; duplicate {
			duplicateManifest++
		} else {
			seenActual[decision] = struct{}{}
		}
	}
	// 用最大二分匹配避免 allowed 集重叠时被输入顺序影响。例如 {A,B} 与 {A}
	// 必须能匹配实际 A+B，而不能因为第一条先拿走 A 产生伪 duplicate/missing。
	actualOwner := make([]int, len(actual))
	for index := range actualOwner {
		actualOwner[index] = -1
	}
	var augment func(int, []bool) bool
	augment = func(requirementIndex int, visited []bool) bool {
		for actualIndex, decision := range actual {
			if visited[actualIndex] || !requirementAccepts(required[requirementIndex], decision) {
				continue
			}
			visited[actualIndex] = true
			if actualOwner[actualIndex] < 0 || augment(actualOwner[actualIndex], visited) {
				actualOwner[actualIndex] = requirementIndex
				return true
			}
		}
		return false
	}
	for requirementIndex := range required {
		_ = augment(requirementIndex, make([]bool, len(actual)))
	}
	matchedActual := make([]bool, len(actual))
	matchedRequirement := make([]bool, len(required))
	var matched int64
	for actualIndex, requirementIndex := range actualOwner {
		if requirementIndex >= 0 {
			matchedActual[actualIndex] = true
			matchedRequirement[requirementIndex] = true
			matched++
		}
	}
	// 一个 anomaly 的第二个同样获批替代方案属于双重决议，而不是普通 stale。
	for actualIndex, decision := range actual {
		if matchedActual[actualIndex] {
			continue
		}
		for _, requirement := range required {
			if requirementAccepts(requirement, decision) {
				duplicateManifest++
				matchedActual[actualIndex] = true
				break
			}
		}
	}
	var missing, unused, wrongCategory int64
	for actualIndex, decision := range actual {
		if matchedActual[actualIndex] {
			continue
		}
		wrongIndex := -1
		for requirementIndex, requirement := range required {
			if !matchedRequirement[requirementIndex] && requirement.entity == decision.entity && requirement.entityKey == decision.key {
				wrongIndex = requirementIndex
				break
			}
		}
		if wrongIndex >= 0 {
			matchedRequirement[wrongIndex] = true
			wrongCategory++
		} else {
			unused++
		}
	}
	for index := range required {
		if !matchedRequirement[index] {
			missing++
		}
	}
	return newCoverageSummary(int64(len(required)), int64(len(actual)), matched, missing, unused, wrongCategory, duplicateRequirements, duplicateManifest), duplicateRequirements, duplicateManifest, nil
}

func validateRequirement(requirement ManifestRequirement) error {
	if requirement.anomaly == (CanonicalManifestKey{}) || requirement.entityKey == (CanonicalManifestKey{}) || len(requirement.allowed) == 0 {
		return gateError("manifest_requirements_invalid", "manifest requirements 含无效或被变异的 identity")
	}
	for _, option := range requirement.allowed {
		if !manifestSectionAcceptsEntity(option.category, requirement.entity) || !manifestCategoryAcceptsAction(option.category, option.action) {
			return gateError("manifest_requirements_invalid", "manifest requirements 含无效 allowed 决议")
		}
	}
	return nil
}

func requirementAccepts(requirement ManifestRequirement, decision manifestDecision) bool {
	if requirement.entity != decision.entity || requirement.entityKey != decision.key {
		return false
	}
	for _, option := range requirement.allowed {
		if option == decision.option {
			return true
		}
	}
	return false
}

func canonicalRequirementParts(entity ManifestEntity, identifiers []string) ([]string, error) {
	switch entity {
	case ManifestEntityUser, ManifestEntityPost, ManifestEntityComment, ManifestEntityLike, ManifestEntityNotification:
		if len(identifiers) != 1 {
			return nil, gateError("manifest_requirement_identity_invalid", "manifest requirement identity 数量不正确")
		}
		value := identifiers[0]
		if err := canonicalizeUUID(&value, false); err != nil {
			return nil, err
		}
		return []string{value}, nil
	case ManifestEntityDictionaryMapping:
		if len(identifiers) != 2 {
			return nil, gateError("manifest_requirement_identity_invalid", "manifest requirement 词表 identity 数量不正确")
		}
		kind := normalizeDictionaryKind(identifiers[0])
		if !validDictionary(kind) || !isIdentifier(identifiers[1]) {
			return nil, gateError("manifest_requirement_identity_invalid", "manifest requirement 词表 identity 无效")
		}
		return []string{kind, identifiers[1]}, nil
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

func manifestCategoryAcceptsAction(category, action string) bool {
	switch category {
	case "excluded_users", "excluded_content", "orphan_like_exclusions", "orphan_notification_exclusions":
		return action == "exclude"
	case "email_rewrites":
		return action == "rewrite"
	case "post_type_resolutions":
		return action == "set_type"
	case "dictionary_mappings":
		return action == "map"
	case "post_image_resolutions":
		return action == "map" || action == "exclude"
	case "avatar_resolutions":
		return action == "clear" || action == "replace"
	case "duplicate_image_asset_resolutions":
		return action == "keep" || action == "exclude"
	case "comment_reparent_resolutions":
		return action == "set_parent" || action == "set_parent_and_reply_to" || action == "clear_parent" || action == "set_reply_to" || action == "clear_reply_to"
	default:
		return false
	}
}

func manifestDecisionEntries(manifest manifestData) ([]manifestDecision, error) {
	entries := make([]manifestDecision, 0, manifest.Summary().TotalEntries)
	appendEntry := func(category, action string, entity ManifestEntity, identifiers ...string) error {
		parts, err := canonicalRequirementParts(entity, identifiers)
		if err != nil {
			return err
		}
		entries = append(entries, manifestDecision{entity: entity, key: hashCanonicalManifestKey(string(entity), parts...), option: ManifestDecisionOption{category: category, action: action}})
		return nil
	}
	for _, d := range manifest.ExcludedUsers {
		if err := appendEntry("excluded_users", d.Action, ManifestEntityUser, d.UserID); err != nil {
			return nil, err
		}
	}
	for _, d := range manifest.ExcludedContent {
		if err := appendEntry("excluded_content", d.Action, ManifestEntity(d.ContentType), d.ContentID); err != nil {
			return nil, err
		}
	}
	for _, d := range manifest.EmailRewrites {
		if err := appendEntry("email_rewrites", d.Action, ManifestEntityUser, d.UserID); err != nil {
			return nil, err
		}
	}
	for _, d := range manifest.PostTypeResolutions {
		if err := appendEntry("post_type_resolutions", d.Action, ManifestEntityPost, d.PostID); err != nil {
			return nil, err
		}
	}
	for _, d := range manifest.DictionaryMappings {
		if err := appendEntry("dictionary_mappings", d.Action, ManifestEntityDictionaryMapping, d.Dictionary, d.Source); err != nil {
			return nil, err
		}
	}
	for _, d := range manifest.PostImageResolutions {
		if err := appendEntry("post_image_resolutions", d.Action, ManifestEntityPostImageReference, d.PostID, d.SourceReference); err != nil {
			return nil, err
		}
	}
	for _, d := range manifest.AvatarResolutions {
		if err := appendEntry("avatar_resolutions", d.Action, ManifestEntityUser, d.UserID); err != nil {
			return nil, err
		}
	}
	for _, d := range manifest.DuplicateImageAssetResolutions {
		if err := appendEntry("duplicate_image_asset_resolutions", d.Action, ManifestEntityDuplicateImageAsset, d.GroupKey, d.ImageAssetID); err != nil {
			return nil, err
		}
	}
	for _, d := range manifest.CommentReparentResolutions {
		if err := appendEntry("comment_reparent_resolutions", d.Action, ManifestEntityComment, d.CommentID); err != nil {
			return nil, err
		}
	}
	for _, d := range manifest.OrphanLikeExclusions {
		if err := appendEntry("orphan_like_exclusions", d.Action, ManifestEntityLike, d.LikeID); err != nil {
			return nil, err
		}
	}
	for _, d := range manifest.OrphanNotificationExclusions {
		if err := appendEntry("orphan_notification_exclusions", d.Action, ManifestEntityNotification, d.NotificationID); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func validDictionary(value string) bool {
	return value == "canteen" || value == "cuisine" || value == "flavor"
}

// Format 阻止 fmt 输出 anomaly identity。
func (ManifestRequirement) Format(state fmt.State, _ rune) {
	_, _ = fmt.Fprint(state, "<manifest-requirement:redacted>")
}

// MarshalJSON 只返回固定脱敏标记。
func (ManifestRequirement) MarshalJSON() ([]byte, error) {
	return []byte(`{"type":"manifest_requirement","redacted":true}`), nil
}

// Format 阻止 fmt 输出 requirements 和 source context。
func (ManifestRequirements) Format(state fmt.State, _ rune) {
	_, _ = fmt.Fprint(state, "<manifest-requirements:redacted>")
}

// MarshalJSON 只返回固定脱敏标记。
func (ManifestRequirements) MarshalJSON() ([]byte, error) {
	return []byte(`{"type":"manifest_requirements","redacted":true}`), nil
}

func newCoverageSummary(required, actual, matched, missing, unused, wrongCategory, duplicateRequirements, duplicateManifest int64) ManifestCoverageSummary {
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
