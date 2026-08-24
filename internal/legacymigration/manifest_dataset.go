package legacymigration

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
)

// ManifestDatasetAdapter 必须枚举完整的最终 migration dataset 及独立发现的 anomalies。
// adapter 不能读取 manifest，也不能按 manifest 决议反向生成 requirements。
type ManifestDatasetAdapter interface {
	PopulateManifestDataset(*ManifestDatasetBuilder) error
}

// ImageAssetPurpose 是 dataset 中图片资产的固定用途。
type ImageAssetPurpose string

const (
	// ImageAssetPurposePost 表示帖子图片。
	ImageAssetPurposePost ImageAssetPurpose = "post"
	// ImageAssetPurposeAvatar 表示头像图片。
	ImageAssetPurposeAvatar ImageAssetPurpose = "avatar"
)

// ManifestDatasetCatalog 是必须显式完成扫描的固定 dataset catalog。
type ManifestDatasetCatalog string

// 固定 catalog code；即使某个 catalog 为空，adapter 也必须逐项标记完成。
const (
	ManifestDatasetCatalogUsers             ManifestDatasetCatalog = "users"
	ManifestDatasetCatalogPosts             ManifestDatasetCatalog = "posts"
	ManifestDatasetCatalogComments          ManifestDatasetCatalog = "comments"
	ManifestDatasetCatalogImageAssets       ManifestDatasetCatalog = "image_assets"
	ManifestDatasetCatalogLikes             ManifestDatasetCatalog = "likes"
	ManifestDatasetCatalogNotifications     ManifestDatasetCatalog = "notifications"
	ManifestDatasetCatalogPostImageRefs     ManifestDatasetCatalog = "post_image_references"
	ManifestDatasetCatalogNaturalPostImages ManifestDatasetCatalog = "natural_post_images"
	ManifestDatasetCatalogNaturalAvatars    ManifestDatasetCatalog = "natural_avatars"
	ManifestDatasetCatalogDictionarySources ManifestDatasetCatalog = "dictionary_sources"
	ManifestDatasetCatalogTargetSeeds       ManifestDatasetCatalog = "target_seeds"
	ManifestDatasetCatalogAnomalies         ManifestDatasetCatalog = "discovered_anomalies"
)

var requiredDatasetCatalogs = []ManifestDatasetCatalog{
	ManifestDatasetCatalogUsers, ManifestDatasetCatalogPosts, ManifestDatasetCatalogComments,
	ManifestDatasetCatalogImageAssets, ManifestDatasetCatalogLikes, ManifestDatasetCatalogNotifications,
	ManifestDatasetCatalogPostImageRefs, ManifestDatasetCatalogNaturalPostImages, ManifestDatasetCatalogNaturalAvatars,
	ManifestDatasetCatalogDictionarySources, ManifestDatasetCatalogTargetSeeds, ManifestDatasetCatalogAnomalies,
}

type (
	sourcePost    struct{ author CanonicalManifestKey }
	sourceComment struct {
		post, author, parent, reply CanonicalManifestKey
		hasParent, hasReply         bool
	}
)

type sourceAsset struct {
	owner   CanonicalManifestKey
	purpose ImageAssetPurpose
}

type sourcePostAsset struct {
	post  CanonicalManifestKey
	asset CanonicalManifestKey
}

type sourcePostImageReference struct {
	post CanonicalManifestKey
}

type manifestSourceContext struct {
	complete          bool
	users             map[CanonicalManifestKey]CanonicalManifestKey
	posts             map[CanonicalManifestKey]sourcePost
	comments          map[CanonicalManifestKey]sourceComment
	assets            map[CanonicalManifestKey]sourceAsset
	likes             map[CanonicalManifestKey]struct{}
	notifications     map[CanonicalManifestKey]struct{}
	postImageRefs     map[CanonicalManifestKey]sourcePostImageReference
	postImageSlots    map[CanonicalManifestKey]sourcePostAsset
	userAvatars       map[CanonicalManifestKey]CanonicalManifestKey
	dictionarySources map[string]map[CanonicalManifestKey]struct{}
	seeds             map[string]map[CanonicalManifestKey]struct{}
	completed         map[ManifestDatasetCatalog]struct{}
}

// ManifestDatasetBuilder 是 dataset adapter 的只写输入面；所有值立即 canonicalize/hash。
type ManifestDatasetBuilder struct {
	source  manifestSourceContext
	entries []ManifestRequirement
	failed  error
}

// BuildManifestRequirements 仅在 adapter 成功枚举完整 dataset 后 seal requirements/context。
func BuildManifestRequirements(adapter ManifestDatasetAdapter, expected ManifestDigest) (ManifestRequirements, error) {
	if expected == (ManifestDigest{}) {
		return ManifestRequirements{}, gateError("manifest_dataset_digest_required", "必须提供独立获批的 dataset snapshot SHA-256")
	}
	if adapter == nil {
		return ManifestRequirements{}, gateError("manifest_source_context_incomplete", "dataset adapter 不能为空")
	}
	builder := newManifestDatasetBuilder()
	if err := adapter.PopulateManifestDataset(builder); err != nil {
		return ManifestRequirements{}, gateError("manifest_dataset_adapter_failed", "dataset adapter 构造 requirements 失败")
	}
	if builder.failed != nil {
		return ManifestRequirements{}, builder.failed
	}
	if err := validateDatasetSnapshot(builder.source); err != nil {
		return ManifestRequirements{}, err
	}
	builder.source.complete = true
	result := ManifestRequirements{entries: cloneRequirements(builder.entries), source: cloneSourceContext(builder.source)}
	result.seal = sealManifestRequirements(result.entries, result.source)
	if !result.seal.equal(expected) {
		return ManifestRequirements{}, gateError("manifest_dataset_digest_mismatch", "dataset snapshot 与独立获批摘要不一致")
	}
	return result, nil
}

func newManifestDatasetBuilder() *ManifestDatasetBuilder {
	return &ManifestDatasetBuilder{source: manifestSourceContext{
		users: make(map[CanonicalManifestKey]CanonicalManifestKey), posts: make(map[CanonicalManifestKey]sourcePost),
		comments: make(map[CanonicalManifestKey]sourceComment), assets: make(map[CanonicalManifestKey]sourceAsset),
		likes: make(map[CanonicalManifestKey]struct{}), notifications: make(map[CanonicalManifestKey]struct{}),
		postImageRefs: make(map[CanonicalManifestKey]sourcePostImageReference), postImageSlots: make(map[CanonicalManifestKey]sourcePostAsset),
		userAvatars:       make(map[CanonicalManifestKey]CanonicalManifestKey),
		dictionarySources: map[string]map[CanonicalManifestKey]struct{}{"canteen": {}, "cuisine": {}, "flavor": {}},
		seeds:             map[string]map[CanonicalManifestKey]struct{}{"canteen": {}, "cuisine": {}, "flavor": {}},
		completed:         make(map[ManifestDatasetCatalog]struct{}),
	}}
}

// MarkCatalogComplete 显式声明一个固定 catalog 已完整扫描；空 catalog 也必须调用。
func (builder *ManifestDatasetBuilder) MarkCatalogComplete(catalog ManifestDatasetCatalog) error {
	valid := false
	for _, required := range requiredDatasetCatalogs {
		if catalog == required {
			valid = true
			break
		}
	}
	if !valid {
		return builder.fail(gateError("manifest_source_context_invalid", "dataset catalog code 不受支持"))
	}
	if _, duplicate := builder.source.completed[catalog]; duplicate {
		return builder.fail(sourceDuplicate())
	}
	builder.source.completed[catalog] = struct{}{}
	return nil
}

func (builder *ManifestDatasetBuilder) fail(err error) error {
	if builder.failed == nil {
		builder.failed = err
	}
	return err
}

// AddUser 登记完整来源用户和迁移前 lower(email) 占用。
func (builder *ManifestDatasetBuilder) AddUser(userID, email string) error {
	key, err := canonicalUUIDKey("user", userID)
	if err != nil {
		return builder.fail(err)
	}
	if !isIdentifier(email) {
		return builder.fail(gateError("manifest_source_context_invalid", "来源 user email 无效"))
	}
	if _, duplicate := builder.source.users[key]; duplicate {
		return builder.fail(sourceDuplicate())
	}
	builder.source.users[key] = hashCanonicalManifestKey("email_target", strings.ToLower(email))
	return nil
}

// AddPost 登记来源帖子及作者。
func (builder *ManifestDatasetBuilder) AddPost(postID, authorID string) error {
	post, err := canonicalUUIDKey("post", postID)
	if err != nil {
		return builder.fail(err)
	}
	author, err := canonicalUUIDKey("user", authorID)
	if err != nil {
		return builder.fail(err)
	}
	if _, duplicate := builder.source.posts[post]; duplicate {
		return builder.fail(sourceDuplicate())
	}
	builder.source.posts[post] = sourcePost{author: author}
	return nil
}

// AddComment 登记来源评论归属、作者、parent 和 reply user。parent/reply 可为空以表达待修复 anomaly。
func (builder *ManifestDatasetBuilder) AddComment(commentID, postID, authorID, parentID, replyUserID string) error {
	comment, err := canonicalUUIDKey("comment", commentID)
	if err != nil {
		return builder.fail(err)
	}
	post, err := canonicalUUIDKey("post", postID)
	if err != nil {
		return builder.fail(err)
	}
	author, err := canonicalUUIDKey("user", authorID)
	if err != nil {
		return builder.fail(err)
	}
	record := sourceComment{post: post, author: author}
	if parentID != "" {
		record.parent, err = canonicalUUIDKey("comment", parentID)
		if err != nil {
			return builder.fail(err)
		}
		record.hasParent = true
	}
	if replyUserID != "" {
		record.reply, err = canonicalUUIDKey("user", replyUserID)
		if err != nil {
			return builder.fail(err)
		}
		record.hasReply = true
	}
	if _, duplicate := builder.source.comments[comment]; duplicate {
		return builder.fail(sourceDuplicate())
	}
	builder.source.comments[comment] = record
	return nil
}

// AddImageAsset 登记来源图片资产的所有者和唯一用途。
func (builder *ManifestDatasetBuilder) AddImageAsset(assetID, ownerID string, purpose ImageAssetPurpose) error {
	if purpose != ImageAssetPurposePost && purpose != ImageAssetPurposeAvatar {
		return builder.fail(gateError("manifest_source_context_invalid", "图片用途不受支持"))
	}
	asset, err := canonicalUUIDKey("asset", assetID)
	if err != nil {
		return builder.fail(err)
	}
	owner, err := canonicalUUIDKey("user", ownerID)
	if err != nil {
		return builder.fail(err)
	}
	if _, duplicate := builder.source.assets[asset]; duplicate {
		return builder.fail(sourceDuplicate())
	}
	builder.source.assets[asset] = sourceAsset{owner: owner, purpose: purpose}
	return nil
}

// AddLike 登记来源点赞。
func (builder *ManifestDatasetBuilder) AddLike(likeID string) error {
	return builder.addUUIDSet(builder.source.likes, "like", likeID)
}

// AddNotification 登记来源通知。
func (builder *ManifestDatasetBuilder) AddNotification(notificationID string) error {
	return builder.addUUIDSet(builder.source.notifications, "notification", notificationID)
}

func (builder *ManifestDatasetBuilder) addUUIDSet(target map[CanonicalManifestKey]struct{}, kind, value string) error {
	key, err := canonicalUUIDKey(kind, value)
	if err != nil {
		return builder.fail(err)
	}
	if _, duplicate := target[key]; duplicate {
		return builder.fail(sourceDuplicate())
	}
	target[key] = struct{}{}
	return nil
}

// AddPostImageReference 登记 dataset 中确实存在的来源图片引用。
func (builder *ManifestDatasetBuilder) AddPostImageReference(postID, sourceReference string) error {
	parts, err := canonicalRequirementParts(ManifestEntityPostImageReference, []string{postID, sourceReference})
	if err != nil {
		return builder.fail(err)
	}
	key := hashCanonicalManifestKey(string(ManifestEntityPostImageReference), parts...)
	if _, duplicate := builder.source.postImageRefs[key]; duplicate {
		return builder.fail(sourceDuplicate())
	}
	post, _ := canonicalUUIDKey("post", postID)
	builder.source.postImageRefs[key] = sourcePostImageReference{post: post}
	return nil
}

// AddNaturalPostImageTarget 登记无需人工决议已占用的 (post,target asset) 唯一键。
func (builder *ManifestDatasetBuilder) AddNaturalPostImageTarget(postID, assetID string) error {
	key, err := postAssetKey(postID, assetID)
	if err != nil {
		return builder.fail(err)
	}
	if _, duplicate := builder.source.postImageSlots[key]; duplicate {
		return builder.fail(sourceDuplicate())
	}
	post, _ := canonicalUUIDKey("post", postID)
	asset, _ := canonicalUUIDKey("asset", assetID)
	builder.source.postImageSlots[key] = sourcePostAsset{post: post, asset: asset}
	return nil
}

// AddNaturalAvatarTarget 登记无需人工决议的现有 user→avatar asset 引用。
func (builder *ManifestDatasetBuilder) AddNaturalAvatarTarget(userID, assetID string) error {
	user, err := canonicalUUIDKey("user", userID)
	if err != nil {
		return builder.fail(err)
	}
	asset, err := canonicalUUIDKey("asset", assetID)
	if err != nil {
		return builder.fail(err)
	}
	if _, duplicate := builder.source.userAvatars[user]; duplicate {
		return builder.fail(sourceDuplicate())
	}
	builder.source.userAvatars[user] = asset
	return nil
}

// AddSeed 登记目标 v11 固定词表。preference_flavor 显式归一为 flavor。
func (builder *ManifestDatasetBuilder) AddSeed(dictionary, value string) error {
	dictionary = normalizeDictionaryKind(dictionary)
	if !validDictionary(dictionary) || !isIdentifier(value) {
		return builder.fail(gateError("manifest_source_context_invalid", "目标词表 seed 无效"))
	}
	key := hashCanonicalManifestKey("seed", dictionary, value)
	if _, duplicate := builder.source.seeds[dictionary][key]; duplicate {
		return builder.fail(sourceDuplicate())
	}
	builder.source.seeds[dictionary][key] = struct{}{}
	return nil
}

// AddDictionarySource 登记旧库中真实存在的来源词表值。
func (builder *ManifestDatasetBuilder) AddDictionarySource(dictionary, value string) error {
	dictionary = normalizeDictionaryKind(dictionary)
	if !validDictionary(dictionary) || !isIdentifier(value) {
		return builder.fail(gateError("manifest_source_context_invalid", "来源词表值无效"))
	}
	key := hashCanonicalManifestKey("dictionary_source", dictionary, value)
	if _, duplicate := builder.source.dictionarySources[dictionary][key]; duplicate {
		return builder.fail(sourceDuplicate())
	}
	builder.source.dictionarySources[dictionary][key] = struct{}{}
	return nil
}

// AddAnomaly 登记独立发现的 anomaly 和所有获批替代决议。
func (builder *ManifestDatasetBuilder) AddAnomaly(anomalyCode string, entity ManifestEntity, identifiers []string, allowed ...ManifestDecisionOption) error {
	requirement, err := NewManifestRequirement(anomalyCode, entity, identifiers, allowed...)
	if err != nil {
		return builder.fail(err)
	}
	builder.entries = append(builder.entries, requirement)
	return nil
}

// Format 阻止 fmt 输出尚未 seal 的 dataset 输入。
func (ManifestDatasetBuilder) Format(state fmt.State, _ rune) {
	_, _ = fmt.Fprint(state, "<manifest-dataset-builder:redacted>")
}

// MarshalJSON 只返回固定脱敏标记。
func (ManifestDatasetBuilder) MarshalJSON() ([]byte, error) {
	return []byte(`{"type":"manifest_dataset_builder","redacted":true}`), nil
}

func (requirements ManifestRequirements) verify() ([]ManifestRequirement, manifestSourceContext, error) {
	if requirements.seal == (ManifestDigest{}) || !requirements.source.complete {
		return nil, manifestSourceContext{}, gateError("manifest_source_context_incomplete", "coverage 缺少由完整 dataset adapter seal 的上下文")
	}
	seal := sealManifestRequirements(requirements.entries, requirements.source)
	if !seal.equal(requirements.seal) {
		return nil, manifestSourceContext{}, gateError("manifest_requirements_tampered", "dataset requirements/context 在 seal 后发生变异")
	}
	return cloneRequirements(requirements.entries), cloneSourceContext(requirements.source), nil
}

func sealManifestRequirements(entries []ManifestRequirement, source manifestSourceContext) ManifestDigest {
	parts := make([]string, 0, len(entries)+len(source.users)+len(source.posts)+len(source.comments)+len(source.assets))
	for _, entry := range entries {
		allowed := make([]string, 0, len(entry.allowed))
		for _, option := range entry.allowed {
			allowed = append(allowed, option.category+"/"+option.action)
		}
		sort.Strings(allowed)
		parts = append(parts, "r/"+entry.anomaly.String()+"/"+string(entry.entity)+"/"+entry.entityKey.String()+"/"+strings.Join(allowed, ","))
	}
	for key, email := range source.users {
		parts = append(parts, "u/"+key.String()+"/"+email.String())
	}
	for key, post := range source.posts {
		parts = append(parts, "p/"+key.String()+"/"+post.author.String())
	}
	for key, comment := range source.comments {
		parts = append(parts, fmt.Sprintf("c/%s/%s/%s/%t/%s/%t/%s", key.String(), comment.post.String(), comment.author.String(), comment.hasParent, comment.parent.String(), comment.hasReply, comment.reply.String()))
	}
	for key, asset := range source.assets {
		parts = append(parts, "a/"+key.String()+"/"+asset.owner.String()+"/"+string(asset.purpose))
	}
	for key := range source.likes {
		parts = append(parts, "l/"+key.String())
	}
	for key := range source.notifications {
		parts = append(parts, "n/"+key.String())
	}
	for key, value := range source.postImageRefs {
		parts = append(parts, "ir/"+key.String()+"/"+value.post.String())
	}
	for key, value := range source.postImageSlots {
		parts = append(parts, "is/"+key.String()+"/"+value.post.String()+"/"+value.asset.String())
	}
	for user, asset := range source.userAvatars {
		parts = append(parts, "ua/"+user.String()+"/"+asset.String())
	}
	for kind, values := range source.dictionarySources {
		for key := range values {
			parts = append(parts, "ds/"+kind+"/"+key.String())
		}
	}
	for kind, seeds := range source.seeds {
		for key := range seeds {
			parts = append(parts, "s/"+kind+"/"+key.String())
		}
	}
	if source.complete {
		parts = append(parts, "complete")
	}
	for catalog := range source.completed {
		parts = append(parts, "catalog/"+string(catalog))
	}
	sort.Strings(parts)
	hasher := sha256.New()
	_, _ = hasher.Write([]byte("danshi-manifest-requirements-v1"))
	for _, part := range parts {
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write([]byte(part))
	}
	var digest ManifestDigest
	copy(digest[:], hasher.Sum(nil))
	return digest
}

func cloneRequirements(entries []ManifestRequirement) []ManifestRequirement {
	result := make([]ManifestRequirement, len(entries))
	copy(result, entries)
	for index := range result {
		result[index].allowed = append([]ManifestDecisionOption(nil), entries[index].allowed...)
	}
	return result
}

func cloneSourceContext(source manifestSourceContext) manifestSourceContext {
	result := newManifestDatasetBuilder().source
	result.complete = source.complete
	for key, value := range source.users {
		result.users[key] = value
	}
	for key, value := range source.posts {
		result.posts[key] = value
	}
	for key, value := range source.comments {
		result.comments[key] = value
	}
	for key, value := range source.assets {
		result.assets[key] = value
	}
	for key := range source.likes {
		result.likes[key] = struct{}{}
	}
	for key := range source.notifications {
		result.notifications[key] = struct{}{}
	}
	for key, value := range source.postImageRefs {
		result.postImageRefs[key] = value
	}
	for key, value := range source.postImageSlots {
		result.postImageSlots[key] = value
	}
	for user, asset := range source.userAvatars {
		result.userAvatars[user] = asset
	}
	for kind, values := range source.dictionarySources {
		for key := range values {
			result.dictionarySources[kind][key] = struct{}{}
		}
	}
	for kind, values := range source.seeds {
		for key := range values {
			result.seeds[kind][key] = struct{}{}
		}
	}
	for catalog := range source.completed {
		result.completed[catalog] = struct{}{}
	}
	return result
}

func validateDatasetSnapshot(source manifestSourceContext) error {
	for _, catalog := range requiredDatasetCatalogs {
		if _, complete := source.completed[catalog]; !complete {
			return gateError("manifest_source_context_incomplete", "dataset catalog 未显式完成")
		}
	}
	for _, post := range source.posts {
		if _, exists := source.users[post.author]; !exists {
			return gateError("manifest_source_context_invalid", "dataset post author 不存在")
		}
	}
	for _, asset := range source.assets {
		if _, exists := source.users[asset.owner]; !exists {
			return gateError("manifest_source_context_invalid", "dataset image asset owner 不存在")
		}
	}
	for _, reference := range source.postImageRefs {
		if _, exists := source.posts[reference.post]; !exists {
			return gateError("manifest_source_context_invalid", "dataset post image reference 的 post 不存在")
		}
	}
	for _, natural := range source.postImageSlots {
		post, postExists := source.posts[natural.post]
		asset, assetExists := source.assets[natural.asset]
		if !postExists || !assetExists || asset.purpose != ImageAssetPurposePost || asset.owner != post.author {
			return gateError("manifest_source_context_invalid", "dataset 自然帖子图片引用无效")
		}
	}
	for user, assetKey := range source.userAvatars {
		asset, assetExists := source.assets[assetKey]
		if _, userExists := source.users[user]; !userExists || !assetExists || asset.purpose != ImageAssetPurposeAvatar || asset.owner != user {
			return gateError("manifest_source_context_invalid", "dataset 自然头像引用无效")
		}
	}
	return nil
}

func validateManifestAgainstSourceContext(manifest manifestData, source manifestSourceContext) error {
	if !source.complete {
		return gateError("manifest_source_context_incomplete", "coverage 缺少完整 dataset source context")
	}
	excludedUsers, excludedPosts, excludedComments, excludedAssets, err := validateDecisionExistence(manifest, source)
	if err != nil {
		return err
	}
	if err := validateFinalEmailTargets(manifest, source.users, excludedUsers); err != nil {
		return err
	}
	if err := validateRetainedOwnership(source, excludedUsers, excludedPosts, excludedComments); err != nil {
		return err
	}
	if err := validateAssetsAndSeeds(manifest, source, excludedUsers, excludedPosts, excludedAssets); err != nil {
		return err
	}
	return validateFinalCommentGraph(manifest, source, excludedUsers, excludedPosts, excludedComments)
}

func validateDecisionExistence(manifest manifestData, source manifestSourceContext) (map[CanonicalManifestKey]struct{}, map[CanonicalManifestKey]struct{}, map[CanonicalManifestKey]struct{}, map[CanonicalManifestKey]struct{}, error) {
	eu, ep, ec, ea := map[CanonicalManifestKey]struct{}{}, map[CanonicalManifestKey]struct{}{}, map[CanonicalManifestKey]struct{}{}, map[CanonicalManifestKey]struct{}{}
	for _, d := range manifest.ExcludedUsers {
		key, _ := canonicalUUIDKey("user", d.UserID)
		if _, ok := source.users[key]; !ok {
			return nil, nil, nil, nil, sourceMissing()
		}
		eu[key] = struct{}{}
	}
	for _, d := range manifest.ExcludedContent {
		key, _ := canonicalUUIDKey(d.ContentType, d.ContentID)
		if d.ContentType == "post" {
			if _, ok := source.posts[key]; !ok {
				return nil, nil, nil, nil, sourceMissing()
			}
			ep[key] = struct{}{}
		} else {
			if _, ok := source.comments[key]; !ok {
				return nil, nil, nil, nil, sourceMissing()
			}
			ec[key] = struct{}{}
		}
	}
	for _, d := range manifest.EmailRewrites {
		key, _ := canonicalUUIDKey("user", d.UserID)
		if _, ok := source.users[key]; !ok {
			return nil, nil, nil, nil, sourceMissing()
		}
	}
	for _, d := range manifest.PostTypeResolutions {
		key, _ := canonicalUUIDKey("post", d.PostID)
		if _, ok := source.posts[key]; !ok {
			return nil, nil, nil, nil, sourceMissing()
		}
	}
	for _, d := range manifest.DictionaryMappings {
		kind := normalizeDictionaryKind(d.Dictionary)
		key := hashCanonicalManifestKey("dictionary_source", kind, d.Source)
		if _, exists := source.dictionarySources[kind][key]; !exists {
			return nil, nil, nil, nil, sourceMissing()
		}
	}
	for _, d := range manifest.PostImageResolutions {
		parts, _ := canonicalRequirementParts(ManifestEntityPostImageReference, []string{d.PostID, d.SourceReference})
		ref := hashCanonicalManifestKey(string(ManifestEntityPostImageReference), parts...)
		reference, ok := source.postImageRefs[ref]
		post, _ := canonicalUUIDKey("post", d.PostID)
		if !ok || reference.post != post {
			return nil, nil, nil, nil, sourceMissing()
		}
		if _, postExists := source.posts[post]; !postExists {
			return nil, nil, nil, nil, sourceMissing()
		}
	}
	for _, d := range manifest.AvatarResolutions {
		key, _ := canonicalUUIDKey("user", d.UserID)
		if _, ok := source.users[key]; !ok {
			return nil, nil, nil, nil, sourceMissing()
		}
	}
	for _, d := range manifest.DuplicateImageAssetResolutions {
		key, _ := canonicalUUIDKey("asset", d.ImageAssetID)
		if _, ok := source.assets[key]; !ok {
			return nil, nil, nil, nil, sourceMissing()
		}
		if d.Action == "exclude" {
			ea[key] = struct{}{}
		}
	}
	for _, d := range manifest.CommentReparentResolutions {
		key, _ := canonicalUUIDKey("comment", d.CommentID)
		if _, ok := source.comments[key]; !ok {
			return nil, nil, nil, nil, sourceMissing()
		}
	}
	for _, d := range manifest.OrphanLikeExclusions {
		key, _ := canonicalUUIDKey("like", d.LikeID)
		if _, ok := source.likes[key]; !ok {
			return nil, nil, nil, nil, sourceMissing()
		}
	}
	for _, d := range manifest.OrphanNotificationExclusions {
		key, _ := canonicalUUIDKey("notification", d.NotificationID)
		if _, ok := source.notifications[key]; !ok {
			return nil, nil, nil, nil, sourceMissing()
		}
	}
	return eu, ep, ec, ea, nil
}

func validateFinalEmailTargets(manifest manifestData, users map[CanonicalManifestKey]CanonicalManifestKey, excluded map[CanonicalManifestKey]struct{}) error {
	final := make(map[CanonicalManifestKey]CanonicalManifestKey, len(users))
	for user, email := range users {
		if _, skip := excluded[user]; !skip {
			final[user] = email
		}
	}
	for _, d := range manifest.EmailRewrites {
		user, _ := canonicalUUIDKey("user", d.UserID)
		if _, ok := final[user]; !ok {
			return manifestConflict()
		}
		final[user] = hashCanonicalManifestKey("email_target", d.NewEmail)
	}
	owners := map[CanonicalManifestKey]struct{}{}
	for _, email := range final {
		if _, dup := owners[email]; dup {
			return manifestConflict()
		}
		owners[email] = struct{}{}
	}
	return nil
}

func validateRetainedOwnership(source manifestSourceContext, excludedUsers, excludedPosts, excludedComments map[CanonicalManifestKey]struct{}) error {
	for post, record := range source.posts {
		if _, skip := excludedPosts[post]; skip {
			continue
		}
		if _, exists := source.users[record.author]; !exists {
			return sourceMissing()
		}
		if _, removed := excludedUsers[record.author]; removed {
			return manifestConflict()
		}
	}
	for comment, record := range source.comments {
		if _, skip := excludedComments[comment]; skip {
			continue
		}
		if _, exists := source.users[record.author]; !exists {
			return sourceMissing()
		}
		if _, removed := excludedUsers[record.author]; removed {
			return manifestConflict()
		}
		if _, exists := source.posts[record.post]; !exists {
			return sourceMissing()
		}
		if _, removed := excludedPosts[record.post]; removed {
			return manifestConflict()
		}
	}
	for _, record := range source.assets {
		if _, exists := source.users[record.owner]; !exists {
			return sourceMissing()
		}
	}
	return nil
}

func validateAssetsAndSeeds(manifest manifestData, source manifestSourceContext, excludedUsers, excludedPosts, excludedAssets map[CanonicalManifestKey]struct{}) error {
	for _, natural := range source.postImageSlots {
		post, postExists := source.posts[natural.post]
		asset, assetExists := source.assets[natural.asset]
		if !postExists || !assetExists {
			return sourceMissing()
		}
		if asset.purpose != ImageAssetPurposePost || asset.owner != post.author {
			return gateError("manifest_source_context_invalid", "自然帖子图片的用途或所有者无效")
		}
		if _, postRemoved := excludedPosts[natural.post]; !postRemoved {
			if _, assetRemoved := excludedAssets[natural.asset]; assetRemoved {
				return manifestConflict()
			}
		}
	}
	for user, asset := range source.userAvatars {
		if _, userRemoved := excludedUsers[user]; !userRemoved {
			if _, assetRemoved := excludedAssets[asset]; assetRemoved {
				return manifestConflict()
			}
		}
	}
	for _, d := range manifest.DictionaryMappings {
		kind := normalizeDictionaryKind(d.Dictionary)
		key := hashCanonicalManifestKey("seed", kind, d.Target)
		if _, ok := source.seeds[kind][key]; !ok {
			return gateError("manifest_dictionary_target_missing", "词表 mapping 目标不在 v11 seed catalog")
		}
	}
	for _, d := range manifest.PostImageResolutions {
		if d.Action != "map" {
			continue
		}
		post, _ := canonicalUUIDKey("post", d.PostID)
		if _, removed := excludedPosts[post]; removed {
			return manifestConflict()
		}
		asset, _ := canonicalUUIDKey("asset", d.TargetImageAssetID)
		record, ok := source.assets[asset]
		if !ok {
			return targetMissing()
		}
		if _, removed := excludedAssets[asset]; removed {
			return manifestConflict()
		}
		if record.purpose != ImageAssetPurposePost {
			return gateError("manifest_asset_purpose_invalid", "图片目标用途不匹配")
		}
		if record.owner != source.posts[post].author {
			return gateError("manifest_asset_owner_invalid", "帖子图片目标所有者不匹配")
		}
		slot, _ := postAssetKey(d.PostID, d.TargetImageAssetID)
		if _, occupied := source.postImageSlots[slot]; occupied {
			return manifestConflict()
		}
	}
	for _, d := range manifest.AvatarResolutions {
		if d.Action != "replace" {
			continue
		}
		user, _ := canonicalUUIDKey("user", d.UserID)
		if _, removed := excludedUsers[user]; removed {
			return manifestConflict()
		}
		asset, _ := canonicalUUIDKey("asset", d.TargetImageAssetID)
		record, ok := source.assets[asset]
		if !ok {
			return targetMissing()
		}
		if _, removed := excludedAssets[asset]; removed {
			return manifestConflict()
		}
		if record.purpose != ImageAssetPurposeAvatar {
			return gateError("manifest_asset_purpose_invalid", "头像目标用途不匹配")
		}
		if record.owner != user {
			return gateError("manifest_asset_owner_invalid", "头像目标所有者不匹配")
		}
	}
	return nil
}

func validateFinalCommentGraph(manifest manifestData, source manifestSourceContext, excludedUsers, excludedPosts, excludedComments map[CanonicalManifestKey]struct{}) error {
	final := make(map[CanonicalManifestKey]sourceComment, len(source.comments))
	for key, value := range source.comments {
		if _, skip := excludedComments[key]; !skip {
			final[key] = value
		}
	}
	for _, d := range manifest.CommentReparentResolutions {
		comment, _ := canonicalUUIDKey("comment", d.CommentID)
		record := final[comment]
		switch d.Action {
		case "set_parent":
			record.parent, _ = canonicalUUIDKey("comment", d.TargetParentID)
			record.hasParent = true
		case "set_parent_and_reply_to":
			record.parent, _ = canonicalUUIDKey("comment", d.TargetParentID)
			record.reply, _ = canonicalUUIDKey("user", d.TargetReplyToUserID)
			record.hasParent, record.hasReply = true, true
		case "clear_parent":
			record.parent = CanonicalManifestKey{}
			record.hasParent = false
		case "set_reply_to":
			record.reply, _ = canonicalUUIDKey("user", d.TargetReplyToUserID)
			record.hasReply = true
		case "clear_reply_to":
			record.reply = CanonicalManifestKey{}
			record.hasReply = false
		}
		final[comment] = record
	}
	edges := map[CanonicalManifestKey]CanonicalManifestKey{}
	for comment, record := range final {
		post, postExists := source.posts[record.post]
		if !postExists {
			return sourceMissing()
		}
		if _, removed := excludedPosts[record.post]; removed {
			return manifestConflict()
		}
		if _, exists := source.users[record.author]; !exists {
			return sourceMissing()
		}
		if _, removed := excludedUsers[record.author]; removed {
			return manifestConflict()
		}
		expectedReply := post.author
		if record.hasParent {
			parent, parentExists := final[record.parent]
			if !parentExists {
				return targetMissing()
			}
			if parent.post != record.post {
				return gateError("manifest_comment_cross_post", "评论 parent 不属于同一帖子")
			}
			expectedReply = parent.author
			edges[comment] = record.parent
		}
		if !record.hasReply {
			return gateError("manifest_comment_reply_invalid", "保留评论缺少最终 reply user")
		}
		if _, exists := source.users[record.reply]; !exists {
			return targetMissing()
		}
		if _, removed := excludedUsers[record.reply]; removed {
			return manifestConflict()
		}
		if record.reply != expectedReply {
			return gateError("manifest_comment_reply_invalid", "评论 reply user 与最终 parent/post author 不符")
		}
	}
	return validateCanonicalCommentCycles(edges)
}

func validateCanonicalCommentCycles(edges map[CanonicalManifestKey]CanonicalManifestKey) error {
	states := map[CanonicalManifestKey]uint8{}
	var visit func(CanonicalManifestKey) bool
	visit = func(key CanonicalManifestKey) bool {
		if states[key] == 1 {
			return true
		}
		if states[key] == 2 {
			return false
		}
		parent, ok := edges[key]
		if !ok {
			return false
		}
		states[key] = 1
		if visit(parent) {
			return true
		}
		states[key] = 2
		return false
	}
	for key := range edges {
		if visit(key) {
			return manifestConflict()
		}
	}
	return nil
}

func postAssetKey(postID, assetID string) (CanonicalManifestKey, error) {
	if err := canonicalizeUUID(&postID, false); err != nil {
		return CanonicalManifestKey{}, err
	}
	if err := canonicalizeUUID(&assetID, false); err != nil {
		return CanonicalManifestKey{}, err
	}
	return hashCanonicalManifestKey("post_image_target", postID, assetID), nil
}

func canonicalUUIDKey(entity, value string) (CanonicalManifestKey, error) {
	if err := canonicalizeUUID(&value, false); err != nil {
		return CanonicalManifestKey{}, err
	}
	return hashCanonicalManifestKey(entity, value), nil
}

func sourceDuplicate() error {
	return gateError("manifest_source_context_duplicate", "dataset source context 含重复 identity")
}

func sourceMissing() error {
	return gateError("manifest_decision_source_missing", "manifest 决议来源实体不存在")
}

func targetMissing() error {
	return gateError("manifest_decision_target_missing", "manifest 决议目标实体不存在")
}
