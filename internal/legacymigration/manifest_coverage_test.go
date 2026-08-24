package legacymigration

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type manifestAdapterFunc func(*ManifestDatasetBuilder) error

func (function manifestAdapterFunc) PopulateManifestDataset(builder *ManifestDatasetBuilder) error {
	return function(builder)
}

func TestValidateCoverageAcceptsAlternativePostDecisions(t *testing.T) {
	userID, postID := syntheticUUID(1), syntheticUUID(2)
	setType := mustOption(t, "post_type_resolutions", "set_type")
	exclude := mustOption(t, "excluded_content", "exclude")
	requirements := mustBuildRequirements(t, func(builder *ManifestDatasetBuilder) error {
		if err := builder.AddUser(userID, "owner@example.invalid"); err != nil {
			return err
		}
		if err := builder.AddPost(postID, userID); err != nil {
			return err
		}
		return builder.AddAnomaly("invalid_post_type", ManifestEntityPost, []string{postID}, setType, exclude)
	})

	tests := []manifestData{
		{PostTypeResolutions: []PostTypeResolution{{PostID: postID, Action: "set_type", TargetType: "share"}}},
		{ExcludedContent: []ExcludedContentDecision{{ContentType: "post", ContentID: postID, Action: "exclude"}}},
	}
	for index, manifest := range tests {
		approved, digest := approveManifestData(t, manifest)
		summary, err := ValidateCoverage(approved, digest, requirements)
		if err != nil {
			t.Fatalf("alternative %d: %v", index, err)
		}
		if coverageCount(summary, "matched") != 1 {
			t.Fatalf("未匹配获批替代方案：%+v", summary)
		}
	}
}

func TestValidateCoverageAcceptsEmailAndCommentAlternatives(t *testing.T) {
	userID, authorID, postID, commentID := syntheticUUID(1), syntheticUUID(2), syntheticUUID(3), syntheticUUID(4)
	rewrite := mustOption(t, "email_rewrites", "rewrite")
	excludeUser := mustOption(t, "excluded_users", "exclude")
	reparent := mustOption(t, "comment_reparent_resolutions", "set_reply_to")
	excludeComment := mustOption(t, "excluded_content", "exclude")
	base := func(builder *ManifestDatasetBuilder) error {
		if err := builder.AddUser(userID, "conflict@example.invalid"); err != nil {
			return err
		}
		if err := builder.AddUser(authorID, "author@example.invalid"); err != nil {
			return err
		}
		if err := builder.AddPost(postID, authorID); err != nil {
			return err
		}
		if err := builder.AddComment(commentID, postID, userID, "", authorID); err != nil {
			return err
		}
		if err := builder.AddAnomaly("email_conflict", ManifestEntityUser, []string{userID}, rewrite, excludeUser); err != nil {
			return err
		}
		return builder.AddAnomaly("comment_parent_anomaly", ManifestEntityComment, []string{commentID}, reparent, excludeComment)
	}
	requirements := mustBuildRequirements(t, base)
	manifest := manifestData{
		EmailRewrites:   []EmailRewriteDecision{{UserID: userID, Action: "rewrite", NewEmail: "fixed@example.invalid"}},
		ExcludedContent: []ExcludedContentDecision{{ContentType: "comment", ContentID: commentID, Action: "exclude"}},
	}
	approved, digest := approveManifestData(t, manifest)
	if _, err := ValidateCoverage(approved, digest, requirements); err != nil {
		t.Fatalf("合法跨 anomaly 替代决议失败：%v", err)
	}
}

func TestCompareCoverageRejectsDoubleResolutionAndWrongCategory(t *testing.T) {
	postID := syntheticUUID(1)
	setType := mustOption(t, "post_type_resolutions", "set_type")
	exclude := mustOption(t, "excluded_content", "exclude")
	requirement, err := NewManifestRequirement("invalid_post_type", ManifestEntityPost, []string{postID}, setType, exclude)
	if err != nil {
		t.Fatal(err)
	}
	parts, _ := canonicalRequirementParts(ManifestEntityPost, []string{postID})
	key := hashCanonicalManifestKey(string(ManifestEntityPost), parts...)
	actual := []manifestDecision{{entity: ManifestEntityPost, key: key, option: setType}, {entity: ManifestEntityPost, key: key, option: exclude}}
	summary, _, duplicate, err := compareCoverage(actual, []ManifestRequirement{requirement})
	if err != nil || duplicate != 1 || coverageCount(summary, "duplicate_manifest_decisions") != 1 {
		t.Fatalf("双重决议未被 exact-set 拒绝：%v %+v", err, summary)
	}

	email := mustOption(t, "email_rewrites", "rewrite")
	summary, _, _, err = compareCoverage([]manifestDecision{{entity: ManifestEntityPost, key: key, option: email}}, []ManifestRequirement{requirement})
	if err != nil || coverageCount(summary, "wrong_category") != 1 {
		t.Fatalf("错误 category 未识别：%v %+v", err, summary)
	}
}

func TestCompareCoverageUsesMaximumMatchingForOverlappingAlternatives(t *testing.T) {
	postID := syntheticUUID(1)
	setType := mustOption(t, "post_type_resolutions", "set_type")
	exclude := mustOption(t, "excluded_content", "exclude")
	broad, err := NewManifestRequirement("broad", ManifestEntityPost, []string{postID}, setType, exclude)
	if err != nil {
		t.Fatal(err)
	}
	narrow, err := NewManifestRequirement("narrow", ManifestEntityPost, []string{postID}, setType)
	if err != nil {
		t.Fatal(err)
	}
	parts, _ := canonicalRequirementParts(ManifestEntityPost, []string{postID})
	key := hashCanonicalManifestKey(string(ManifestEntityPost), parts...)
	actual := []manifestDecision{
		{entity: ManifestEntityPost, key: key, option: setType},
		{entity: ManifestEntityPost, key: key, option: exclude},
	}
	summary, _, duplicate, err := compareCoverage(actual, []ManifestRequirement{broad, narrow})
	if err != nil || duplicate != 0 || coverageCount(summary, "matched") != 2 {
		t.Fatalf("重叠 allowed 集未做最大匹配：%v %+v", err, summary)
	}
}

func TestValidateCoverageRequiresDigestAndDetectsApprovedMutation(t *testing.T) {
	requirements := mustBuildRequirements(t, func(*ManifestDatasetBuilder) error { return nil })
	approved, digest := approveManifestData(t, manifestData{})
	if _, err := ValidateCoverage(approved, ManifestDigest{}, requirements); err == nil || err.Error() != "manifest_digest_required" {
		t.Fatalf("遗漏 digest 未拒绝：%v", err)
	}
	wrong := ManifestDigest(sha256.Sum256([]byte("wrong")))
	if _, err := ValidateCoverage(approved, wrong, requirements); err == nil || err.Error() != "manifest_digest_mismatch" {
		t.Fatalf("错误 digest 未拒绝：%v", err)
	}
	approved.data.ExcludedUsers = append(approved.data.ExcludedUsers, ExcludedUserDecision{UserID: syntheticUUID(1), Action: "exclude"})
	if _, err := ValidateCoverage(approved, digest, requirements); err == nil || err.Error() != "approved_manifest_tampered" {
		t.Fatalf("加载后 mutation 未被 seal 捕获：%v", err)
	}
}

func TestRequirementsMutationAndFormattingAreFailClosedAndRedacted(t *testing.T) {
	userID := syntheticUUID(1)
	requirements := mustBuildRequirements(t, func(builder *ManifestDatasetBuilder) error {
		if err := builder.AddUser(userID, "private@example.invalid"); err != nil {
			return err
		}
		return builder.AddAnomaly("private-anomaly", ManifestEntityUser, []string{userID}, mustOption(t, "excluded_users", "exclude"))
	})
	approved, digest := approveManifestData(t, manifestData{ExcludedUsers: []ExcludedUserDecision{{UserID: userID, Action: "exclude"}}})
	encoded, _ := json.Marshal(requirements)
	formatted := fmt.Sprintf("%+v", requirements)
	for _, forbidden := range []string{userID, "private@example.invalid", "private-anomaly"} {
		if strings.Contains(string(encoded), forbidden) || strings.Contains(formatted, forbidden) {
			t.Fatalf("requirements 泄露私有值：%s / %s", encoded, formatted)
		}
	}
	requirements.entries[0].allowed[0].action = "rewrite"
	if _, err := ValidateCoverage(approved, digest, requirements); err == nil || err.Error() != "manifest_requirements_tampered" {
		t.Fatalf("requirements mutation 未被 seal 捕获：%v", err)
	}
}

func TestValidateCoverageFailsMissingUnusedAndWrongCategory(t *testing.T) {
	postID, userID := syntheticUUID(1), syntheticUUID(2)
	setType := mustOption(t, "post_type_resolutions", "set_type")
	requirements := mustBuildRequirements(t, func(builder *ManifestDatasetBuilder) error {
		if err := builder.AddUser(userID, "owner@example.invalid"); err != nil {
			return err
		}
		if err := builder.AddPost(postID, userID); err != nil {
			return err
		}
		return builder.AddAnomaly("post_type", ManifestEntityPost, []string{postID}, setType)
	})
	approved, digest := approveManifestData(t, manifestData{})
	summary, err := ValidateCoverage(approved, digest, requirements)
	if err == nil || err.Error() != "manifest_coverage_mismatch" || coverageCount(summary, "missing") != 1 {
		t.Fatalf("missing 未拒绝：%v %+v", err, summary)
	}

	emptyRequirements := mustBuildRequirements(t, func(builder *ManifestDatasetBuilder) error {
		if err := builder.AddUser(userID, "owner@example.invalid"); err != nil {
			return err
		}
		return builder.AddPost(postID, userID)
	})
	approved, digest = approveManifestData(t, manifestData{PostTypeResolutions: []PostTypeResolution{{PostID: postID, Action: "set_type", TargetType: "share"}}})
	summary, err = ValidateCoverage(approved, digest, emptyRequirements)
	if err == nil || coverageCount(summary, "unused") != 1 {
		t.Fatalf("unused 未拒绝：%v %+v", err, summary)
	}

	excludeOnly := mustBuildRequirements(t, func(builder *ManifestDatasetBuilder) error {
		if err := builder.AddUser(userID, "owner@example.invalid"); err != nil {
			return err
		}
		if err := builder.AddPost(postID, userID); err != nil {
			return err
		}
		return builder.AddAnomaly("post", ManifestEntityPost, []string{postID}, mustOption(t, "excluded_content", "exclude"))
	})
	summary, err = ValidateCoverage(approved, digest, excludeOnly)
	if err == nil || coverageCount(summary, "wrong_category") != 1 {
		t.Fatalf("wrong category 未拒绝：%v %+v", err, summary)
	}
}

func approveManifestData(t *testing.T, data manifestData) (ApprovedManifest, ManifestDigest) {
	t.Helper()
	data.SchemaVersion = ManifestSchemaVersion
	if err := data.canonicalize(); err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	if err := data.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	digest := ManifestDigest(sha256.Sum256([]byte(fmt.Sprintf("approved-%d", data.Summary().TotalEntries))))
	approved, err := newApprovedManifest(data, digest)
	if err != nil {
		t.Fatalf("newApprovedManifest: %v", err)
	}
	return approved, digest
}

func mustOption(t *testing.T, category, action string) ManifestDecisionOption {
	t.Helper()
	option, err := NewManifestDecisionOption(category, action)
	if err != nil {
		t.Fatal(err)
	}
	return option
}

func mustBuildRequirements(t *testing.T, populate func(*ManifestDatasetBuilder) error) ManifestRequirements {
	t.Helper()
	completePopulate := func(builder *ManifestDatasetBuilder) error {
		if err := populate(builder); err != nil {
			return err
		}
		for _, catalog := range requiredDatasetCatalogs {
			if err := builder.MarkCatalogComplete(catalog); err != nil {
				return err
			}
		}
		return nil
	}
	probe := newManifestDatasetBuilder()
	if err := completePopulate(probe); err != nil {
		t.Fatalf("populate probe: %v", err)
	}
	if probe.failed != nil {
		t.Fatalf("populate probe: %v", probe.failed)
	}
	if err := validateDatasetSnapshot(probe.source); err != nil {
		t.Fatalf("validateDatasetSnapshot: %v", err)
	}
	probe.source.complete = true
	expected := sealManifestRequirements(probe.entries, probe.source)
	requirements, err := BuildManifestRequirements(manifestAdapterFunc(completePopulate), expected)
	if err != nil {
		t.Fatalf("BuildManifestRequirements: %v", err)
	}
	return requirements
}
