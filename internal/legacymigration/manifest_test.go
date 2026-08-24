package legacymigration

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadManifestAcceptsExplicitEmptySections(t *testing.T) {
	data := emptyManifestJSON()
	path := writeManifest(t, data, 0o400)
	manifest, digest, err := loadManifestForTest(t, path)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	summary := manifest.Summary()
	if summary.SchemaVersion != ManifestSchemaVersion || summary.TotalEntries != 0 {
		t.Fatalf("空 manifest 摘要错误：%+v", summary)
	}
	if len(summary.Sections) != len(requiredManifestSections) {
		t.Fatalf("固定 section code 数量错误：%+v", summary.Sections)
	}
	if digest != ManifestDigest(sha256.Sum256(data)) || len(digest.String()) != sha256.Size*2 {
		t.Fatalf("manifest digest 与获批原始字节不一致：%s", digest.String())
	}
	for index, section := range summary.Sections {
		if section.Code != requiredManifestSections[index] || section.Count != 0 {
			t.Fatalf("固定 section code 或计数错误：%+v", summary.Sections)
		}
	}
}

func TestLoadManifestRequiresAndBindsIndependentDigest(t *testing.T) {
	data := emptyManifestJSON()
	path := writeManifest(t, data, 0o600)
	if _, err := LoadManifest(path, ManifestDigest{}); err == nil || err.Error() != "manifest_digest_required" {
		t.Fatalf("遗漏独立 digest 应 fail closed，实际 %v", err)
	}
	wrong := ManifestDigest(sha256.Sum256([]byte("wrong-approved-bytes")))
	if _, err := LoadManifest(path, wrong); err == nil || err.Error() != "manifest_digest_mismatch" {
		t.Fatalf("错误独立 digest 应 fail closed，实际 %v", err)
	}
	expected := ManifestDigest(sha256.Sum256(data))
	approved, err := LoadManifest(path, expected)
	if err != nil {
		t.Fatalf("正确独立 digest 加载失败：%v", err)
	}
	if _, err := approved.Summary(ManifestDigest{}); err == nil || err.Error() != "manifest_digest_required" {
		t.Fatalf("Summary 不能省略 digest：%v", err)
	}
	if _, err := approved.Summary(wrong); err == nil || err.Error() != "manifest_digest_mismatch" {
		t.Fatalf("Summary 未重新绑定 digest：%v", err)
	}
	if summary, err := approved.Summary(expected); err != nil || summary.TotalEntries != 0 {
		t.Fatalf("Summary 正确 digest 失败：%v %+v", err, summary)
	}
}

func TestApprovedManifestLoadedFromFileDetectsMutationAndRedactsValues(t *testing.T) {
	document := emptyManifestDocument()
	privateID := syntheticUUID(91)
	document["excluded_users"] = []any{map[string]any{"user_id": privateID, "action": "exclude"}}
	data := mustJSON(t, document)
	expected := ManifestDigest(sha256.Sum256(data))
	approved, err := LoadManifest(writeManifest(t, data, 0o600), expected)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	encoded, marshalErr := json.Marshal(approved)
	if marshalErr != nil {
		t.Fatalf("json.Marshal: %v", marshalErr)
	}
	formatted := fmt.Sprintf("%+v", approved)
	if strings.Contains(string(encoded), privateID) || strings.Contains(formatted, privateID) {
		t.Fatalf("ApprovedManifest 泄露私有决议：%s / %s", encoded, formatted)
	}
	approved.data.ExcludedUsers[0].Action = "rewrite"
	if _, err := approved.Summary(expected); err == nil || err.Error() != "approved_manifest_tampered" {
		t.Fatalf("file-load 后 mutation 未被 canonical seal 捕获：%v", err)
	}
}

func TestLoadManifestAcceptsValidatedNonEmptyDecisions(t *testing.T) {
	document := emptyManifestDocument()
	document["excluded_users"] = []any{
		map[string]any{"user_id": syntheticUUID(1), "action": "exclude"},
	}
	document["excluded_content"] = []any{
		map[string]any{"content_type": "post", "content_id": syntheticUUID(2), "action": "exclude"},
		map[string]any{"content_type": "comment", "content_id": syntheticUUID(3), "action": "exclude"},
	}
	document["email_rewrites"] = []any{
		map[string]any{"user_id": syntheticUUID(4), "action": "rewrite", "new_email": "rewritten@example.invalid"},
	}
	document["post_type_resolutions"] = []any{
		map[string]any{"post_id": syntheticUUID(5), "action": "set_type", "target_type": "share"},
	}
	document["dictionary_mappings"] = []any{
		map[string]any{"dictionary": "cuisine", "source": "source-cuisine", "action": "map", "target": "target-cuisine"},
	}
	document["post_image_resolutions"] = []any{
		map[string]any{
			"post_id": syntheticUUID(6), "source_reference": "source-image", "action": "map",
			"target_image_asset_id": syntheticUUID(7),
		},
	}
	document["avatar_resolutions"] = []any{
		map[string]any{"user_id": syntheticUUID(8), "action": "replace", "target_image_asset_id": syntheticUUID(7)},
	}
	document["duplicate_image_asset_resolutions"] = []any{
		map[string]any{"group_key": "duplicate-group", "image_asset_id": syntheticUUID(7), "action": "keep"},
		map[string]any{"group_key": "duplicate-group", "image_asset_id": syntheticUUID(9), "action": "exclude"},
	}
	document["comment_reparent_resolutions"] = []any{
		map[string]any{
			"comment_id": syntheticUUID(10), "action": "set_reply_to",
			"target_reply_to_user_id": syntheticUUID(11),
		},
	}
	document["orphan_like_exclusions"] = []any{
		map[string]any{"like_id": syntheticUUID(12), "action": "exclude"},
	}
	document["orphan_notification_exclusions"] = []any{
		map[string]any{"notification_id": syntheticUUID(13), "action": "exclude"},
	}

	manifest, _, err := loadManifestForTest(t, writeManifest(t, mustJSON(t, document), 0o600))
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if manifest.SchemaVersion != ManifestSchemaVersion {
		t.Fatalf("schema_version 错误：%d", manifest.SchemaVersion)
	}
	if summary := manifest.Summary(); summary.TotalEntries != 13 {
		t.Fatalf("非空 manifest 聚合总数错误：%+v", summary)
	}
}

func TestLoadManifestRequiresEveryExplicitSection(t *testing.T) {
	document := emptyManifestDocument()
	delete(document, "avatar_resolutions")
	assertManifestCode(t, writeManifest(t, mustJSON(t, document), 0o600), "manifest_section_missing")

	document = emptyManifestDocument()
	document["avatar_resolutions"] = nil
	assertManifestCode(t, writeManifest(t, mustJSON(t, document), 0o600), "manifest_section_missing")
}

func TestLoadManifestRejectsUnknownFields(t *testing.T) {
	document := emptyManifestDocument()
	document["unexpected_section"] = []any{}
	assertManifestCode(t, writeManifest(t, mustJSON(t, document), 0o600), "manifest_schema_invalid")

	document = emptyManifestDocument()
	document["excluded_users"] = []any{
		map[string]any{"user_id": syntheticUUID(1), "action": "exclude", "unexpected": true},
	}
	assertManifestCode(t, writeManifest(t, mustJSON(t, document), 0o600), "manifest_schema_invalid")
}

func TestLoadManifestRejectsDuplicateJSONKeysAtAnyDepth(t *testing.T) {
	topLevel := strings.Replace(
		string(emptyManifestJSON()),
		`"excluded_users":[]`,
		`"excluded_users":[],"excluded_users":[]`,
		1,
	)
	assertManifestCode(t, writeManifest(t, []byte(topLevel), 0o600), "manifest_duplicate_key")

	nested := strings.Replace(
		string(emptyManifestJSON()),
		`"excluded_users":[]`,
		`"excluded_users":[{"user_id":"10000000-0000-4000-8000-000000000001","user_id":"10000000-0000-4000-8000-000000000002","action":"exclude"}]`,
		1,
	)
	assertManifestCode(t, writeManifest(t, []byte(nested), 0o600), "manifest_duplicate_key")

	caseFoldedTopLevel := strings.Replace(
		string(emptyManifestJSON()),
		`"excluded_users":[]`,
		`"excluded_users":[],"EXCLUDED_USERS":[]`,
		1,
	)
	assertManifestCode(t, writeManifest(t, []byte(caseFoldedTopLevel), 0o600), "manifest_duplicate_key")

	caseFoldedNested := strings.Replace(
		string(emptyManifestJSON()),
		`"excluded_users":[]`,
		`"excluded_users":[{"user_id":"10000000-0000-4000-8000-000000000001","USER_ID":"10000000-0000-4000-8000-000000000002","action":"exclude"}]`,
		1,
	)
	assertManifestCode(t, writeManifest(t, []byte(caseFoldedNested), 0o600), "manifest_duplicate_key")

	unicodeFolded := strings.Replace(
		string(emptyManifestJSON()),
		`"excluded_users":[]`,
		`"excluded_users":[],"K":[],"K":[]`,
		1,
	)
	assertManifestCode(t, writeManifest(t, []byte(unicodeFolded), 0o600), "manifest_duplicate_key")
}

func TestLoadManifestRequiresExactFieldCase(t *testing.T) {
	topLevel := strings.Replace(string(emptyManifestJSON()), `"excluded_users":[]`, `"EXCLUDED_USERS":[]`, 1)
	assertManifestCode(t, writeManifest(t, []byte(topLevel), 0o600), "manifest_schema_invalid")

	nested := strings.Replace(
		string(emptyManifestJSON()),
		`"excluded_users":[]`,
		`"excluded_users":[{"USER_ID":"10000000-0000-4000-8000-000000000001","action":"exclude"}]`,
		1,
	)
	assertManifestCode(t, writeManifest(t, []byte(nested), 0o600), "manifest_schema_invalid")
}

func TestLoadManifestRejectsInsecureOrNonRegularFiles(t *testing.T) {
	assertManifestCode(t, writeManifest(t, emptyManifestJSON(), 0o640), "manifest_permissions_too_open")

	directory := filepath.Join(t.TempDir(), "manifest-directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	assertManifestCode(t, directory, "manifest_not_regular")

	target := writeManifest(t, emptyManifestJSON(), 0o600)
	symlink := filepath.Join(t.TempDir(), "manifest-link.json")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	assertManifestCode(t, symlink, "manifest_not_regular")

	writableParent := t.TempDir()
	if err := os.Chmod(writableParent, 0o770); err != nil {
		t.Fatalf("Chmod parent: %v", err)
	}
	manifestPath := filepath.Join(writableParent, "manifest.json")
	if err := os.WriteFile(manifestPath, emptyManifestJSON(), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	assertManifestCode(t, manifestPath, "manifest_parent_permissions_too_open")
}

func TestLoadManifestRejectsOversizedFile(t *testing.T) {
	data := make([]byte, int(MaxManifestBytes)+1)
	assertManifestCode(t, writeManifest(t, data, 0o600), "manifest_too_large")
}

func TestLoadManifestRejectsInvalidUTF8AndIsolatedSurrogate(t *testing.T) {
	invalidUTF8 := append([]byte(nil), emptyManifestJSON()...)
	invalidUTF8[len(invalidUTF8)-1] = 0xff
	assertManifestCode(t, writeManifest(t, invalidUTF8, 0o600), "manifest_invalid_json")

	isolatedSurrogate := strings.Replace(
		string(emptyManifestJSON()),
		`"dictionary_mappings":[]`,
		`"dictionary_mappings":[{"dictionary":"cuisine","source":"\ud800","action":"map","target":"target-value"}]`,
		1,
	)
	assertManifestCode(t, writeManifest(t, []byte(isolatedSurrogate), 0o600), "manifest_invalid_json")

	validUnicode := strings.Replace(
		string(emptyManifestJSON()),
		`"dictionary_mappings":[]`,
		`"dictionary_mappings":[{"dictionary":"cuisine","source":"\ufffd","action":"map","target":"\ud83d\ude00"}]`,
		1,
	)
	manifest, _, err := loadManifestForTest(t, writeManifest(t, []byte(validUnicode), 0o600))
	if err != nil {
		t.Fatalf("合法 replacement rune 与配对 surrogate 不应被拒绝：%v", err)
	}
	if manifest.DictionaryMappings[0].Source != "�" || manifest.DictionaryMappings[0].Target != "😀" {
		t.Fatalf("合法 Unicode 解码错误：%+v", manifest.DictionaryMappings[0])
	}
}

func TestLoadManifestRejectsEmptyIdentifiersUnknownActionsDuplicatesAndConflicts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
		code   string
	}{
		{
			name: "empty identifier",
			mutate: func(document map[string]any) {
				document["excluded_users"] = []any{map[string]any{"user_id": " ", "action": "exclude"}}
			},
			code: "manifest_identifier_empty",
		},
		{
			name: "unknown action",
			mutate: func(document map[string]any) {
				document["orphan_like_exclusions"] = []any{map[string]any{"like_id": syntheticUUID(1), "action": "ignore"}}
			},
			code: "manifest_action_unknown",
		},
		{
			name: "duplicate decision",
			mutate: func(document map[string]any) {
				decision := map[string]any{"notification_id": syntheticUUID(1), "action": "exclude"}
				document["orphan_notification_exclusions"] = []any{decision, decision}
			},
			code: "manifest_entry_duplicate",
		},
		{
			name: "cross section conflict",
			mutate: func(document map[string]any) {
				document["excluded_users"] = []any{map[string]any{"user_id": syntheticUUID(1), "action": "exclude"}}
				document["email_rewrites"] = []any{
					map[string]any{"user_id": syntheticUUID(1), "action": "rewrite", "new_email": "rewritten@example.invalid"},
				}
			},
			code: "manifest_decision_conflict",
		},
		{
			name: "duplicate asset group without unique keep",
			mutate: func(document map[string]any) {
				document["duplicate_image_asset_resolutions"] = []any{
					map[string]any{"group_key": "group-a", "image_asset_id": syntheticUUID(1), "action": "exclude"},
					map[string]any{"group_key": "group-a", "image_asset_id": syntheticUUID(2), "action": "exclude"},
				}
			},
			code: "manifest_decision_conflict",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := emptyManifestDocument()
			test.mutate(document)
			assertManifestCode(t, writeManifest(t, mustJSON(t, document), 0o600), test.code)
		})
	}
}

func TestLoadManifestCanonicalizesUUIDsBeforeIdentityChecks(t *testing.T) {
	canonical := syntheticUUID(1)
	variant := "{" + strings.ToUpper(canonical) + "}"
	compact := strings.ReplaceAll(canonical, "-", "")
	document := emptyManifestDocument()
	document["excluded_users"] = []any{map[string]any{"user_id": variant, "action": "exclude"}}
	manifest, _, err := loadManifestForTest(t, writeManifest(t, mustJSON(t, document), 0o600))
	if err != nil {
		t.Fatalf("LoadManifest variant UUID: %v", err)
	}
	if manifest.ExcludedUsers[0].UserID != canonical {
		t.Fatalf("UUID 未 canonicalize：%q", manifest.ExcludedUsers[0].UserID)
	}

	document["excluded_users"] = []any{
		map[string]any{"user_id": canonical, "action": "exclude"},
		map[string]any{"user_id": compact, "action": "exclude"},
	}
	assertManifestCode(t, writeManifest(t, mustJSON(t, document), 0o600), "manifest_entry_duplicate")

	document = emptyManifestDocument()
	document["excluded_users"] = []any{map[string]any{"user_id": "not-a-uuid", "action": "exclude"}}
	assertManifestCode(t, writeManifest(t, mustJSON(t, document), 0o600), "manifest_uuid_invalid")
}

func TestLoadManifestRejectsCanonicalEmailAndTargetImageDuplicates(t *testing.T) {
	document := emptyManifestDocument()
	document["email_rewrites"] = []any{
		map[string]any{"user_id": syntheticUUID(1), "action": "rewrite", "new_email": "Same@Example.invalid"},
		map[string]any{"user_id": syntheticUUID(2), "action": "rewrite", "new_email": "same@example.invalid"},
	}
	assertManifestCode(t, writeManifest(t, mustJSON(t, document), 0o600), "manifest_email_target_duplicate")

	document = emptyManifestDocument()
	document["post_image_resolutions"] = []any{
		map[string]any{
			"post_id": syntheticUUID(1), "source_reference": "source-a", "action": "map",
			"target_image_asset_id": syntheticUUID(2),
		},
		map[string]any{
			"post_id": syntheticUUID(1), "source_reference": "source-b", "action": "map",
			"target_image_asset_id": syntheticUUID(2),
		},
	}
	assertManifestCode(t, writeManifest(t, mustJSON(t, document), 0o600), "manifest_entry_duplicate")
}

func TestLoadManifestRejectsReparentReferencesToExcludedEntities(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "excluded parent comment",
			mutate: func(document map[string]any) {
				document["excluded_content"] = []any{
					map[string]any{"content_type": "comment", "content_id": syntheticUUID(2), "action": "exclude"},
				}
				document["comment_reparent_resolutions"] = []any{
					map[string]any{
						"comment_id": syntheticUUID(1), "action": "set_parent", "target_parent_id": syntheticUUID(2),
					},
				}
			},
		},
		{
			name: "excluded reply user",
			mutate: func(document map[string]any) {
				document["excluded_users"] = []any{
					map[string]any{"user_id": syntheticUUID(2), "action": "exclude"},
				}
				document["comment_reparent_resolutions"] = []any{
					map[string]any{
						"comment_id": syntheticUUID(1), "action": "set_reply_to",
						"target_reply_to_user_id": syntheticUUID(2),
					},
				}
			},
		},
		{
			name: "self parent",
			mutate: func(document map[string]any) {
				document["comment_reparent_resolutions"] = []any{
					map[string]any{
						"comment_id": syntheticUUID(1), "action": "set_parent", "target_parent_id": syntheticUUID(1),
					},
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := emptyManifestDocument()
			test.mutate(document)
			assertManifestCode(t, writeManifest(t, mustJSON(t, document), 0o600), "manifest_decision_conflict")
		})
	}
}

func TestLoadManifestRejectsAmbiguousOrCyclicReparentActions(t *testing.T) {
	document := emptyManifestDocument()
	document["comment_reparent_resolutions"] = []any{
		map[string]any{
			"comment_id": syntheticUUID(1), "action": "set_parent",
			"target_parent_id": syntheticUUID(2), "target_reply_to_user_id": syntheticUUID(3),
		},
	}
	assertManifestCode(t, writeManifest(t, mustJSON(t, document), 0o600), "manifest_action_fields_invalid")

	document = emptyManifestDocument()
	document["comment_reparent_resolutions"] = []any{
		map[string]any{
			"comment_id": syntheticUUID(1), "action": "set_parent", "target_parent_id": syntheticUUID(2),
		},
		map[string]any{
			"comment_id": syntheticUUID(2), "action": "set_parent", "target_parent_id": syntheticUUID(1),
		},
	}
	assertManifestCode(t, writeManifest(t, mustJSON(t, document), 0o600), "manifest_decision_conflict")

	document = emptyManifestDocument()
	document["comment_reparent_resolutions"] = []any{
		map[string]any{
			"comment_id": syntheticUUID(1), "action": "set_parent_and_reply_to",
			"target_parent_id": syntheticUUID(2), "target_reply_to_user_id": syntheticUUID(3),
		},
	}
	if _, _, err := loadManifestForTest(t, writeManifest(t, mustJSON(t, document), 0o600)); err != nil {
		t.Fatalf("显式组合 action 应被接受：%v", err)
	}
}

func TestLoadManifestRejectsReferencesToOtherExcludedEntities(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "post type for excluded post",
			mutate: func(document map[string]any) {
				document["excluded_content"] = []any{
					map[string]any{"content_type": "post", "content_id": syntheticUUID(1), "action": "exclude"},
				}
				document["post_type_resolutions"] = []any{
					map[string]any{"post_id": syntheticUUID(1), "action": "set_type", "target_type": "share"},
				}
			},
		},
		{
			name: "post image targets excluded asset",
			mutate: func(document map[string]any) {
				document["post_image_resolutions"] = []any{
					map[string]any{
						"post_id": syntheticUUID(1), "source_reference": "source-a", "action": "map",
						"target_image_asset_id": syntheticUUID(2),
					},
				}
				document["duplicate_image_asset_resolutions"] = duplicateAssetGroup(syntheticUUID(3), syntheticUUID(2))
			},
		},
		{
			name: "avatar targets excluded asset",
			mutate: func(document map[string]any) {
				document["avatar_resolutions"] = []any{
					map[string]any{
						"user_id": syntheticUUID(1), "action": "replace", "target_image_asset_id": syntheticUUID(2),
					},
				}
				document["duplicate_image_asset_resolutions"] = duplicateAssetGroup(syntheticUUID(3), syntheticUUID(2))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := emptyManifestDocument()
			test.mutate(document)
			assertManifestCode(t, writeManifest(t, mustJSON(t, document), 0o600), "manifest_decision_conflict")
		})
	}
}

func TestLoadManifestErrorsNeverEchoPathValuesOrParserDetails(t *testing.T) {
	privateValue := "private-manifest-value-do-not-print"
	path := writeManifest(t, []byte(`{"schema_version":1,"excluded_users":["`+privateValue+`"`), 0o600)
	_, _, err := loadManifestForTest(t, path)
	if err == nil {
		t.Fatal("非法 JSON 应被拒绝")
	}
	encoded, marshalErr := json.Marshal(ErrorReport(err))
	if marshalErr != nil {
		t.Fatalf("json.Marshal: %v", marshalErr)
	}
	for _, forbidden := range []string{privateValue, path, "unexpected EOF", "invalid character"} {
		if strings.Contains(err.Error(), forbidden) || strings.Contains(string(encoded), forbidden) {
			t.Fatalf("manifest 错误泄露私有值或底层细节 %q：error=%q report=%s", forbidden, err, encoded)
		}
	}

	missingPath := filepath.Join(t.TempDir(), privateValue+".json")
	_, _, err = loadManifestForTest(t, missingPath)
	if err == nil || strings.Contains(err.Error(), privateValue) {
		t.Fatalf("文件错误泄露私有路径或没有失败：%v", err)
	}
}

func emptyManifestDocument() map[string]any {
	return map[string]any{
		"schema_version":                    ManifestSchemaVersion,
		"excluded_users":                    []any{},
		"excluded_content":                  []any{},
		"email_rewrites":                    []any{},
		"post_type_resolutions":             []any{},
		"dictionary_mappings":               []any{},
		"post_image_resolutions":            []any{},
		"avatar_resolutions":                []any{},
		"duplicate_image_asset_resolutions": []any{},
		"comment_reparent_resolutions":      []any{},
		"orphan_like_exclusions":            []any{},
		"orphan_notification_exclusions":    []any{},
	}
}

func emptyManifestJSON() []byte {
	return []byte(`{"schema_version":1,"excluded_users":[],"excluded_content":[],"email_rewrites":[],"post_type_resolutions":[],"dictionary_mappings":[],"post_image_resolutions":[],"avatar_resolutions":[],"duplicate_image_asset_resolutions":[],"comment_reparent_resolutions":[],"orphan_like_exclusions":[],"orphan_notification_exclusions":[]}`)
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return data
}

func writeManifest(t *testing.T, data []byte, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	return path
}

func assertManifestCode(t *testing.T, path, code string) {
	t.Helper()
	_, _, err := loadManifestForTest(t, path)
	if err == nil || err.Error() != code {
		t.Fatalf("期望 %s，实际 %v", code, err)
	}
}

func loadManifestForTest(t *testing.T, path string) (manifestData, ManifestDigest, error) {
	t.Helper()
	data, readErr := os.ReadFile(path)
	expected := ManifestDigest(sha256.Sum256(data))
	if readErr != nil || expected == (ManifestDigest{}) {
		expected = ManifestDigest(sha256.Sum256([]byte("synthetic-independent-approval")))
	}
	approved, err := LoadManifest(path, expected)
	if err != nil {
		return manifestData{}, expected, err
	}
	manifest, err := approved.verify(expected)
	return manifest, expected, err
}

func syntheticUUID(index int) string {
	return fmt.Sprintf("10000000-0000-4000-8000-%012d", index)
}

func duplicateAssetGroup(keepID, excludeID string) []any {
	return []any{
		map[string]any{"group_key": "duplicate-group", "image_asset_id": keepID, "action": "keep"},
		map[string]any{"group_key": "duplicate-group", "image_asset_id": excludeID, "action": "exclude"},
	}
}
