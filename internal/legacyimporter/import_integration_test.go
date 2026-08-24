package legacyimporter

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	dbinfra "github.com/Foodan-Dev/danshi-backend/internal/infra/db"
)

func TestAdvanceIdentitySequencesAllowsNormalInserts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	_, targetDSN := startPostgres(ctx, t, "sequence_target", "sequence-test-password")
	migrateTarget(ctx, t, targetDSN)
	database, err := sql.Open("pgx", targetDSN)
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()

	tx, err := database.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `SET LOCAL danshi.allow_counter_write='on'`)
	require.NoError(t, err)
	statements := []string{
		`INSERT INTO users (id,email,password_hash,name) VALUES (10000,'legacy-sequence@example.com','hash','legacy')`,
		`INSERT INTO user_role_records (id,user_id,role,action) VALUES (10000,10000,'moderator','grant')`,
		`INSERT INTO user_ban_records (id,user_id,action,ban_is_permanent,reason) VALUES (10000,10000,'ban',true,'legacy')`,
		`INSERT INTO image_assets (id,uploader_id,object_key,public_url,content_type) VALUES (10000,10000,'legacy-key','https://example.com/legacy','image/jpeg')`,
		`INSERT INTO cuisines (id,name) VALUES (10000,'legacy-cuisine')`,
		`INSERT INTO flavors (id,name) VALUES (10000,'legacy-flavor')`,
		`INSERT INTO posts (id,author_id,post_type,share_type,category,title,content) VALUES (10000,10000,'share','recommend','food','legacy','legacy')`,
		`INSERT INTO tags (id,name) VALUES (10000,'legacy-tag')`,
		`INSERT INTO comments (id,post_id,author_id,reply_to_user_id,content,moderation) VALUES (10000,10000,10000,10000,'legacy','pass')`,
		`INSERT INTO notifications (id,recipient_id,sender_id,type) VALUES (10000,10000,10000,'follow')`,
	}
	for _, statement := range statements {
		_, err = tx.ExecContext(ctx, statement)
		require.NoError(t, err)
	}
	require.NoError(t, advanceIdentitySequences(ctx, tx, dataset{}))
	require.NoError(t, tx.Commit())

	assertNormalIdentityInserts(ctx, t, database)
}

func TestImportAndVerifyFromDump(t *testing.T) {
	dumpPath := os.Getenv("DANSHI_LEGACY_DUMP")
	if dumpPath == "" {
		t.Skip("DANSHI_LEGACY_DUMP is not set")
	}
	if _, err := os.Stat(dumpPath); err != nil {
		t.Fatalf("DANSHI_LEGACY_DUMP is not readable: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	t.Cleanup(cancel)
	sourceContainer, sourceDSN := startPostgres(ctx, t, "legacy_source", "source-test-password")
	restoreDump(ctx, t, sourceContainer, dumpPath)
	_, targetDSN := startPostgres(ctx, t, "danshi_target", "target-test-password")
	migrateTarget(ctx, t, targetDSN)

	var firstImport bytes.Buffer
	require.NoError(t, Import(ctx, sourceDSN, targetDSN, &firstImport))
	require.Contains(t, firstImport.String(), "IMPORT_OK users=47 posts=33 comments=109 images=38 notifications=384")

	var firstVerify bytes.Buffer
	require.NoError(t, Verify(ctx, sourceDSN, targetDSN, &firstVerify))
	require.Contains(t, firstVerify.String(), "VERIFY_OK mismatches=0")
	require.NotContains(t, firstVerify.String(), "MISMATCH")
	assertHistoricalDictionaryMappings(ctx, t, targetDSN)
	firstIDs := readIdentityIDs(ctx, t, targetDSN)

	var secondImport bytes.Buffer
	require.NoError(t, Import(ctx, sourceDSN, targetDSN, &secondImport))
	require.Equal(t, firstImport.String(), secondImport.String())
	var secondVerify bytes.Buffer
	require.NoError(t, Verify(ctx, sourceDSN, targetDSN, &secondVerify))
	require.Equal(t, firstVerify.String(), secondVerify.String())
	assertHistoricalDictionaryMappings(ctx, t, targetDSN)
	require.Equal(t, firstIDs, readIdentityIDs(ctx, t, targetDSN))
	assertNormalIdentityInsertsForDSN(ctx, t, targetDSN)

	corruptTargetForMismatchTest(ctx, t, targetDSN)
	var failedVerify bytes.Buffer
	require.Error(t, Verify(ctx, sourceDSN, targetDSN, &failedVerify))
	require.Regexp(t, regexp.MustCompile(`MISMATCH table=posts source_id=[0-9a-f-]+ field=view_count code=value_mismatch`),
		failedVerify.String())
}

func readIdentityIDs(ctx context.Context, t *testing.T, dsn string) map[string][]int64 {
	t.Helper()
	database, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()
	result := make(map[string][]int64, len(explicitlyImportedIdentityTables))
	for _, table := range explicitlyImportedIdentityTables {
		query := fmt.Sprintf("SELECT id FROM %s ORDER BY id", table) // table comes from the fixed importer allowlist.
		rows, queryErr := database.QueryContext(ctx, query)
		require.NoError(t, queryErr)
		for rows.Next() {
			var id int64
			require.NoError(t, rows.Scan(&id))
			require.LessOrEqual(t, id, javaScriptMaxSafeInteger)
			result[table] = append(result[table], id)
		}
		require.NoError(t, rows.Err())
		closeRows(rows)
	}
	return result
}

func assertNormalIdentityInsertsForDSN(ctx context.Context, t *testing.T, dsn string) {
	t.Helper()
	database, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()
	assertNormalIdentityInserts(ctx, t, database)
}

func assertNormalIdentityInserts(ctx context.Context, t *testing.T, database *sql.DB) {
	t.Helper()
	maxIDs := map[string]int64{}
	for _, table := range explicitlyImportedIdentityTables {
		query := fmt.Sprintf("SELECT COALESCE(MAX(id),0) FROM %s", table) // table comes from the fixed importer allowlist.
		var maxID int64
		require.NoError(t, database.QueryRowContext(ctx, query).Scan(&maxID))
		maxIDs[table] = maxID
	}
	tx, err := database.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, tx.Rollback()) }()

	insert := func(table, query string, args ...any) int64 {
		t.Helper()
		var id int64
		require.NoError(t, tx.QueryRowContext(ctx, query, args...).Scan(&id))
		require.Greater(t, id, maxIDs[table], "identity sequence for %s did not advance", table)
		return id
	}
	userID := insert("users", `
		INSERT INTO users (email,password_hash,name) VALUES ('normal-sequence@example.com','hash','normal') RETURNING id`)
	insert("user_role_records", `
		INSERT INTO user_role_records (user_id,role,action) VALUES ($1,'moderator','grant') RETURNING id`, userID)
	insert("user_ban_records", `
		INSERT INTO user_ban_records (user_id,action,ban_is_permanent,reason) VALUES ($1,'ban',true,'normal') RETURNING id`, userID)
	insert("image_assets", `
		INSERT INTO image_assets (uploader_id,object_key,public_url,content_type)
		VALUES ($1,'normal-key','https://example.com/normal','image/jpeg') RETURNING id`, userID)
	insert("cuisines", `INSERT INTO cuisines (name) VALUES ('normal-cuisine') RETURNING id`)
	insert("flavors", `INSERT INTO flavors (name) VALUES ('normal-flavor') RETURNING id`)
	postID := insert("posts", `
		INSERT INTO posts (author_id,post_type,share_type,category,title,content)
		VALUES ($1,'share','recommend','food','normal','normal') RETURNING id`, userID)
	insert("tags", `INSERT INTO tags (name) VALUES ('normal-tag') RETURNING id`)
	insert("comments", `
		INSERT INTO comments (post_id,author_id,reply_to_user_id,content)
		VALUES ($1,$2,$2,'normal') RETURNING id`, postID, userID)
	insert("notifications", `
		INSERT INTO notifications (recipient_id,sender_id,type) VALUES ($1,$1,'follow') RETURNING id`, userID)
}

func assertHistoricalDictionaryMappings(ctx context.Context, t *testing.T, dsn string) {
	t.Helper()
	database, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()

	assertInactiveDictionaries(ctx, t, database,
		`SELECT name,is_active,sort_order FROM cuisines WHERE name IN ($1,$2,$3) ORDER BY name`,
		[]string{"云南菜", "台湾菜", "江西菜"}, 99)
	assertInactiveDictionaries(ctx, t, database,
		`SELECT name,is_active,sort_order FROM flavors WHERE name IN ($1,$2,$3) ORDER BY name`,
		[]string{"咸", "辣", "酸甜"}, 999)

	var unexpectedCuisineAliases int
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT count(*) FROM cuisines WHERE name IN ('西餐','快餐')`).Scan(&unexpectedCuisineAliases))
	require.Zero(t, unexpectedCuisineAliases)

	rows, err := database.QueryContext(ctx, `
		SELECT f.name,pf.stance,count(*)
		FROM post_flavors AS pf JOIN flavors AS f ON f.id=pf.flavor_id
		WHERE f.name IN ('咸','辣','酸甜','清淡','麻辣','特辣')
		GROUP BY f.name,pf.stance ORDER BY f.name,pf.stance`)
	require.NoError(t, err)
	defer closeRows(rows)
	actual := map[string]int{}
	for rows.Next() {
		var name, stance string
		var count int
		require.NoError(t, rows.Scan(&name, &stance, &count))
		actual[name+":"+stance] = count
	}
	require.NoError(t, rows.Err())
	require.Equal(t, map[string]int{
		"咸:has": 1, "辣:has": 1, "酸甜:has": 1,
		"清淡:prefer": 2, "麻辣:avoid": 1, "特辣:avoid": 1,
	}, actual)

	var invalidStances int
	require.NoError(t, database.QueryRowContext(ctx, `
		SELECT count(*) FROM post_flavors
		WHERE (post_type='share') IS DISTINCT FROM (stance='has')`).Scan(&invalidStances))
	require.Zero(t, invalidStances)
}

func assertInactiveDictionaries(
	ctx context.Context,
	t *testing.T,
	database *sql.DB,
	query string,
	names []string,
	seedMaxSortOrder int,
) {
	t.Helper()
	rows, err := database.QueryContext(ctx, query, names[0], names[1], names[2])
	require.NoError(t, err)
	defer closeRows(rows)
	found := map[string]bool{}
	for rows.Next() {
		var name string
		var active bool
		var sortOrder int
		require.NoError(t, rows.Scan(&name, &active, &sortOrder))
		require.False(t, active)
		require.Greater(t, sortOrder, seedMaxSortOrder)
		found[name] = true
	}
	require.NoError(t, rows.Err())
	require.Len(t, found, len(names))
	for _, name := range names {
		require.True(t, found[name])
	}
}

func startPostgres(ctx context.Context, t *testing.T, database, password string) (testcontainers.Container, string) {
	t.Helper()
	container, err := tcpostgres.Run(ctx, "postgres:18",
		tcpostgres.WithDatabase(database), tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword(password), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	testcontainers.CleanupContainer(t, container)
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	return container, dsn
}

func restoreDump(ctx context.Context, t *testing.T, container testcontainers.Container, dumpPath string) {
	t.Helper()
	require.NoError(t, container.CopyFileToContainer(ctx, dumpPath, "/tmp/source.dump", 0o600))
	exitCode, output, err := container.Exec(ctx, []string{
		"pg_restore", "--exit-on-error", "--no-owner", "--no-privileges",
		"--username=postgres", "--dbname=legacy_source", "/tmp/source.dump",
	})
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, output)
	require.Equal(t, 0, exitCode, "pg_restore failed with exit code %d", exitCode)
}

func migrateTarget(ctx context.Context, t *testing.T, dsn string) {
	t.Helper()
	database, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	require.NoError(t, database.PingContext(ctx))
	require.NoError(t, dbinfra.Up(ctx, database))
}

func corruptTargetForMismatchTest(ctx context.Context, t *testing.T, dsn string) {
	t.Helper()
	database, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer func() { require.NoError(t, database.Close()) }()
	_, err = database.ExecContext(ctx, `
		UPDATE posts SET view_count=view_count+1
		WHERE id=(SELECT min(id) FROM posts)`)
	require.NoError(t, err)
}
