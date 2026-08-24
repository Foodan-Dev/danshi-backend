package legacymigration

import "testing"

func TestBuildManifestRequirementsRequiresIndependentCompleteAdapter(t *testing.T) {
	expected := ManifestDigest{1}
	if _, err := BuildManifestRequirements(nil, expected); err == nil || err.Error() != "manifest_source_context_incomplete" {
		t.Fatalf("nil adapter 未拒绝：%v", err)
	}
	if _, err := BuildManifestRequirements(manifestAdapterFunc(func(*ManifestDatasetBuilder) error { return nil }), expected); err == nil || err.Error() != "manifest_source_context_incomplete" {
		t.Fatalf("未显式完成所有 catalog 的 no-op adapter 未拒绝：%v", err)
	}
	if _, err := BuildManifestRequirements(manifestAdapterFunc(func(*ManifestDatasetBuilder) error { return nil }), ManifestDigest{}); err == nil || err.Error() != "manifest_dataset_digest_required" {
		t.Fatalf("遗漏 dataset digest 未拒绝：%v", err)
	}
	completeEmpty := manifestAdapterFunc(func(builder *ManifestDatasetBuilder) error { return markAllDatasetCatalogs(builder) })
	if _, err := BuildManifestRequirements(completeEmpty, expected); err == nil || err.Error() != "manifest_dataset_digest_mismatch" {
		t.Fatalf("错误 dataset digest 未拒绝：%v", err)
	}
	approved, digest := approveManifestData(t, manifestData{})
	if _, err := ValidateCoverage(approved, digest, ManifestRequirements{}); err == nil || err.Error() != "manifest_source_context_incomplete" {
		t.Fatalf("未 seal requirements 未拒绝：%v", err)
	}
}

func TestCommentTargetsMustExistStayInPostAndMatchAuthor(t *testing.T) {
	userA, userB, commenter := syntheticUUID(1), syntheticUUID(2), syntheticUUID(3)
	postA, postB := syntheticUUID(4), syntheticUUID(5)
	parentA, parentB, child := syntheticUUID(6), syntheticUUID(7), syntheticUUID(8)
	option := mustOption(t, "comment_reparent_resolutions", "set_parent_and_reply_to")
	base := func(builder *ManifestDatasetBuilder) error {
		for _, entry := range []struct{ id, email string }{{userA, "a@example.invalid"}, {userB, "b@example.invalid"}, {commenter, "c@example.invalid"}} {
			if err := builder.AddUser(entry.id, entry.email); err != nil {
				return err
			}
		}
		if err := builder.AddPost(postA, userA); err != nil {
			return err
		}
		if err := builder.AddPost(postB, userB); err != nil {
			return err
		}
		if err := builder.AddComment(parentA, postA, userA, "", userA); err != nil {
			return err
		}
		if err := builder.AddComment(parentB, postB, userB, "", userB); err != nil {
			return err
		}
		if err := builder.AddComment(child, postA, commenter, parentA, userA); err != nil {
			return err
		}
		return builder.AddAnomaly("comment-parent", ManifestEntityComment, []string{child}, option)
	}
	requirements := mustBuildRequirements(t, base)
	tests := []struct{ name, parent, reply, code string }{
		{"dangling", syntheticUUID(99), userA, "manifest_decision_target_missing"},
		{"cross post", parentB, userB, "manifest_comment_cross_post"},
		{"wrong reply author", parentA, userB, "manifest_comment_reply_invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := manifestData{CommentReparentResolutions: []CommentReparentResolution{{CommentID: child, Action: "set_parent_and_reply_to", TargetParentID: test.parent, TargetReplyToUserID: test.reply}}}
			approved, digest := approveManifestData(t, manifest)
			_, err := ValidateCoverage(approved, digest, requirements)
			if err == nil || err.Error() != test.code {
				t.Fatalf("期望 %s，实际 %v", test.code, err)
			}
		})
	}
}

func TestCommentSourceDanglingAndExcludedReferencesFail(t *testing.T) {
	user, missingUser, post, comment := syntheticUUID(1), syntheticUUID(2), syntheticUUID(3), syntheticUUID(4)
	requirements := mustBuildRequirements(t, func(builder *ManifestDatasetBuilder) error {
		if err := builder.AddUser(user, "user@example.invalid"); err != nil {
			return err
		}
		if err := builder.AddPost(post, user); err != nil {
			return err
		}
		return builder.AddComment(comment, post, user, "", missingUser)
	})
	approved, digest := approveManifestData(t, manifestData{})
	if _, err := ValidateCoverage(approved, digest, requirements); err == nil || err.Error() != "manifest_decision_target_missing" {
		t.Fatalf("dangling reply user 未拒绝：%v", err)
	}
}

func TestImageTargetMustExistHaveCorrectPurposeAndOwner(t *testing.T) {
	owner, other, post, ref := syntheticUUID(1), syntheticUUID(2), syntheticUUID(3), "legacy-image-ref"
	mapOption := mustOption(t, "post_image_resolutions", "map")
	tests := []struct {
		name             string
		asset            string
		purpose          ImageAssetPurpose
		assetOwner, code string
		register         bool
	}{
		{"dangling", syntheticUUID(4), ImageAssetPurposePost, owner, "manifest_decision_target_missing", false},
		{"wrong purpose", syntheticUUID(5), ImageAssetPurposeAvatar, owner, "manifest_asset_purpose_invalid", true},
		{"wrong owner", syntheticUUID(6), ImageAssetPurposePost, other, "manifest_asset_owner_invalid", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requirements := mustBuildRequirements(t, func(builder *ManifestDatasetBuilder) error {
				if err := builder.AddUser(owner, "owner@example.invalid"); err != nil {
					return err
				}
				if err := builder.AddUser(other, "other@example.invalid"); err != nil {
					return err
				}
				if err := builder.AddPost(post, owner); err != nil {
					return err
				}
				if err := builder.AddPostImageReference(post, ref); err != nil {
					return err
				}
				if test.register {
					if err := builder.AddImageAsset(test.asset, test.assetOwner, test.purpose); err != nil {
						return err
					}
				}
				return builder.AddAnomaly("post-image", ManifestEntityPostImageReference, []string{post, ref}, mapOption)
			})
			manifest := manifestData{PostImageResolutions: []PostImageResolution{{PostID: post, SourceReference: ref, Action: "map", TargetImageAssetID: test.asset}}}
			approved, digest := approveManifestData(t, manifest)
			_, err := ValidateCoverage(approved, digest, requirements)
			if err == nil || err.Error() != test.code {
				t.Fatalf("期望 %s，实际 %v", test.code, err)
			}
		})
	}
}

func TestDatasetRejectsPostImageReferenceWhosePostDoesNotExist(t *testing.T) {
	populate := func(builder *ManifestDatasetBuilder) error {
		return builder.AddPostImageReference(syntheticUUID(99), "orphan-ref")
	}
	probe := newManifestDatasetBuilder()
	if err := populate(probe); err != nil {
		t.Fatal(err)
	}
	if err := markAllDatasetCatalogs(probe); err != nil {
		t.Fatal(err)
	}
	probe.source.complete = true
	expected := sealManifestRequirements(probe.entries, probe.source)
	completedPopulate := manifestAdapterFunc(func(builder *ManifestDatasetBuilder) error {
		if err := populate(builder); err != nil {
			return err
		}
		return markAllDatasetCatalogs(builder)
	})
	if _, err := BuildManifestRequirements(completedPopulate, expected); err == nil || err.Error() != "manifest_source_context_invalid" {
		t.Fatalf("source image reference 不得只靠 composite hash 自证：%v", err)
	}
}

func markAllDatasetCatalogs(builder *ManifestDatasetBuilder) error {
	for _, catalog := range requiredDatasetCatalogs {
		if err := builder.MarkCatalogComplete(catalog); err != nil {
			return err
		}
	}
	return nil
}

func TestExcludedAssetCannotRemainNaturallyReferenced(t *testing.T) {
	user, post, usedAsset, keepAsset := syntheticUUID(1), syntheticUUID(2), syntheticUUID(3), syntheticUUID(4)
	exclude := mustOption(t, "duplicate_image_asset_resolutions", "exclude")
	keep := mustOption(t, "duplicate_image_asset_resolutions", "keep")
	requirements := mustBuildRequirements(t, func(builder *ManifestDatasetBuilder) error {
		if err := builder.AddUser(user, "owner@example.invalid"); err != nil {
			return err
		}
		if err := builder.AddPost(post, user); err != nil {
			return err
		}
		if err := builder.AddImageAsset(usedAsset, user, ImageAssetPurposePost); err != nil {
			return err
		}
		if err := builder.AddImageAsset(keepAsset, user, ImageAssetPurposePost); err != nil {
			return err
		}
		if err := builder.AddNaturalPostImageTarget(post, usedAsset); err != nil {
			return err
		}
		if err := builder.AddAnomaly("duplicate-exclude", ManifestEntityDuplicateImageAsset, []string{"group", usedAsset}, exclude); err != nil {
			return err
		}
		return builder.AddAnomaly("duplicate-keep", ManifestEntityDuplicateImageAsset, []string{"group", keepAsset}, keep)
	})
	manifest := manifestData{DuplicateImageAssetResolutions: []DuplicateImageAssetResolution{
		{GroupKey: "group", ImageAssetID: usedAsset, Action: "exclude"},
		{GroupKey: "group", ImageAssetID: keepAsset, Action: "keep"},
	}}
	approved, digest := approveManifestData(t, manifest)
	if _, err := ValidateCoverage(approved, digest, requirements); err == nil || err.Error() != "manifest_decision_conflict" {
		t.Fatalf("自然帖子引用仍指向 excluded asset：%v", err)
	}
}

func TestExcludedAssetCannotRemainNaturalAvatar(t *testing.T) {
	user, avatarAsset, keepAsset := syntheticUUID(1), syntheticUUID(2), syntheticUUID(3)
	requirements := mustBuildRequirements(t, func(builder *ManifestDatasetBuilder) error {
		if err := builder.AddUser(user, "owner@example.invalid"); err != nil {
			return err
		}
		if err := builder.AddImageAsset(avatarAsset, user, ImageAssetPurposeAvatar); err != nil {
			return err
		}
		if err := builder.AddImageAsset(keepAsset, user, ImageAssetPurposeAvatar); err != nil {
			return err
		}
		if err := builder.AddNaturalAvatarTarget(user, avatarAsset); err != nil {
			return err
		}
		if err := builder.AddAnomaly("avatar-exclude", ManifestEntityDuplicateImageAsset, []string{"group", avatarAsset}, mustOption(t, "duplicate_image_asset_resolutions", "exclude")); err != nil {
			return err
		}
		return builder.AddAnomaly("avatar-keep", ManifestEntityDuplicateImageAsset, []string{"group", keepAsset}, mustOption(t, "duplicate_image_asset_resolutions", "keep"))
	})
	approved, digest := approveManifestData(t, manifestData{DuplicateImageAssetResolutions: []DuplicateImageAssetResolution{
		{GroupKey: "group", ImageAssetID: avatarAsset, Action: "exclude"},
		{GroupKey: "group", ImageAssetID: keepAsset, Action: "keep"},
	}})
	if _, err := ValidateCoverage(approved, digest, requirements); err == nil || err.Error() != "manifest_decision_conflict" {
		t.Fatalf("自然头像仍指向 excluded asset：%v", err)
	}
}

func TestAvatarTargetMustHaveAvatarPurposeAndSameOwner(t *testing.T) {
	user, other, asset := syntheticUUID(1), syntheticUUID(2), syntheticUUID(3)
	requirements := mustBuildRequirements(t, func(builder *ManifestDatasetBuilder) error {
		if err := builder.AddUser(user, "user@example.invalid"); err != nil {
			return err
		}
		if err := builder.AddUser(other, "other@example.invalid"); err != nil {
			return err
		}
		if err := builder.AddImageAsset(asset, other, ImageAssetPurposeAvatar); err != nil {
			return err
		}
		return builder.AddAnomaly("avatar", ManifestEntityUser, []string{user}, mustOption(t, "avatar_resolutions", "replace"))
	})
	approved, digest := approveManifestData(t, manifestData{AvatarResolutions: []AvatarResolution{{UserID: user, Action: "replace", TargetImageAssetID: asset}}})
	if _, err := ValidateCoverage(approved, digest, requirements); err == nil || err.Error() != "manifest_asset_owner_invalid" {
		t.Fatalf("错误 avatar owner 未拒绝：%v", err)
	}
}

func TestDictionaryTargetMustExistAndPreferenceFlavorAliasesFlavor(t *testing.T) {
	mapOption := mustOption(t, "dictionary_mappings", "map")
	build := func(seed string) ManifestRequirements {
		return mustBuildRequirements(t, func(builder *ManifestDatasetBuilder) error {
			if err := builder.AddDictionarySource("preference_flavor", "old"); err != nil {
				return err
			}
			if err := builder.AddSeed("flavor", seed); err != nil {
				return err
			}
			return builder.AddAnomaly("legacy-flavor", ManifestEntityDictionaryMapping, []string{"preference_flavor", "old"}, mapOption)
		})
	}
	manifest := manifestData{DictionaryMappings: []DictionaryMapping{{Dictionary: "preference_flavor", Source: "old", Action: "map", Target: "typo"}}}
	approved, digest := approveManifestData(t, manifest)
	if _, err := ValidateCoverage(approved, digest, build("approved")); err == nil || err.Error() != "manifest_dictionary_target_missing" {
		t.Fatalf("seed typo 未拒绝：%v", err)
	}

	manifest = manifestData{DictionaryMappings: []DictionaryMapping{{Dictionary: "preference_flavor", Source: "old", Action: "map", Target: "approved"}}}
	approved, digest = approveManifestData(t, manifest)
	if _, err := ValidateCoverage(approved, digest, build("approved")); err != nil {
		t.Fatalf("preference_flavor 未归入 flavor：%v", err)
	}
}

func TestExcludedAuthorCannotLeaveRetainedPostOrComment(t *testing.T) {
	user, post, comment := syntheticUUID(1), syntheticUUID(2), syntheticUUID(3)
	requirements := mustBuildRequirements(t, func(builder *ManifestDatasetBuilder) error {
		if err := builder.AddUser(user, "user@example.invalid"); err != nil {
			return err
		}
		if err := builder.AddPost(post, user); err != nil {
			return err
		}
		if err := builder.AddComment(comment, post, user, "", user); err != nil {
			return err
		}
		return builder.AddAnomaly("exclude-user", ManifestEntityUser, []string{user}, mustOption(t, "excluded_users", "exclude"))
	})
	approved, digest := approveManifestData(t, manifestData{ExcludedUsers: []ExcludedUserDecision{{UserID: user, Action: "exclude"}}})
	if _, err := ValidateCoverage(approved, digest, requirements); err == nil || err.Error() != "manifest_decision_conflict" {
		t.Fatalf("排除 author 后悬空引用未拒绝：%v", err)
	}
}

func TestExcludedUserMayLeaveUnreferencedAssetWithNullableUploader(t *testing.T) {
	user, asset := syntheticUUID(1), syntheticUUID(2)
	requirements := mustBuildRequirements(t, func(builder *ManifestDatasetBuilder) error {
		if err := builder.AddUser(user, "user@example.invalid"); err != nil {
			return err
		}
		if err := builder.AddImageAsset(asset, user, ImageAssetPurposeAvatar); err != nil {
			return err
		}
		return builder.AddAnomaly("exclude-user", ManifestEntityUser, []string{user}, mustOption(t, "excluded_users", "exclude"))
	})
	approved, digest := approveManifestData(t, manifestData{ExcludedUsers: []ExcludedUserDecision{{UserID: user, Action: "exclude"}}})
	if _, err := ValidateCoverage(approved, digest, requirements); err != nil {
		t.Fatalf("无引用资产的 nullable uploader 不应阻止排除用户：%v", err)
	}
}
