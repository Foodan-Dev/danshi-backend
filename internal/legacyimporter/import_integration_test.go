package legacyimporter

import (
	"bytes"
	"context"
	"database/sql"
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

	var secondImport bytes.Buffer
	require.NoError(t, Import(ctx, sourceDSN, targetDSN, &secondImport))
	require.Equal(t, firstImport.String(), secondImport.String())
	var secondVerify bytes.Buffer
	require.NoError(t, Verify(ctx, sourceDSN, targetDSN, &secondVerify))
	require.Equal(t, firstVerify.String(), secondVerify.String())
	assertHistoricalDictionaryMappings(ctx, t, targetDSN)

	corruptTargetForMismatchTest(ctx, t, targetDSN)
	var failedVerify bytes.Buffer
	require.Error(t, Verify(ctx, sourceDSN, targetDSN, &failedVerify))
	require.Regexp(t, regexp.MustCompile(`MISMATCH table=posts source_id=[0-9a-f-]+ field=view_count code=value_mismatch`),
		failedVerify.String())
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
