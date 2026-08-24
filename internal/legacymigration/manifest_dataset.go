package legacymigration

import "strings"

// ManifestSourceContext 是 dataset adapter 提供的完整来源关系与唯一键占用快照。
// 所有成员只保存 canonical SHA key，不保存邮箱或 UUID 原值。
type ManifestSourceContext struct {
	CommentParentsComplete   bool
	UserEmailsComplete       bool
	PostImageTargetsComplete bool
	CommentParents           []ManifestCommentParentEdge
	UserEmails               []ManifestSourceEmail
	PostImageTargets         []ManifestPostImageTarget
}

// ManifestCommentParentEdge 是来源评论当前的一条非空 parent 边。
type ManifestCommentParentEdge struct {
	Comment CanonicalManifestKey
	Parent  CanonicalManifestKey
}

// ManifestSourceEmail 是来源用户及其迁移前 lower(email) 占用。
type ManifestSourceEmail struct {
	User  CanonicalManifestKey
	Email CanonicalManifestKey
}

// ManifestPostImageTarget 是无需人工决议即可自然映射的 (post_id, image_asset_id) 唯一键。
type ManifestPostImageTarget struct {
	PostAsset CanonicalManifestKey
}

// NewManifestCommentParentEdge 规范化来源评论 parent 边。
func NewManifestCommentParentEdge(commentID, parentID string) (ManifestCommentParentEdge, error) {
	comment, err := canonicalUUIDKey("comment", commentID)
	if err != nil {
		return ManifestCommentParentEdge{}, err
	}
	parent, err := canonicalUUIDKey("comment", parentID)
	if err != nil {
		return ManifestCommentParentEdge{}, err
	}
	return ManifestCommentParentEdge{Comment: comment, Parent: parent}, nil
}

// NewManifestSourceEmail 规范化来源用户与其迁移前邮箱占用。
func NewManifestSourceEmail(userID, email string) (ManifestSourceEmail, error) {
	user, err := canonicalUUIDKey("user", userID)
	if err != nil {
		return ManifestSourceEmail{}, err
	}
	if !isIdentifier(email) {
		return ManifestSourceEmail{}, gateError("manifest_source_context_invalid", "来源邮箱含空值或首尾空白")
	}
	return ManifestSourceEmail{
		User:  user,
		Email: hashCanonicalManifestKey("email_target", strings.ToLower(email)),
	}, nil
}

// NewManifestPostImageTarget 规范化来源自然图片映射的目标唯一键。
func NewManifestPostImageTarget(postID, imageAssetID string) (ManifestPostImageTarget, error) {
	post := postID
	if err := canonicalizeUUID(&post, false); err != nil {
		return ManifestPostImageTarget{}, err
	}
	asset := imageAssetID
	if err := canonicalizeUUID(&asset, false); err != nil {
		return ManifestPostImageTarget{}, err
	}
	return ManifestPostImageTarget{
		PostAsset: hashCanonicalManifestKey("post_image_target", post, asset),
	}, nil
}

func validateManifestAgainstSourceContext(manifest Manifest, source ManifestSourceContext) error {
	if !source.CommentParentsComplete || !source.UserEmailsComplete || !source.PostImageTargetsComplete {
		return gateError("manifest_source_context_incomplete", "coverage 缺少完整来源关系或唯一键占用快照")
	}
	commentParents, err := indexCommentParents(source.CommentParents)
	if err != nil {
		return err
	}
	userEmails, err := indexSourceEmails(source.UserEmails)
	if err != nil {
		return err
	}
	postImageTargets, err := indexPostImageTargets(source.PostImageTargets)
	if err != nil {
		return err
	}
	if err := validateFinalEmailTargets(manifest, userEmails); err != nil {
		return err
	}
	if err := validatePostImageTargetsAgainstSource(manifest.PostImageResolutions, postImageTargets); err != nil {
		return err
	}
	return validateFinalCommentParentGraph(manifest, commentParents)
}

func indexCommentParents(edges []ManifestCommentParentEdge) (
	map[CanonicalManifestKey]CanonicalManifestKey,
	error,
) {
	result := make(map[CanonicalManifestKey]CanonicalManifestKey, len(edges))
	for _, edge := range edges {
		if edge.Comment == (CanonicalManifestKey{}) || edge.Parent == (CanonicalManifestKey{}) {
			return nil, gateError("manifest_source_context_invalid", "来源评论 parent 图含无效 canonical key")
		}
		if _, duplicate := result[edge.Comment]; duplicate {
			return nil, gateError("manifest_source_context_duplicate", "来源评论 parent 图含重复 child")
		}
		result[edge.Comment] = edge.Parent
	}
	return result, nil
}

func indexSourceEmails(entries []ManifestSourceEmail) (
	map[CanonicalManifestKey]CanonicalManifestKey,
	error,
) {
	result := make(map[CanonicalManifestKey]CanonicalManifestKey, len(entries))
	for _, entry := range entries {
		if entry.User == (CanonicalManifestKey{}) || entry.Email == (CanonicalManifestKey{}) {
			return nil, gateError("manifest_source_context_invalid", "来源邮箱占用含无效 canonical key")
		}
		if _, duplicate := result[entry.User]; duplicate {
			return nil, gateError("manifest_source_context_duplicate", "来源邮箱占用含重复 user")
		}
		result[entry.User] = entry.Email
	}
	return result, nil
}

func indexPostImageTargets(entries []ManifestPostImageTarget) (map[CanonicalManifestKey]struct{}, error) {
	result := make(map[CanonicalManifestKey]struct{}, len(entries))
	for _, entry := range entries {
		if entry.PostAsset == (CanonicalManifestKey{}) {
			return nil, gateError("manifest_source_context_invalid", "来源帖子图片映射含无效 canonical key")
		}
		if _, duplicate := result[entry.PostAsset]; duplicate {
			return nil, gateError("manifest_source_context_duplicate", "来源帖子图片映射含重复唯一键")
		}
		result[entry.PostAsset] = struct{}{}
	}
	return result, nil
}

func validateFinalEmailTargets(
	manifest Manifest,
	source map[CanonicalManifestKey]CanonicalManifestKey,
) error {
	final := make(map[CanonicalManifestKey]CanonicalManifestKey, len(source))
	for user, email := range source {
		final[user] = email
	}
	for _, decision := range manifest.ExcludedUsers {
		user, err := canonicalUUIDKey("user", decision.UserID)
		if err != nil {
			return err
		}
		delete(final, user)
	}
	for _, decision := range manifest.EmailRewrites {
		user, err := canonicalUUIDKey("user", decision.UserID)
		if err != nil {
			return err
		}
		if _, exists := final[user]; !exists {
			return manifestConflict()
		}
		final[user] = hashCanonicalManifestKey("email_target", decision.NewEmail)
	}
	owners := make(map[CanonicalManifestKey]CanonicalManifestKey, len(final))
	for user, email := range final {
		if _, duplicate := owners[email]; duplicate {
			return manifestConflict()
		}
		owners[email] = user
	}
	return nil
}

func validatePostImageTargetsAgainstSource(
	decisions []PostImageResolution,
	existing map[CanonicalManifestKey]struct{},
) error {
	for _, decision := range decisions {
		if decision.Action != "map" {
			continue
		}
		target, err := NewManifestPostImageTarget(decision.PostID, decision.TargetImageAssetID)
		if err != nil {
			return err
		}
		if _, occupied := existing[target.PostAsset]; occupied {
			return manifestConflict()
		}
	}
	return nil
}

func validateFinalCommentParentGraph(
	manifest Manifest,
	source map[CanonicalManifestKey]CanonicalManifestKey,
) error {
	edges := make(map[CanonicalManifestKey]CanonicalManifestKey, len(source)+len(manifest.CommentReparentResolutions))
	for comment, parent := range source {
		edges[comment] = parent
	}
	excluded := make(map[CanonicalManifestKey]struct{})
	for _, decision := range manifest.ExcludedContent {
		if decision.ContentType != "comment" {
			continue
		}
		comment, err := canonicalUUIDKey("comment", decision.ContentID)
		if err != nil {
			return err
		}
		excluded[comment] = struct{}{}
		delete(edges, comment)
	}
	for _, decision := range manifest.CommentReparentResolutions {
		comment, err := canonicalUUIDKey("comment", decision.CommentID)
		if err != nil {
			return err
		}
		switch decision.Action {
		case "set_parent", "set_parent_and_reply_to":
			parent, keyErr := canonicalUUIDKey("comment", decision.TargetParentID)
			if keyErr != nil {
				return keyErr
			}
			edges[comment] = parent
		case "clear_parent":
			delete(edges, comment)
		}
	}
	for _, parent := range edges {
		if _, removed := excluded[parent]; removed {
			return manifestConflict()
		}
	}
	return validateCanonicalCommentCycles(edges)
}

func validateCanonicalCommentCycles(edges map[CanonicalManifestKey]CanonicalManifestKey) error {
	states := make(map[CanonicalManifestKey]uint8, len(edges))
	var visit func(CanonicalManifestKey) bool
	visit = func(comment CanonicalManifestKey) bool {
		switch states[comment] {
		case 1:
			return true
		case 2:
			return false
		}
		parent, exists := edges[comment]
		if !exists {
			return false
		}
		states[comment] = 1
		if visit(parent) {
			return true
		}
		states[comment] = 2
		return false
	}
	for comment := range edges {
		if visit(comment) {
			return manifestConflict()
		}
	}
	return nil
}

func canonicalUUIDKey(entity, value string) (CanonicalManifestKey, error) {
	if err := canonicalizeUUID(&value, false); err != nil {
		return CanonicalManifestKey{}, err
	}
	return hashCanonicalManifestKey(entity, value), nil
}
