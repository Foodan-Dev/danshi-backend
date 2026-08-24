package legacymigration

import "testing"

func TestValidateCoverageRequiresCompleteSourceContext(t *testing.T) {
	manifest := validatedForCoverage(Manifest{SchemaVersion: ManifestSchemaVersion})
	_, err := ValidateCoverage(manifest, ManifestRequirements{})
	if err == nil || err.Error() != "manifest_source_context_incomplete" {
		t.Fatalf("缺失完整来源上下文应 fail closed，实际 %v", err)
	}
}

func TestValidateCoverageDetectsCycleAgainstExistingCommentGraph(t *testing.T) {
	commentA := syntheticUUID(1)
	commentB := syntheticUUID(2)
	manifest := validatedForCoverage(Manifest{
		SchemaVersion: ManifestSchemaVersion,
		CommentReparentResolutions: []CommentReparentResolution{{
			CommentID: commentA, Action: "set_parent", TargetParentID: commentB,
		}},
	})
	requirement := mustRequirement(t, "comment_reparent_resolutions", ManifestEntityComment, commentA)
	existing, err := NewManifestCommentParentEdge(commentB, commentA)
	if err != nil {
		t.Fatalf("NewManifestCommentParentEdge: %v", err)
	}
	requirements := completeRequirements(requirement)
	requirements.Source.CommentParents = []ManifestCommentParentEdge{existing}
	_, err = ValidateCoverage(manifest, requirements)
	if err == nil || err.Error() != "manifest_decision_conflict" {
		t.Fatalf("决议与来源 parent 图合并后的环应被拒绝，实际 %v", err)
	}
}

func TestValidateCoverageChecksEmailOwnerIdentity(t *testing.T) {
	userA := syntheticUUID(1)
	userB := syntheticUUID(2)
	email := "occupied@example.invalid"
	manifest := validatedForCoverage(Manifest{
		SchemaVersion: ManifestSchemaVersion,
		EmailRewrites: []EmailRewriteDecision{{
			UserID: userA, Action: "rewrite", NewEmail: email,
		}},
	})
	requirement := mustRequirement(t, "email_rewrites", ManifestEntityUser, userA)
	sourceA, err := NewManifestSourceEmail(userA, "original@example.invalid")
	if err != nil {
		t.Fatalf("NewManifestSourceEmail source A: %v", err)
	}
	occupied, err := NewManifestSourceEmail(userB, email)
	if err != nil {
		t.Fatalf("NewManifestSourceEmail occupied: %v", err)
	}
	requirements := completeRequirements(requirement)
	requirements.Source.UserEmails = []ManifestSourceEmail{sourceA, occupied}
	_, err = ValidateCoverage(manifest, requirements)
	if err == nil || err.Error() != "manifest_decision_conflict" {
		t.Fatalf("目标 lower(email) 被其他保留用户占用时应被拒绝，实际 %v", err)
	}

	sameOwner, err := NewManifestSourceEmail(userA, email)
	if err != nil {
		t.Fatalf("NewManifestSourceEmail same owner: %v", err)
	}
	requirements.Source.UserEmails = []ManifestSourceEmail{sameOwner}
	if _, err := ValidateCoverage(manifest, requirements); err != nil {
		t.Fatalf("同一来源身份持有相同邮箱不应产生伪冲突：%v", err)
	}
}

func TestValidateCoverageChecksNaturalPostImageTargets(t *testing.T) {
	postID := syntheticUUID(1)
	assetID := syntheticUUID(2)
	manifest := validatedForCoverage(Manifest{
		SchemaVersion: ManifestSchemaVersion,
		PostImageResolutions: []PostImageResolution{{
			PostID: postID, SourceReference: "source-image", Action: "map", TargetImageAssetID: assetID,
		}},
	})
	requirement := mustRequirement(
		t, "post_image_resolutions", ManifestEntityPostImageReference, postID, "source-image",
	)
	existing, err := NewManifestPostImageTarget(postID, assetID)
	if err != nil {
		t.Fatalf("NewManifestPostImageTarget: %v", err)
	}
	requirements := completeRequirements(requirement)
	requirements.Source.PostImageTargets = []ManifestPostImageTarget{existing}
	_, err = ValidateCoverage(manifest, requirements)
	if err == nil || err.Error() != "manifest_decision_conflict" {
		t.Fatalf("自然映射已占用 (post,target_asset) 时应被拒绝，实际 %v", err)
	}
}

func TestValidateCoverageRejectsDuplicateSourceContextKeys(t *testing.T) {
	userID := syntheticUUID(1)
	entry, err := NewManifestSourceEmail(userID, "same@example.invalid")
	if err != nil {
		t.Fatalf("NewManifestSourceEmail: %v", err)
	}
	requirements := completeRequirements()
	requirements.Source.UserEmails = []ManifestSourceEmail{entry, entry}
	manifest := validatedForCoverage(Manifest{SchemaVersion: ManifestSchemaVersion})
	_, err = ValidateCoverage(manifest, requirements)
	if err == nil || err.Error() != "manifest_source_context_duplicate" {
		t.Fatalf("重复来源唯一键应被拒绝，实际 %v", err)
	}
}

func TestValidateCoverageAppliesAllEmailRewritesBeforeFinalUniqueness(t *testing.T) {
	userA := syntheticUUID(1)
	userB := syntheticUUID(2)
	manifest := validatedForCoverage(Manifest{
		SchemaVersion: ManifestSchemaVersion,
		EmailRewrites: []EmailRewriteDecision{
			{UserID: userA, Action: "rewrite", NewEmail: "new-a@example.invalid"},
			{UserID: userB, Action: "rewrite", NewEmail: "new-b@example.invalid"},
		},
	})
	requirements := completeRequirements(
		mustRequirement(t, "email_rewrites", ManifestEntityUser, userA),
		mustRequirement(t, "email_rewrites", ManifestEntityUser, userB),
	)
	sourceA, err := NewManifestSourceEmail(userA, "Duplicate@Example.invalid")
	if err != nil {
		t.Fatalf("NewManifestSourceEmail A: %v", err)
	}
	sourceB, err := NewManifestSourceEmail(userB, "duplicate@example.invalid")
	if err != nil {
		t.Fatalf("NewManifestSourceEmail B: %v", err)
	}
	requirements.Source.UserEmails = []ManifestSourceEmail{sourceA, sourceB}
	if _, err := ValidateCoverage(manifest, requirements); err != nil {
		t.Fatalf("全部 rewrite 后已消除旧 lower(email) 冲突，不应 false-block：%v", err)
	}

	manifest.EmailRewrites[0].NewEmail = "old-b@example.invalid"
	manifest.EmailRewrites[1].NewEmail = "new-b@example.invalid"
	sourceA, err = NewManifestSourceEmail(userA, "old-a@example.invalid")
	if err != nil {
		t.Fatalf("NewManifestSourceEmail old A: %v", err)
	}
	sourceB, err = NewManifestSourceEmail(userB, "old-b@example.invalid")
	if err != nil {
		t.Fatalf("NewManifestSourceEmail old B: %v", err)
	}
	requirements.Source.UserEmails = []ManifestSourceEmail{sourceA, sourceB}
	if _, err := ValidateCoverage(manifest, requirements); err != nil {
		t.Fatalf("原子叠加 rewrites 后合法接管旧邮箱不应 false-block：%v", err)
	}
}
