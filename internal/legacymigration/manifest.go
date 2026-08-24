package legacymigration

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
)

const (
	// ManifestSchemaVersion 是私有清洗决议文件当前唯一支持的结构版本。
	ManifestSchemaVersion = 1
	// MaxManifestBytes 限制清洗决议文件大小，避免错误路径或异常文件耗尽内存。
	MaxManifestBytes int64 = 1 << 20
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
)

// Manifest 是受权限保护的旧库清洗决议。它可以包含私有来源标识，禁止直接写入日志或公开报告。
type Manifest struct {
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
func (manifest Manifest) Summary() ManifestSummary {
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
// 返回错误只包含固定 code，不包含路径、manifest 值或底层系统/JSON 错误。
func LoadManifest(path string) (Manifest, error) {
	if strings.TrimSpace(path) == "" {
		return Manifest{}, gateError("manifest_path_missing", "必须提供私有清洗 manifest 路径")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return Manifest{}, gateError("manifest_stat_failed", "无法核验私有清洗 manifest")
	}
	if !pathInfo.Mode().IsRegular() {
		return Manifest{}, gateError("manifest_not_regular", "私有清洗 manifest 必须是普通文件")
	}
	if pathInfo.Mode().Perm()&0o077 != 0 {
		return Manifest{}, gateError("manifest_permissions_too_open", "私有清洗 manifest 不得向 group 或 other 开放权限")
	}
	if pathInfo.Size() > MaxManifestBytes {
		return Manifest{}, gateError("manifest_too_large", "私有清洗 manifest 超过大小上限")
	}

	file, err := os.Open(path)
	if err != nil {
		return Manifest{}, gateError("manifest_read_failed", "无法读取私有清洗 manifest")
	}
	defer func() { _ = file.Close() }()
	openInfo, err := file.Stat()
	if err != nil {
		return Manifest{}, gateError("manifest_read_failed", "无法读取私有清洗 manifest")
	}
	if !openInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openInfo) {
		return Manifest{}, gateError("manifest_file_changed", "私有清洗 manifest 在加载期间发生变化")
	}
	if openInfo.Mode().Perm()&0o077 != 0 {
		return Manifest{}, gateError("manifest_permissions_too_open", "私有清洗 manifest 不得向 group 或 other 开放权限")
	}
	if openInfo.Size() > MaxManifestBytes {
		return Manifest{}, gateError("manifest_too_large", "私有清洗 manifest 超过大小上限")
	}

	data, err := io.ReadAll(io.LimitReader(file, MaxManifestBytes+1))
	if err != nil {
		return Manifest{}, gateError("manifest_read_failed", "无法读取私有清洗 manifest")
	}
	if int64(len(data)) > MaxManifestBytes {
		return Manifest{}, gateError("manifest_too_large", "私有清洗 manifest 超过大小上限")
	}
	return decodeManifest(data)
}

func decodeManifest(data []byte) (Manifest, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		if errors.Is(err, errDuplicateManifestKey) {
			return Manifest{}, gateError("manifest_duplicate_key", "私有清洗 manifest 含重复 JSON key")
		}
		return Manifest{}, gateError("manifest_invalid_json", "私有清洗 manifest 不是合法 JSON")
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return Manifest{}, gateError("manifest_invalid_json", "私有清洗 manifest 顶层必须是 JSON object")
	}
	if _, exists := fields["schema_version"]; !exists {
		return Manifest{}, gateError("manifest_schema_version_missing", "私有清洗 manifest 缺少 schema_version")
	}
	for _, section := range requiredManifestSections {
		raw, exists := fields[section]
		if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return Manifest{}, gateError("manifest_section_missing", "私有清洗 manifest 必须显式包含全部 section，空 section 也必须写 []")
		}
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, gateError("manifest_schema_invalid", "私有清洗 manifest 不符合 v1 结构")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Manifest{}, gateError("manifest_invalid_json", "私有清洗 manifest 只能包含一个 JSON object")
	}
	if manifest.SchemaVersion != ManifestSchemaVersion {
		return Manifest{}, gateError("manifest_schema_version_unsupported", "私有清洗 manifest schema_version 不受支持")
	}
	if err := manifest.validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
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
			if _, exists := seen[key]; exists {
				return errDuplicateManifestKey
			}
			seen[key] = struct{}{}
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

func ensureJSONEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("trailing JSON value")
	}
	return nil
}

func (manifest Manifest) validate() error {
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
		if err := requireAction(decision.Action, "set_parent", "clear_parent", "set_reply_to", "clear_reply_to"); err != nil {
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
		return parent
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
