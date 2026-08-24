package legacyimporter

import (
	"bytes"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRegisterSequentialIDsUsesSourceRowNumbers(t *testing.T) {
	t.Parallel()

	rows := []sourceUser{
		{TargetID: 1, ID: "4a4e232a-63b5-4d7e-b17e-684cd064377c"},
		{TargetID: 2, ID: "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"},
	}
	ids := map[string]int64{}
	require.NoError(t, registerSequentialIDs(rows, ids,
		func(row sourceUser) string { return row.ID }, func(row sourceUser) int64 { return row.TargetID }, "users"))
	require.Equal(t, int64(1), ids[rows[0].ID])
	require.Equal(t, int64(2), ids[rows[1].ID])

	rows[1].TargetID = 1
	require.ErrorContains(t, registerSequentialIDs(rows, map[string]int64{},
		func(row sourceUser) string { return row.ID }, func(row sourceUser) int64 { return row.TargetID }, "users"),
		"code=duplicate_sequential_id")
	rows[1].TargetID, rows[1].ID = 2, "not-a-uuid"
	require.ErrorContains(t, registerSequentialIDs(rows, map[string]int64{},
		func(row sourceUser) string { return row.ID }, func(row sourceUser) int64 { return row.TargetID }, "users"),
		"code=invalid_uuid")
}

func TestValidateLocalDSN(t *testing.T) {
	t.Parallel()

	identity, err := validateLocalDSN("postgres://user:secret@127.0.0.1:55432/source")
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:55432/source", identity)
	_, err = validateLocalDSN("postgres://user:secret@db.example.com:5432/source")
	require.Error(t, err)
	_, err = validateLocalDSN("postgres://user:secret@127.0.0.1:55432/")
	require.Error(t, err)
}

func TestInferReplyParentRequiresExactlyOneEarlierCandidateInFloor(t *testing.T) {
	t.Parallel()

	t0 := time.Date(2026, time.August, 24, 1, 0, 0, 0, time.UTC)
	replyTo := "user-b"
	comments := []sourceComment{
		{ID: "root", PostID: "post", AuthorID: "user-a", CreatedAt: t0},
		{ID: "candidate", PostID: "post", AuthorID: replyTo, ParentID: stringPointer("root"), CreatedAt: t0.Add(time.Minute)},
		{ID: "current", PostID: "post", AuthorID: "user-c", ParentID: stringPointer("root"), ReplyToUserID: &replyTo, CreatedAt: t0.Add(2 * time.Minute)},
		{ID: "other-floor", PostID: "post", AuthorID: replyTo, CreatedAt: t0.Add(time.Minute)},
	}
	roots := map[string]string{"root": "root", "candidate": "root", "current": "root", "other-floor": "other-floor"}
	candidate, err := inferReplyParent(comments[2], comments, roots)
	require.NoError(t, err)
	require.Equal(t, "candidate", candidate.ID)

	comments = append(comments, sourceComment{
		ID: "ambiguous", PostID: "post", AuthorID: replyTo,
		ParentID: stringPointer("root"), CreatedAt: t0.Add(90 * time.Second),
	})
	roots["ambiguous"] = "root"
	_, err = inferReplyParent(comments[2], comments, roots)
	require.ErrorContains(t, err, "code=reply_parent_not_unique")
}

func TestNullablePostJSONRequiresTheExpectedContainerType(t *testing.T) {
	t.Parallel()

	jsonNull := sql.NullString{String: "null", Valid: true}
	nullType := sql.NullString{String: "null", Valid: true}
	var flavors []string
	require.NoError(t, decodeNullableStringArray(jsonNull, nullType, &flavors))
	require.Nil(t, flavors)

	arrayJSON := sql.NullString{String: `["咸","辣"]`, Valid: true}
	arrayType := sql.NullString{String: "array", Valid: true}
	require.NoError(t, decodeNullableStringArray(arrayJSON, arrayType, &flavors))
	require.Equal(t, []string{"咸", "辣"}, flavors)
	require.Error(t, decodeNullableStringArray(arrayJSON, sql.NullString{String: "object", Valid: true}, &flavors))

	var preferences *sourcePreferences
	require.NoError(t, decodeNullablePreferences(jsonNull, nullType, &preferences))
	require.Nil(t, preferences)
	require.NoError(t, decodeNullablePreferences(
		sql.NullString{String: `{"prefer_flavors":["清淡"],"avoid_flavors":["重辣"]}`, Valid: true},
		sql.NullString{String: "object", Valid: true}, &preferences,
	))
	require.Equal(t, []string{"清淡"}, preferences.PreferFlavors)
	require.Equal(t, []string{"重辣"}, preferences.AvoidFlavors)
	require.Error(t, decodeNullablePreferences(
		sql.NullString{String: `[]`, Valid: true}, sql.NullString{String: "array", Valid: true}, &preferences,
	))
}

func TestPostFlavorMappingPreservesStanceAndDeduplicates(t *testing.T) {
	t.Parallel()

	dict := dictionaries{Flavors: map[string]dictionaryItem{
		"清淡": {ID: 101}, "麻辣": {ID: 102}, "特辣": {ID: 103},
	}}
	data := newDataset(sourceData{})
	createdAt := time.Date(2026, time.August, 24, 2, 0, 0, 0, time.UTC)
	share := sourcePost{
		ID: "share", Flavors: []string{"咸", "咸", "辣", "酸甜"}, CreatedAt: createdAt,
	}
	require.NoError(t, transformPostFlavors(share, "share", 1, dict, &data))
	require.Len(t, data.PostFlavors, 3)
	require.Len(t, data.Flavors, 3)
	for _, flavor := range data.Flavors {
		require.False(t, flavor.IsActive)
		require.Greater(t, flavor.SortOrder, int32(999))
	}
	require.Equal(t, 1, countDecisionEvents(data.Events, "flavor_mapping_deduplicated"))

	seeking := sourcePost{
		ID: "seeking", CreatedAt: createdAt,
		Preferences: &sourcePreferences{
			PreferFlavors: []string{"清淡"}, AvoidFlavors: []string{"麻辣", "重辣", "重辣"},
		},
	}
	require.NoError(t, transformPostFlavors(seeking, "seeking", 2, dict, &data))
	require.Len(t, data.PostFlavors, 6)
	require.Equal(t, 2, countDecisionEvents(data.Events, "flavor_mapping_deduplicated"))

	stances := map[int64]string{}
	for _, row := range data.PostFlavors {
		require.Equal(t, row.PostType == "share", row.Stance == "has")
		if row.PostID == 2 {
			stances[row.FlavorID] = row.Stance
		}
	}
	require.Equal(t, map[int64]string{101: "prefer", 102: "avoid", 103: "avoid"}, stances)
	require.ErrorContains(t, addPostFlavor("post", "preferences", 2, 101, "avoid", "seeking", &data),
		"code=conflicting_flavor_stance")
	require.ErrorContains(t, addPostFlavor("post", "preferences", 3, 101, "prefer", "share", &data),
		"code=flavor_stance_post_type_mismatch")
}

func TestCuisineMappingUsesSeedAliasesAndInactiveHistoricalTerms(t *testing.T) {
	t.Parallel()

	dict := dictionaries{Cuisines: map[string]dictionaryItem{
		"西式": {ID: 201}, "其他": {ID: 202},
	}}
	data := newDataset(sourceData{})
	createdAt := time.Date(2026, time.August, 24, 3, 0, 0, 0, time.UTC)
	expected := map[string]int64{"西餐": 201, "快餐": 202}
	for _, name := range []string{"西餐", "快餐", "云南菜", "台湾菜", "江西菜"} {
		id, err := resolveCuisine(sourcePost{ID: name, Cuisine: stringPointer(name), CreatedAt: createdAt}, dict, &data)
		require.NoError(t, err)
		require.NotNil(t, id)
		if seedID, exists := expected[name]; exists {
			require.Equal(t, seedID, *id)
		}
	}
	require.Len(t, data.Cuisines, 3)
	for _, cuisine := range data.Cuisines {
		require.False(t, cuisine.IsActive)
		require.Greater(t, cuisine.SortOrder, int32(99))
	}
}

func countDecisionEvents(events []decisionEvent, code string) int {
	count := 0
	for _, event := range events {
		if event.Code == code {
			count++
		}
	}
	return count
}

func TestReportsNeverContainComparedValues(t *testing.T) {
	t.Parallel()

	data := newDataset(sourceData{})
	data.Users[1] = userRow{SourceID: "source-user", ID: 1, Email: "private@example.com", PasswordHash: "secret-hash"}
	issues := []mismatch{{Table: "users", SourceID: "source-user", Field: "email", Code: "value_mismatch"}}
	var output bytes.Buffer
	writeVerifyReport(&output, data, issues)
	report := output.String()
	require.NotContains(t, report, "private@example.com")
	require.NotContains(t, report, "secret-hash")
	require.Contains(t, report, "source_id=source-user field=email code=value_mismatch")
	require.True(t, strings.HasSuffix(report, "VERIFY_FAILED mismatches=1\n"))
}

func TestVerifyRejectsIDsOutsideJavaScriptSafeIntegerRange(t *testing.T) {
	t.Parallel()

	data := newDataset(sourceData{})
	unsafeID := javaScriptMaxSafeInteger + 1
	data.Users[unsafeID] = userRow{SourceID: "source-user", ID: unsafeID}
	issues := javaScriptSafeIDMismatches(data)
	require.Equal(t, []mismatch{{
		Table: "users", SourceID: "source-user", Field: "id", Code: "javascript_unsafe_integer",
	}}, issues)

	data.Users = map[int64]userRow{
		javaScriptMaxSafeInteger: {SourceID: "safe-user", ID: javaScriptMaxSafeInteger},
	}
	require.Empty(t, javaScriptSafeIDMismatches(data))
}
