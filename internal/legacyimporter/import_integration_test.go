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

	var secondImport bytes.Buffer
	require.NoError(t, Import(ctx, sourceDSN, targetDSN, &secondImport))
	require.Equal(t, firstImport.String(), secondImport.String())
	var secondVerify bytes.Buffer
	require.NoError(t, Verify(ctx, sourceDSN, targetDSN, &secondVerify))
	require.Equal(t, firstVerify.String(), secondVerify.String())

	corruptTargetForMismatchTest(ctx, t, targetDSN)
	var failedVerify bytes.Buffer
	require.Error(t, Verify(ctx, sourceDSN, targetDSN, &failedVerify))
	require.Regexp(t, regexp.MustCompile(`MISMATCH table=posts source_id=[0-9a-f-]+ field=view_count code=value_mismatch`),
		failedVerify.String())
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
