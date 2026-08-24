package legacyimporter

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMapUUIDIsStablePositiveAndNamespaced(t *testing.T) {
	t.Parallel()

	first, err := mapUUID("4a4e232a-63b5-4d7e-b17e-684cd064377c")
	require.NoError(t, err)
	second, err := mapUUID("4A4E232A-63B5-4D7E-B17E-684CD064377C")
	require.NoError(t, err)
	require.Equal(t, int64(2287392585008901255), first)
	require.Equal(t, first, second)
	require.Positive(t, first)
	require.NotEqual(t, first, mapText("tag", "4a4e232a-63b5-4d7e-b17e-684cd064377c"))
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
