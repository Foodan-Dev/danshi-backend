package legacymigration

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	// ManifestSchemaVersion 是私有清洗决议文件当前唯一支持的结构版本。
	ManifestSchemaVersion = 1
)

var (
	errDuplicateManifestKey  = errors.New("duplicate manifest key")
	requiredManifestSections = []string{
		"excluded_users",
		"excluded_content",
		"email_rewrites",
		"post_type_resolutions",
		"dictionary_mappings",
		"post_image_resolutions",
		"avatar_resolutions",
		"duplicate_image_asset_resolutions",
		"comment_reparent_resolutions",
		"orphan_like_exclusions",
		"orphan_notification_exclusions",
	}
	manifestSectionFields = map[string][]string{
		"excluded_users":                    {"user_id", "action"},
		"excluded_content":                  {"content_type", "content_id", "action"},
		"email_rewrites":                    {"user_id", "action", "new_email"},
		"post_type_resolutions":             {"post_id", "action", "target_type"},
		"dictionary_mappings":               {"dictionary", "source", "action", "target"},
		"post_image_resolutions":            {"post_id", "source_reference", "action", "target_image_asset_id"},
		"avatar_resolutions":                {"user_id", "action", "target_image_asset_id"},
		"duplicate_image_asset_resolutions": {"group_key", "image_asset_id", "action"},
		"comment_reparent_resolutions": {
			"comment_id", "action", "target_parent_id", "target_reply_to_user_id",
		},
		"orphan_like_exclusions":         {"like_id", "action"},
		"orphan_notification_exclusions": {"notification_id", "action"},
	}
)

// manifestData 是受权限保护的旧库清洗决议，只能封装在 ApprovedManifest 内部。
type manifestData struct {
	SchemaVersion                  int                             `json:"schema_version"`
	ExcludedUsers                  []ExcludedUserDecision          `json:"excluded_users"`
	ExcludedContent                []ExcludedContentDecision       `json:"excluded_content"`
	EmailRewrites                  []EmailRewriteDecision          `json:"email_rewrites"`
	PostTypeResolutions            []PostTypeResolution            `json:"post_type_resolutions"`
	DictionaryMappings             []DictionaryMapping             `json:"dictionary_mappings"`
	PostImageResolutions           []PostImageResolution           `json:"post_image_resolutions"`
	AvatarResolutions              []AvatarResolution              `json:"avatar_resolutions"`
	DuplicateImageAssetResolutions []DuplicateImageAssetResolution `json:"duplicate_image_asset_resolutions"`
	CommentReparentResolutions     []CommentReparentResolution     `json:"comment_reparent_resolutions"`
	OrphanLikeExclusions           []OrphanLikeExclusion           `json:"orphan_like_exclusions"`
	OrphanNotificationExclusions   []OrphanNotificationExclusion   `json:"orphan_notification_exclusions"`
}

// ExcludedUserDecision 显式排除一个来源用户。
type ExcludedUserDecision struct {
	UserID string `json:"user_id"`
	Action string `json:"action"`
}

// ExcludedContentDecision 显式排除一篇来源帖子或评论。
type ExcludedContentDecision struct {
	ContentType string `json:"content_type"`
	ContentID   string `json:"content_id"`
	Action      string `json:"action"`
}

// EmailRewriteDecision 为保留用户指定唯一的新邮箱。
type EmailRewriteDecision struct {
	UserID   string `json:"user_id"`
	Action   string `json:"action"`
	NewEmail string `json:"new_email"`
}

// PostTypeResolution 将来源帖子类型收敛到 v11 支持的枚举。
type PostTypeResolution struct {
	PostID     string `json:"post_id"`
	Action     string `json:"action"`
	TargetType string `json:"target_type"`
}

// DictionaryMapping 将来源词表值映射到目标固定词表值。
type DictionaryMapping struct {
	Dictionary string `json:"dictionary"`
	Source     string `json:"source"`
	Action     string `json:"action"`
	Target     string `json:"target"`
}

// PostImageResolution 决定帖子中的单个来源图片引用如何迁移。
type PostImageResolution struct {
	PostID             string `json:"post_id"`
	SourceReference    string `json:"source_reference"`
	Action             string `json:"action"`
	TargetImageAssetID string `json:"target_image_asset_id,omitempty"`
}

// AvatarResolution 决定保留用户的无效头像是清空还是替换。
type AvatarResolution struct {
	UserID             string `json:"user_id"`
	Action             string `json:"action"`
	TargetImageAssetID string `json:"target_image_asset_id,omitempty"`
}

// DuplicateImageAssetResolution 为重复图片组中的单个来源资产指定保留或排除动作。
type DuplicateImageAssetResolution struct {
	GroupKey     string `json:"group_key"`
	ImageAssetID string `json:"image_asset_id"`
	Action       string `json:"action"`
}

// CommentReparentResolution 显式修正来源评论的父评论或回复对象。
type CommentReparentResolution struct {
	CommentID           string `json:"comment_id"`
	Action              string `json:"action"`
	TargetParentID      string `json:"target_parent_id,omitempty"`
	TargetReplyToUserID string `json:"target_reply_to_user_id,omitempty"`
}

// OrphanLikeExclusion 显式排除一个无法解析目标的来源点赞。
type OrphanLikeExclusion struct {
	LikeID string `json:"like_id"`
	Action string `json:"action"`
}

// OrphanNotificationExclusion 显式排除一个无法解析目标的来源通知。
type OrphanNotificationExclusion struct {
	NotificationID string `json:"notification_id"`
	Action         string `json:"action"`
}

// ManifestSummary 只包含固定 section code 和聚合数量，可以安全进入迁移报告。
type ManifestSummary struct {
	SchemaVersion int              `json:"schema_version"`
	Sections      []AggregateCount `json:"sections"`
	TotalEntries  int64            `json:"total_entries"`
}

// Summary 将私有 manifest 收敛为固定 code 的聚合计数，不复制任何行级值。
func (manifest manifestData) Summary() ManifestSummary {
	counts := []int{
		len(manifest.ExcludedUsers),
		len(manifest.ExcludedContent),
		len(manifest.EmailRewrites),
		len(manifest.PostTypeResolutions),
		len(manifest.DictionaryMappings),
		len(manifest.PostImageResolutions),
		len(manifest.AvatarResolutions),
		len(manifest.DuplicateImageAssetResolutions),
		len(manifest.CommentReparentResolutions),
		len(manifest.OrphanLikeExclusions),
		len(manifest.OrphanNotificationExclusions),
	}
	result := ManifestSummary{
		SchemaVersion: ManifestSchemaVersion,
		Sections:      make([]AggregateCount, 0, len(requiredManifestSections)),
	}
	for index, code := range requiredManifestSections {
		count := int64(counts[index])
		result.Sections = append(result.Sections, AggregateCount{Code: code, Count: count})
		result.TotalEntries += count
	}
	return result
}

// LoadManifest 从权限受限的普通文件加载并严格验证私有清洗决议。
// expected 必须来自独立审批渠道；返回对象把该摘要与决议封装在一起，错误不会包含路径、manifest 值或底层错误。
func LoadManifest(path string, expected ManifestDigest) (ApprovedManifest, error) {
	if expected == (ManifestDigest{}) {
		return ApprovedManifest{}, gateError("manifest_digest_required", "必须提供独立获批的 manifest SHA-256")
	}
	data, digest, err := readManifestFile(path)
	if err != nil {
		return ApprovedManifest{}, err
	}
	if !digest.equal(expected) {
		return ApprovedManifest{}, gateError("manifest_digest_mismatch", "私有清洗 manifest SHA-256 与获批摘要不一致")
	}
	manifest, err := decodeManifest(data)
	if err != nil {
		return ApprovedManifest{}, err
	}
	return newApprovedManifest(manifest, digest)
}

func decodeManifest(data []byte) (manifestData, error) {
	if !utf8.Valid(data) {
		return manifestData{}, gateError("manifest_invalid_json", "私有清洗 manifest 必须是合法 UTF-8 JSON")
	}
	if err := validateJSONSurrogatePairs(data); err != nil {
		return manifestData{}, gateError("manifest_invalid_json", "私有清洗 manifest 含未配对的 UTF-16 surrogate")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		if errors.Is(err, errDuplicateManifestKey) {
			return manifestData{}, gateError("manifest_duplicate_key", "私有清洗 manifest 含重复 JSON key")
		}
		return manifestData{}, gateError("manifest_invalid_json", "私有清洗 manifest 不是合法 JSON")
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return manifestData{}, gateError("manifest_invalid_json", "私有清洗 manifest 顶层必须是 JSON object")
	}
	if err := validateExactManifestFields(fields); err != nil {
		return manifestData{}, err
	}
	if _, exists := fields["schema_version"]; !exists {
		return manifestData{}, gateError("manifest_schema_version_missing", "私有清洗 manifest 缺少 schema_version")
	}
	for _, section := range requiredManifestSections {
		raw, exists := fields[section]
		if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return manifestData{}, gateError("manifest_section_missing", "私有清洗 manifest 必须显式包含全部 section，空 section 也必须写 []")
		}
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest manifestData
	if err := decoder.Decode(&manifest); err != nil {
		return manifestData{}, gateError("manifest_schema_invalid", "私有清洗 manifest 不符合 v1 结构")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return manifestData{}, gateError("manifest_invalid_json", "私有清洗 manifest 只能包含一个 JSON object")
	}
	if manifest.SchemaVersion != ManifestSchemaVersion {
		return manifestData{}, gateError("manifest_schema_version_unsupported", "私有清洗 manifest schema_version 不受支持")
	}
	if err := manifest.canonicalize(); err != nil {
		return manifestData{}, err
	}
	if err := manifest.validate(); err != nil {
		return manifestData{}, err
	}
	return manifest, nil
}

func validateExactManifestFields(fields map[string]json.RawMessage) error {
	allowedTopLevel := make(map[string]struct{}, len(requiredManifestSections)+1)
	allowedTopLevel["schema_version"] = struct{}{}
	for _, section := range requiredManifestSections {
		allowedTopLevel[section] = struct{}{}
	}
	for field := range fields {
		if _, allowed := allowedTopLevel[field]; !allowed {
			return gateError("manifest_schema_invalid", "私有清洗 manifest 含大小写不精确或未知字段")
		}
	}
	for section, allowedNames := range manifestSectionFields {
		raw, exists := fields[section]
		if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		var entries []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil {
			return gateError("manifest_schema_invalid", "私有清洗 manifest section 必须是 object array")
		}
		allowed := make(map[string]struct{}, len(allowedNames))
		for _, name := range allowedNames {
			allowed[name] = struct{}{}
		}
		for _, entry := range entries {
			if entry == nil {
				return gateError("manifest_schema_invalid", "私有清洗 manifest section 不能包含 null")
			}
			for field := range entry {
				if _, exists := allowed[field]; !exists {
					return gateError("manifest_schema_invalid", "私有清洗 manifest 含大小写不精确或未知字段")
				}
			}
		}
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			folded := foldManifestKey(key)
			if _, exists := seen[folded]; exists {
				return errDuplicateManifestKey
			}
			seen[folded] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}

func validateJSONSurrogatePairs(data []byte) error {
	insideString := false
	for index := 0; index < len(data); index++ {
		switch data[index] {
		case '"':
			insideString = !insideString
		case '\\':
			if !insideString {
				continue
			}
			index++
			if index >= len(data) || data[index] != 'u' {
				continue
			}
			value, ok := decodeJSONHex4(data, index+1)
			if !ok {
				return errors.New("invalid unicode escape")
			}
			index += 4
			if value >= 0xdc00 && value <= 0xdfff {
				return errors.New("unpaired low surrogate")
			}
			if value < 0xd800 || value > 0xdbff {
				continue
			}
			if index+6 >= len(data) || data[index+1] != '\\' || data[index+2] != 'u' {
				return errors.New("unpaired high surrogate")
			}
			low, validLow := decodeJSONHex4(data, index+3)
			if !validLow || low < 0xdc00 || low > 0xdfff {
				return errors.New("unpaired high surrogate")
			}
			index += 6
		}
	}
	return nil
}

func decodeJSONHex4(data []byte, start int) (uint16, bool) {
	if start+4 > len(data) {
		return 0, false
	}
	var value uint16
	for _, digit := range data[start : start+4] {
		value <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			value |= uint16(digit - '0')
		case digit >= 'a' && digit <= 'f':
			value |= uint16(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			value |= uint16(digit-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func foldManifestKey(value string) string {
	var folded strings.Builder
	folded.Grow(len(value))
	for _, current := range value {
		minimum := current
		for next := unicode.SimpleFold(current); next != current; next = unicode.SimpleFold(next) {
			if next < minimum {
				minimum = next
			}
		}
		folded.WriteRune(minimum)
	}
	return folded.String()
}

func ensureJSONEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("trailing JSON value")
	}
	return nil
}

func (manifest *manifestData) canonicalize() error {
	if err := manifest.canonicalizeUsers(); err != nil {
		return err
	}
	if err := manifest.canonicalizePostsAndContent(); err != nil {
		return err
	}
	if err := manifest.canonicalizeImages(); err != nil {
		return err
	}
	if err := manifest.canonicalizeComments(); err != nil {
		return err
	}
	for index := range manifest.OrphanLikeExclusions {
		if err := canonicalizeUUID(&manifest.OrphanLikeExclusions[index].LikeID, false); err != nil {
			return err
		}
	}
	for index := range manifest.OrphanNotificationExclusions {
		if err := canonicalizeUUID(&manifest.OrphanNotificationExclusions[index].NotificationID, false); err != nil {
			return err
		}
	}
	return nil
}

func (manifest *manifestData) canonicalizeUsers() error {
	for index := range manifest.ExcludedUsers {
		if err := canonicalizeUUID(&manifest.ExcludedUsers[index].UserID, false); err != nil {
			return err
		}
	}
	for index := range manifest.EmailRewrites {
		decision := &manifest.EmailRewrites[index]
		if err := canonicalizeUUID(&decision.UserID, false); err != nil {
			return err
		}
		email, err := canonicalEmail(decision.NewEmail)
		if err != nil {
			return err
		}
		decision.NewEmail = email
	}
	for index := range manifest.AvatarResolutions {
		if err := canonicalizeUUID(&manifest.AvatarResolutions[index].UserID, false); err != nil {
			return err
		}
	}
	return nil
}

func (manifest *manifestData) canonicalizePostsAndContent() error {
	for index := range manifest.ExcludedContent {
		if err := canonicalizeUUID(&manifest.ExcludedContent[index].ContentID, false); err != nil {
			return err
		}
	}
	for index := range manifest.PostTypeResolutions {
		if err := canonicalizeUUID(&manifest.PostTypeResolutions[index].PostID, false); err != nil {
			return err
		}
	}
	for index := range manifest.PostImageResolutions {
		if err := canonicalizeUUID(&manifest.PostImageResolutions[index].PostID, false); err != nil {
			return err
		}
	}
	for index := range manifest.DictionaryMappings {
		manifest.DictionaryMappings[index].Dictionary = normalizeDictionaryKind(manifest.DictionaryMappings[index].Dictionary)
	}
	return nil
}

func normalizeDictionaryKind(value string) string {
	if value == "preference_flavor" {
		return "flavor"
	}
	return value
}

func (manifest *manifestData) canonicalizeImages() error {
	for index := range manifest.PostImageResolutions {
		if err := canonicalizeUUID(&manifest.PostImageResolutions[index].TargetImageAssetID, true); err != nil {
			return err
		}
	}
	for index := range manifest.AvatarResolutions {
		if err := canonicalizeUUID(&manifest.AvatarResolutions[index].TargetImageAssetID, true); err != nil {
			return err
		}
	}
	for index := range manifest.DuplicateImageAssetResolutions {
		if err := canonicalizeUUID(&manifest.DuplicateImageAssetResolutions[index].ImageAssetID, false); err != nil {
			return err
		}
	}
	return nil
}

func (manifest *manifestData) canonicalizeComments() error {
	for index := range manifest.CommentReparentResolutions {
		decision := &manifest.CommentReparentResolutions[index]
		if err := canonicalizeUUID(&decision.CommentID, false); err != nil {
			return err
		}
		if err := canonicalizeUUID(&decision.TargetParentID, true); err != nil {
			return err
		}
		if err := canonicalizeUUID(&decision.TargetReplyToUserID, true); err != nil {
			return err
		}
	}
	return nil
}

func canonicalizeUUID(value *string, optional bool) error {
	if optional && *value == "" {
		return nil
	}
	if strings.TrimSpace(*value) != *value || *value == "" {
		return gateError("manifest_identifier_empty", "私有清洗 manifest 含空标识或标识首尾空白")
	}
	parsed, err := uuid.Parse(*value)
	if err != nil || parsed == uuid.Nil {
		return gateError("manifest_uuid_invalid", "私有清洗 manifest 含无法解析的来源 UUID")
	}
	*value = parsed.String()
	return nil
}

func canonicalEmail(value string) (string, error) {
	if !isIdentifier(value) {
		return "", gateError("manifest_identifier_empty", "私有清洗 manifest 含空值或值首尾空白")
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Name != "" || parsed.Address != value {
		return "", gateError("manifest_email_invalid", "私有清洗 manifest 含无效目标邮箱")
	}
	return strings.ToLower(value), nil
}

func (manifest manifestData) validate() error {
	validation := manifestValidation{
		excludedUsers:   make(map[string]struct{}, len(manifest.ExcludedUsers)),
		excludedContent: make(map[[2]string]struct{}, len(manifest.ExcludedContent)),
		assetActions:    make(map[string]string, len(manifest.DuplicateImageAssetResolutions)),
	}
	if err := validation.validateExcludedUsers(manifest.ExcludedUsers); err != nil {
		return err
	}
	if err := validation.validateExcludedContent(manifest.ExcludedContent); err != nil {
		return err
	}
	if err := validation.validateEmailRewrites(manifest.EmailRewrites); err != nil {
		return err
	}
	if err := validation.validatePostTypes(manifest.PostTypeResolutions); err != nil {
		return err
	}
	if err := validateDictionaryMappings(manifest.DictionaryMappings); err != nil {
		return err
	}
	if err := validation.validatePostImages(manifest.PostImageResolutions); err != nil {
		return err
	}
	if err := validation.validateAvatars(manifest.AvatarResolutions); err != nil {
		return err
	}
	if err := validation.validateDuplicateImageAssets(manifest.DuplicateImageAssetResolutions); err != nil {
		return err
	}
	if err := validation.validateAssetTargets(manifest.PostImageResolutions, manifest.AvatarResolutions); err != nil {
		return err
	}
	if err := validation.validateCommentReparents(manifest.CommentReparentResolutions); err != nil {
		return err
	}
	if err := validateOrphanLikeExclusions(manifest.OrphanLikeExclusions); err != nil {
		return err
	}
	return validateOrphanNotificationExclusions(manifest.OrphanNotificationExclusions)
}

type manifestValidation struct {
	excludedUsers   map[string]struct{}
	excludedContent map[[2]string]struct{}
	assetActions    map[string]string
}

func (validation *manifestValidation) validateExcludedUsers(decisions []ExcludedUserDecision) error {
	for _, decision := range decisions {
		if err := requireIdentifiers(decision.UserID); err != nil {
			return err
		}
		if err := requireAction(decision.Action, "exclude"); err != nil {
			return err
		}
		if err := addUnique(validation.excludedUsers, decision.UserID); err != nil {
			return err
		}
	}
	return nil
}

func (validation *manifestValidation) validateExcludedContent(decisions []ExcludedContentDecision) error {
	for _, decision := range decisions {
		if err := requireIdentifiers(decision.ContentType, decision.ContentID); err != nil {
			return err
		}
		if decision.ContentType != "post" && decision.ContentType != "comment" {
			return gateError("manifest_identifier_kind_unknown", "私有清洗 manifest 含未识别的标识类型")
		}
		if err := requireAction(decision.Action, "exclude"); err != nil {
			return err
		}
		if err := addUnique(validation.excludedContent, [2]string{decision.ContentType, decision.ContentID}); err != nil {
			return err
		}
	}
	return nil
}

func (validation manifestValidation) validateEmailRewrites(decisions []EmailRewriteDecision) error {
	emailUsers := make(map[string]struct{}, len(decisions))
	targetEmails := make(map[string]struct{}, len(decisions))
	for _, decision := range decisions {
		if err := requireIdentifiers(decision.UserID, decision.NewEmail); err != nil {
			return err
		}
		if err := requireAction(decision.Action, "rewrite"); err != nil {
			return err
		}
		if err := addUnique(emailUsers, decision.UserID); err != nil {
			return err
		}
		if _, duplicate := targetEmails[decision.NewEmail]; duplicate {
			return gateError("manifest_email_target_duplicate", "私有清洗 manifest 含重复目标邮箱")
		}
		targetEmails[decision.NewEmail] = struct{}{}
		if _, conflict := validation.excludedUsers[decision.UserID]; conflict {
			return manifestConflict()
		}
	}
	return nil
}

func (validation manifestValidation) validatePostTypes(decisions []PostTypeResolution) error {
	postTypes := make(map[string]struct{}, len(decisions))
	for _, decision := range decisions {
		if err := requireIdentifiers(decision.PostID, decision.TargetType); err != nil {
			return err
		}
		if err := requireAction(decision.Action, "set_type"); err != nil {
			return err
		}
		if decision.TargetType != "share" && decision.TargetType != "seeking" {
			return gateError("manifest_target_value_unknown", "私有清洗 manifest 含未识别的目标值")
		}
		if err := addUnique(postTypes, decision.PostID); err != nil {
			return err
		}
		if _, conflict := validation.excludedContent[[2]string{"post", decision.PostID}]; conflict {
			return manifestConflict()
		}
	}
	return nil
}

func validateDictionaryMappings(decisions []DictionaryMapping) error {
	dictionaries := make(map[[2]string]struct{}, len(decisions))
	for _, decision := range decisions {
		if err := requireIdentifiers(decision.Dictionary, decision.Source, decision.Target); err != nil {
			return err
		}
		if decision.Dictionary != "canteen" && decision.Dictionary != "cuisine" && decision.Dictionary != "flavor" {
			return gateError("manifest_identifier_kind_unknown", "私有清洗 manifest 含未识别的标识类型")
		}
		if err := requireAction(decision.Action, "map"); err != nil {
			return err
		}
		if err := addUnique(dictionaries, [2]string{decision.Dictionary, decision.Source}); err != nil {
			return err
		}
	}
	return nil
}

func (validation manifestValidation) validatePostImages(decisions []PostImageResolution) error {
	postImages := make(map[[2]string]struct{}, len(decisions))
	postTargets := make(map[[2]string]struct{}, len(decisions))
	for _, decision := range decisions {
		if err := requireIdentifiers(decision.PostID, decision.SourceReference); err != nil {
			return err
		}
		if err := requireAction(decision.Action, "map", "exclude"); err != nil {
			return err
		}
		if (decision.Action == "map") != isIdentifier(decision.TargetImageAssetID) {
			return gateError("manifest_action_fields_invalid", "私有清洗 manifest 的 action 与字段组合不合法")
		}
		if err := addUnique(postImages, [2]string{decision.PostID, decision.SourceReference}); err != nil {
			return err
		}
		if decision.Action == "map" {
			if err := addUnique(postTargets, [2]string{decision.PostID, decision.TargetImageAssetID}); err != nil {
				return err
			}
		}
		if _, conflict := validation.excludedContent[[2]string{"post", decision.PostID}]; conflict {
			return manifestConflict()
		}
	}
	return nil
}

func (validation manifestValidation) validateAvatars(decisions []AvatarResolution) error {
	avatarUsers := make(map[string]struct{}, len(decisions))
	for _, decision := range decisions {
		if err := requireIdentifiers(decision.UserID); err != nil {
			return err
		}
		if err := requireAction(decision.Action, "clear", "replace"); err != nil {
			return err
		}
		if (decision.Action == "replace") != isIdentifier(decision.TargetImageAssetID) {
			return gateError("manifest_action_fields_invalid", "私有清洗 manifest 的 action 与字段组合不合法")
		}
		if err := addUnique(avatarUsers, decision.UserID); err != nil {
			return err
		}
		if _, conflict := validation.excludedUsers[decision.UserID]; conflict {
			return manifestConflict()
		}
	}
	return nil
}

func (validation *manifestValidation) validateDuplicateImageAssets(decisions []DuplicateImageAssetResolution) error {
	groupCounts := make(map[string]int)
	groupKeeps := make(map[string]int)
	for _, decision := range decisions {
		if err := requireIdentifiers(decision.GroupKey, decision.ImageAssetID); err != nil {
			return err
		}
		if err := requireAction(decision.Action, "keep", "exclude"); err != nil {
			return err
		}
		if _, duplicate := validation.assetActions[decision.ImageAssetID]; duplicate {
			return manifestDuplicateEntry()
		}
		validation.assetActions[decision.ImageAssetID] = decision.Action
		groupCounts[decision.GroupKey]++
		if decision.Action == "keep" {
			groupKeeps[decision.GroupKey]++
		}
	}
	for group, count := range groupCounts {
		if count < 2 || groupKeeps[group] != 1 {
			return manifestConflict()
		}
	}
	return nil
}

func (validation manifestValidation) validateAssetTargets(
	postImages []PostImageResolution,
	avatars []AvatarResolution,
) error {
	for _, decision := range postImages {
		if decision.Action == "map" && validation.assetActions[decision.TargetImageAssetID] == "exclude" {
			return manifestConflict()
		}
	}
	for _, decision := range avatars {
		if decision.Action == "replace" && validation.assetActions[decision.TargetImageAssetID] == "exclude" {
			return manifestConflict()
		}
	}
	return nil
}

func (validation manifestValidation) validateCommentReparents(decisions []CommentReparentResolution) error {
	comments := make(map[string]struct{}, len(decisions))
	for _, decision := range decisions {
		if err := requireIdentifiers(decision.CommentID); err != nil {
			return err
		}
		if err := requireAction(
			decision.Action,
			"set_parent", "set_parent_and_reply_to", "clear_parent", "set_reply_to", "clear_reply_to",
		); err != nil {
			return err
		}
		if !validCommentResolutionFields(decision) {
			return gateError("manifest_action_fields_invalid", "私有清洗 manifest 的 action 与字段组合不合法")
		}
		if err := addUnique(comments, decision.CommentID); err != nil {
			return err
		}
		if _, conflict := validation.excludedContent[[2]string{"comment", decision.CommentID}]; conflict {
			return manifestConflict()
		}
		if decision.TargetParentID == decision.CommentID {
			return manifestConflict()
		}
		if _, conflict := validation.excludedContent[[2]string{"comment", decision.TargetParentID}]; conflict {
			return manifestConflict()
		}
		if _, conflict := validation.excludedUsers[decision.TargetReplyToUserID]; conflict {
			return manifestConflict()
		}
	}
	return validateCommentParentCycles(decisions)
}

func validateCommentParentCycles(decisions []CommentReparentResolution) error {
	edges := make(map[string]string, len(decisions))
	for _, decision := range decisions {
		if decision.Action == "set_parent" || decision.Action == "set_parent_and_reply_to" {
			edges[decision.CommentID] = decision.TargetParentID
		}
	}
	states := make(map[string]uint8, len(edges))
	var visit func(string) bool
	visit = func(commentID string) bool {
		switch states[commentID] {
		case 1:
			return true
		case 2:
			return false
		}
		parentID, exists := edges[commentID]
		if !exists {
			return false
		}
		states[commentID] = 1
		if visit(parentID) {
			return true
		}
		states[commentID] = 2
		return false
	}
	for commentID := range edges {
		if visit(commentID) {
			return manifestConflict()
		}
	}
	return nil
}

func validateOrphanLikeExclusions(decisions []OrphanLikeExclusion) error {
	likes := make(map[string]struct{}, len(decisions))
	for _, decision := range decisions {
		if err := requireIdentifiers(decision.LikeID); err != nil {
			return err
		}
		if err := requireAction(decision.Action, "exclude"); err != nil {
			return err
		}
		if err := addUnique(likes, decision.LikeID); err != nil {
			return err
		}
	}
	return nil
}

func validateOrphanNotificationExclusions(decisions []OrphanNotificationExclusion) error {
	notifications := make(map[string]struct{}, len(decisions))
	for _, decision := range decisions {
		if err := requireIdentifiers(decision.NotificationID); err != nil {
			return err
		}
		if err := requireAction(decision.Action, "exclude"); err != nil {
			return err
		}
		if err := addUnique(notifications, decision.NotificationID); err != nil {
			return err
		}
	}
	return nil
}

func validCommentResolutionFields(decision CommentReparentResolution) bool {
	parent := isIdentifier(decision.TargetParentID)
	replyTo := isIdentifier(decision.TargetReplyToUserID)
	switch decision.Action {
	case "set_parent":
		return parent && !replyTo
	case "set_parent_and_reply_to":
		return parent && replyTo
	case "clear_parent":
		return !parent && !replyTo
	case "set_reply_to":
		return !parent && replyTo
	case "clear_reply_to":
		return !parent && !replyTo
	default:
		return false
	}
}

func requireIdentifiers(values ...string) error {
	for _, value := range values {
		if !isIdentifier(value) {
			return gateError("manifest_identifier_empty", "私有清洗 manifest 含空标识")
		}
	}
	return nil
}

// 非 UUID 的来源 URL、词表值与重复组 key 都作为精确 opaque value 比较：
// 不做 trim、大小写折叠或 URL 重写，首尾空白直接拒绝，避免改变来源快照中的 anomaly identity。
func isIdentifier(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}

func requireAction(actual string, allowed ...string) error {
	for _, action := range allowed {
		if actual == action {
			return nil
		}
	}
	return gateError("manifest_action_unknown", "私有清洗 manifest 含未识别 action")
}

func addUnique[K comparable](seen map[K]struct{}, key K) error {
	if _, duplicate := seen[key]; duplicate {
		return manifestDuplicateEntry()
	}
	seen[key] = struct{}{}
	return nil
}

func manifestDuplicateEntry() error {
	return gateError("manifest_entry_duplicate", "私有清洗 manifest 含重复决议")
}

func manifestConflict() error {
	return gateError("manifest_decision_conflict", "私有清洗 manifest 含相互冲突的决议")
}
