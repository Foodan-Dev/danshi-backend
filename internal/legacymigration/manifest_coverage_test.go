package legacymigration

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateCoverageAcceptsExactCanonicalSet(t *testing.T) {
	userID := syntheticUUID(1)
	requirement, err := NewManifestRequirement(
		"excluded_users",
		ManifestEntityUser,
		"{"+strings.ToUpper(userID)+"}",
	)
	if err != nil {
		t.Fatalf("NewManifestRequirement: %v", err)
	}
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion,
		ExcludedUsers: []ExcludedUserDecision{{UserID: userID, Action: "exclude"}},
	}
	manifest.validated = true
	summary, err := ValidateCoverage(manifest, completeRequirements(requirement))
	if err != nil {
		t.Fatalf("ValidateCoverage: %v", err)
	}
	if coverageCount(summary, "matched") != 1 ||
		coverageCount(summary, "missing") != 0 ||
		coverageCount(summary, "unused") != 0 ||
		coverageCount(summary, "wrong_category") != 0 {
		t.Fatalf("exact-set 摘要错误：%+v", summary)
	}
}

func TestValidateCoverageFailsClosedForMissingUnusedAndWrongCategory(t *testing.T) {
	postID := syntheticUUID(1)
	postRequirement := mustRequirement(t, "post_type_resolutions", ManifestEntityPost, postID)
	tests := []struct {
		name     string
		manifest Manifest
		required []ManifestRequirement
		counts   map[string]int64
	}{
		{
			name:     "missing",
			manifest: Manifest{SchemaVersion: ManifestSchemaVersion},
			required: []ManifestRequirement{postRequirement},
			counts:   map[string]int64{"missing": 1, "unused": 0, "wrong_category": 0},
		},
		{
			name: "unused",
			manifest: Manifest{
				SchemaVersion: ManifestSchemaVersion,
				PostTypeResolutions: []PostTypeResolution{{
					PostID: postID, Action: "set_type", TargetType: "share",
				}},
			},
			counts: map[string]int64{"missing": 0, "unused": 1, "wrong_category": 0},
		},
		{
			name: "wrong category",
			manifest: Manifest{
				SchemaVersion: ManifestSchemaVersion,
				ExcludedContent: []ExcludedContentDecision{{
					ContentType: "post", ContentID: postID, Action: "exclude",
				}},
			},
			required: []ManifestRequirement{postRequirement},
			counts:   map[string]int64{"missing": 0, "unused": 0, "wrong_category": 1},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.manifest.validated = true
			summary, err := ValidateCoverage(test.manifest, completeRequirements(test.required...))
			if err == nil || err.Error() != "manifest_coverage_mismatch" {
				t.Fatalf("exact-set 不一致应 fail closed，实际 %v", err)
			}
			for code, expected := range test.counts {
				if actual := coverageCount(summary, code); actual != expected {
					t.Fatalf("%s=%d，期望 %d；摘要 %+v", code, actual, expected, summary)
				}
			}
			encoded, marshalErr := json.Marshal(summary)
			if marshalErr != nil {
				t.Fatalf("json.Marshal: %v", marshalErr)
			}
			if strings.Contains(string(encoded), postID) {
				t.Fatalf("coverage summary 泄露来源 UUID：%s", encoded)
			}
		})
	}
}

func TestValidateCoverageReportsDuplicateRequirementsAndManifestDecisions(t *testing.T) {
	userID := syntheticUUID(1)
	requirement := mustRequirement(t, "excluded_users", ManifestEntityUser, userID)

	summary, err := ValidateCoverage(
		validatedForCoverage(Manifest{SchemaVersion: ManifestSchemaVersion}),
		completeRequirements(requirement, requirement),
	)
	if err == nil || err.Error() != "manifest_coverage_duplicate" ||
		coverageCount(summary, "duplicate_requirements") != 1 {
		t.Fatalf("重复 requirement 未被聚合拒绝：error=%v summary=%+v", err, summary)
	}

	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion,
		ExcludedUsers: []ExcludedUserDecision{
			{UserID: userID, Action: "exclude"},
			{UserID: "{" + strings.ToUpper(userID) + "}", Action: "exclude"},
		},
	}
	manifest.validated = true
	summary, err = ValidateCoverage(manifest, completeRequirements(requirement))
	if err == nil || err.Error() != "manifest_coverage_duplicate" ||
		coverageCount(summary, "duplicate_manifest_decisions") != 1 {
		t.Fatalf("canonical duplicate manifest 未被聚合拒绝：error=%v summary=%+v", err, summary)
	}
}

func TestValidateCoverageDoesNotReuseOneExactMatchForAnotherCategory(t *testing.T) {
	userID := syntheticUUID(1)
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion,
		ExcludedUsers: []ExcludedUserDecision{{UserID: userID, Action: "exclude"}},
	}
	manifest.validated = true
	requirements := completeRequirements(
		mustRequirement(t, "excluded_users", ManifestEntityUser, userID),
		mustRequirement(t, "email_rewrites", ManifestEntityUser, userID),
	)
	summary, err := ValidateCoverage(manifest, requirements)
	if err == nil || err.Error() != "manifest_coverage_mismatch" {
		t.Fatalf("未覆盖的第二 category 应失败，实际 %v", err)
	}
	if coverageCount(summary, "matched") != 1 ||
		coverageCount(summary, "missing") != 1 ||
		coverageCount(summary, "wrong_category") != 0 {
		t.Fatalf("一个 exact match 被错误复用于另一 category：%+v", summary)
	}
}

func TestNewManifestRequirementRejectsInvalidCategoryIdentityAndWhitespace(t *testing.T) {
	tests := []struct {
		name        string
		section     string
		entity      ManifestEntity
		identifiers []string
	}{
		{
			name: "wrong category", section: "email_rewrites", entity: ManifestEntityPost,
			identifiers: []string{syntheticUUID(1)},
		},
		{
			name: "invalid uuid", section: "excluded_users", entity: ManifestEntityUser,
			identifiers: []string{"not-a-uuid"},
		},
		{
			name: "opaque whitespace", section: "post_image_resolutions", entity: ManifestEntityPostImageReference,
			identifiers: []string{syntheticUUID(1), " source-value"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewManifestRequirement(test.section, test.entity, test.identifiers...); err == nil {
				t.Fatal("无效 requirement 应被拒绝")
			}
		})
	}
}

func TestValidateCoverageRejectsManifestThatDidNotPassFileGate(t *testing.T) {
	_, err := ValidateCoverage(
		Manifest{SchemaVersion: ManifestSchemaVersion},
		ManifestRequirements{},
	)
	if err == nil || err.Error() != "manifest_not_validated" {
		t.Fatalf("直接构造的 manifest 不得绕过显式 section/schema 门禁：%v", err)
	}

	manifest := validatedForCoverage(Manifest{SchemaVersion: 0})
	_, err = ValidateCoverage(manifest, ManifestRequirements{})
	if err == nil || err.Error() != "manifest_not_validated" {
		t.Fatalf("错误 schema_version 不得进入 coverage：%v", err)
	}
}

func mustRequirement(
	t *testing.T,
	section string,
	entity ManifestEntity,
	identifiers ...string,
) ManifestRequirement {
	t.Helper()
	requirement, err := NewManifestRequirement(section, entity, identifiers...)
	if err != nil {
		t.Fatalf("NewManifestRequirement: %v", err)
	}
	return requirement
}

func validatedForCoverage(manifest Manifest) Manifest {
	manifest.validated = true
	return manifest
}

func completeRequirements(entries ...ManifestRequirement) ManifestRequirements {
	return ManifestRequirements{
		Entries: entries,
		Source: ManifestSourceContext{
			CommentParentsComplete:   true,
			UserEmailsComplete:       true,
			PostImageTargetsComplete: true,
		},
	}
}
