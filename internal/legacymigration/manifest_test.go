package legacymigration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadManifestAcceptsExplicitEmptySections(t *testing.T) {
	path := writeManifest(t, emptyManifestJSON(), 0o400)
	manifest, err := LoadManifest(path)
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
	for index, section := range summary.Sections {
		if section.Code != requiredManifestSections[index] || section.Count != 0 {
			t.Fatalf("固定 section code 或计数错误：%+v", summary.Sections)
		}
	}
}

func TestLoadManifestAcceptsValidatedNonEmptyDecisions(t *testing.T) {
	document := emptyManifestDocument()
	document["excluded_users"] = []any{
		map[string]any{"user_id": "user-excluded", "action": "exclude"},
	}
	document["excluded_content"] = []any{
		map[string]any{"content_type": "post", "content_id": "post-excluded", "action": "exclude"},
		map[string]any{"content_type": "comment", "content_id": "comment-excluded", "action": "exclude"},
	}
	document["email_rewrites"] = []any{
		map[string]any{"user_id": "user-email", "action": "rewrite", "new_email": "rewritten@example.invalid"},
	}
	document["post_type_resolutions"] = []any{
		map[string]any{"post_id": "post-type", "action": "set_type", "target_type": "share"},
	}
	document["dictionary_mappings"] = []any{
		map[string]any{"dictionary": "cuisine", "source": "source-cuisine", "action": "map", "target": "target-cuisine"},
	}
	document["post_image_resolutions"] = []any{
		map[string]any{
			"post_id": "post-image", "source_reference": "source-image", "action": "map",
			"target_image_asset_id": "asset-keep",
		},
	}
	document["avatar_resolutions"] = []any{
		map[string]any{"user_id": "user-avatar", "action": "replace", "target_image_asset_id": "asset-keep"},
	}
	document["duplicate_image_asset_resolutions"] = []any{
		map[string]any{"group_key": "duplicate-group", "image_asset_id": "asset-keep", "action": "keep"},
		map[string]any{"group_key": "duplicate-group", "image_asset_id": "asset-drop", "action": "exclude"},
	}
	document["comment_reparent_resolutions"] = []any{
		map[string]any{
			"comment_id": "comment-reparent", "action": "set_reply_to",
			"target_reply_to_user_id": "user-reply",
		},
	}
	document["orphan_like_exclusions"] = []any{
		map[string]any{"like_id": "like-orphan", "action": "exclude"},
	}
	document["orphan_notification_exclusions"] = []any{
		map[string]any{"notification_id": "notification-orphan", "action": "exclude"},
	}

	manifest, err := LoadManifest(writeManifest(t, mustJSON(t, document), 0o600))
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
		map[string]any{"user_id": "user-a", "action": "exclude", "unexpected": true},
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
		`"excluded_users":[{"user_id":"user-a","user_id":"user-b","action":"exclude"}]`,
		1,
	)
	assertManifestCode(t, writeManifest(t, []byte(nested), 0o600), "manifest_duplicate_key")
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
}

func TestLoadManifestRejectsOversizedFile(t *testing.T) {
	data := make([]byte, int(MaxManifestBytes)+1)
	assertManifestCode(t, writeManifest(t, data, 0o600), "manifest_too_large")
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
				document["orphan_like_exclusions"] = []any{map[string]any{"like_id": "like-a", "action": "ignore"}}
			},
			code: "manifest_action_unknown",
		},
		{
			name: "duplicate decision",
			mutate: func(document map[string]any) {
				decision := map[string]any{"notification_id": "notification-a", "action": "exclude"}
				document["orphan_notification_exclusions"] = []any{decision, decision}
			},
			code: "manifest_entry_duplicate",
		},
		{
			name: "cross section conflict",
			mutate: func(document map[string]any) {
				document["excluded_users"] = []any{map[string]any{"user_id": "user-a", "action": "exclude"}}
				document["email_rewrites"] = []any{
					map[string]any{"user_id": "user-a", "action": "rewrite", "new_email": "rewritten@example.invalid"},
				}
			},
			code: "manifest_decision_conflict",
		},
		{
			name: "duplicate asset group without unique keep",
			mutate: func(document map[string]any) {
				document["duplicate_image_asset_resolutions"] = []any{
					map[string]any{"group_key": "group-a", "image_asset_id": "asset-a", "action": "exclude"},
					map[string]any{"group_key": "group-a", "image_asset_id": "asset-b", "action": "exclude"},
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

func TestLoadManifestErrorsNeverEchoPathValuesOrParserDetails(t *testing.T) {
	privateValue := "private-manifest-value-do-not-print"
	path := writeManifest(t, []byte(`{"schema_version":1,"excluded_users":["`+privateValue+`"`), 0o600)
	_, err := LoadManifest(path)
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
	_, err = LoadManifest(missingPath)
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
	_, err := LoadManifest(path)
	if err == nil || err.Error() != code {
		t.Fatalf("期望 %s，实际 %v", code, err)
	}
}
