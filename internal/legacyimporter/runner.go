package legacyimporter

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sort"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib" // Register pgx with database/sql for the standalone importer.

	dbinfra "github.com/Foodan-Dev/danshi-backend/internal/infra/db"
)

// Import reads one repeatable, read-only source snapshot and atomically converges an empty target.
func Import(ctx context.Context, sourceDSN, targetDSN string, output io.Writer) error {
	sourceDB, targetDB, err := openDatabases(ctx, sourceDSN, targetDSN)
	if err != nil {
		return err
	}
	defer func() { _ = sourceDB.Close() }()
	defer func() { _ = targetDB.Close() }()
	dict, err := prepareTarget(ctx, targetDB)
	if err != nil {
		return err
	}
	source, err := loadSourceData(ctx, sourceDB)
	if err != nil {
		return err
	}
	data, err := transformSource(source, dict)
	if err != nil {
		return err
	}
	nonempty, err := targetHasBusinessRows(ctx, targetDB)
	if err != nil {
		return err
	}
	if nonempty {
		mismatches, collectErr := collectMismatches(ctx, targetDB, data, dict)
		if collectErr != nil {
			return collectErr
		}
		if len(mismatches) != 0 {
			return failure("target_not_empty_or_imported", "", nil)
		}
	}
	if err = writeDataset(ctx, targetDB, data); err != nil {
		return err
	}
	writeImportReport(output, data)
	return nil
}

// Verify performs a read-only, row-by-row comparison and writes a value-free report.
func Verify(ctx context.Context, sourceDSN, targetDSN string, output io.Writer) error {
	sourceDB, targetDB, err := openDatabases(ctx, sourceDSN, targetDSN)
	if err != nil {
		return err
	}
	defer func() { _ = sourceDB.Close() }()
	defer func() { _ = targetDB.Close() }()
	dict, err := prepareTarget(ctx, targetDB)
	if err != nil {
		return err
	}
	source, err := loadSourceData(ctx, sourceDB)
	if err != nil {
		return err
	}
	data, err := transformSource(source, dict)
	if err != nil {
		return err
	}
	mismatches, err := collectMismatches(ctx, targetDB, data, dict)
	if err != nil {
		return err
	}
	writeVerifyReport(output, data, mismatches)
	if len(mismatches) != 0 {
		return failure("verify_mismatch", "", nil)
	}
	return nil
}

func openDatabases(ctx context.Context, sourceDSN, targetDSN string) (*sql.DB, *sql.DB, error) {
	sourceIdentity, err := validateLocalDSN(sourceDSN)
	if err != nil {
		return nil, nil, failure("source_dsn_rejected", "", err)
	}
	targetIdentity, err := validateLocalDSN(targetDSN)
	if err != nil {
		return nil, nil, failure("target_dsn_rejected", "", err)
	}
	if sourceIdentity == targetIdentity {
		return nil, nil, failure("source_target_same_database", "", nil)
	}
	sourceDB, err := sql.Open("pgx", sourceDSN)
	if err != nil {
		return nil, nil, failure("source_open_failed", "", err)
	}
	sourceDB.SetMaxOpenConns(1)
	if err = sourceDB.PingContext(ctx); err != nil {
		_ = sourceDB.Close()
		return nil, nil, failure("source_unreachable", "", err)
	}
	targetDB, err := sql.Open("pgx", targetDSN)
	if err != nil {
		_ = sourceDB.Close()
		return nil, nil, failure("target_open_failed", "", err)
	}
	targetDB.SetMaxOpenConns(2)
	if err = targetDB.PingContext(ctx); err != nil {
		_ = targetDB.Close()
		_ = sourceDB.Close()
		return nil, nil, failure("target_unreachable", "", err)
	}
	return sourceDB, targetDB, nil
}

func validateLocalDSN(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return "", errors.New("invalid postgres url")
	}
	host := strings.ToLower(parsed.Hostname())
	local := host == "localhost"
	if ip := net.ParseIP(host); ip != nil {
		local = ip.IsLoopback()
	}
	database := strings.TrimPrefix(parsed.EscapedPath(), "/")
	if !local || host == "" || database == "" {
		return "", errors.New("database must use a loopback host and explicit name")
	}
	return net.JoinHostPort(host, parsed.Port()) + "/" + database, nil
}

func prepareTarget(ctx context.Context, target *sql.DB) (dictionaries, error) {
	if err := dbinfra.AssertVersion(ctx, target); err != nil {
		return dictionaries{}, failure("target_schema_version_mismatch", "goose_db_version", err)
	}
	return loadDictionaries(ctx, target)
}

func loadDictionaries(ctx context.Context, target *sql.DB) (dictionaries, error) {
	result := dictionaries{
		Canteens: map[string]int64{}, Cuisines: map[string]dictionaryItem{}, Flavors: map[string]dictionaryItem{},
	}
	if err := loadTargetCanteens(ctx, target, &result); err != nil {
		return result, err
	}
	if err := loadTargetCuisines(ctx, target, &result); err != nil {
		return result, err
	}
	if err := loadTargetFlavors(ctx, target, &result); err != nil {
		return result, err
	}
	return result, nil
}

func loadTargetCanteens(ctx context.Context, target *sql.DB, result *dictionaries) error {
	rows, err := target.QueryContext(ctx, `SELECT id, name FROM canteens ORDER BY id`)
	if err != nil {
		return failure("target_read_failed", "canteens", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var id int64
		var name string
		if err = rows.Scan(&id, &name); err != nil {
			return failure("target_scan_failed", "canteens", err)
		}
		result.Canteens[name] = id
	}
	if err = rows.Err(); err != nil {
		return failure("target_rows_failed", "canteens", err)
	}
	return nil
}

func loadTargetCuisines(ctx context.Context, target *sql.DB, result *dictionaries) error {
	rows, err := target.QueryContext(ctx, `SELECT id, name, is_active, sort_order FROM cuisines ORDER BY id`)
	if err != nil {
		return failure("target_read_failed", "cuisines", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var name string
		var value dictionaryItem
		if err = rows.Scan(&value.ID, &name, &value.IsActive, &value.SortOrder); err != nil {
			return failure("target_scan_failed", "cuisines", err)
		}
		result.Cuisines[name] = value
	}
	if err = rows.Err(); err != nil {
		return failure("target_rows_failed", "cuisines", err)
	}
	return nil
}

func loadTargetFlavors(ctx context.Context, target *sql.DB, result *dictionaries) error {
	rows, err := target.QueryContext(ctx, `SELECT id, name, is_active, sort_order FROM flavors ORDER BY id`)
	if err != nil {
		return failure("target_read_failed", "flavors", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var name string
		var value dictionaryItem
		if err = rows.Scan(&value.ID, &name, &value.IsActive, &value.SortOrder); err != nil {
			return failure("target_scan_failed", "flavors", err)
		}
		result.Flavors[name] = value
	}
	if err = rows.Err(); err != nil {
		return failure("target_rows_failed", "flavors", err)
	}
	return nil
}

func targetHasBusinessRows(ctx context.Context, target *sql.DB) (bool, error) {
	tables := []string{
		"users", "user_roles", "user_role_records", "user_ban_records", "image_assets",
		"posts", "tags", "post_tags", "post_flavors", "post_images", "comments", "comment_mentions",
		"follows", "favorites", "post_likes", "comment_likes", "notifications", "post_histories",
		"comment_histories", "moderation_records", "email_verification_codes", "user_sessions", "dictionary_suggestions",
	}
	for _, table := range tables {
		var count int
		query := fmt.Sprintf("SELECT count(*) FROM %s", table) // table comes only from the fixed allowlist above.
		if err := target.QueryRowContext(ctx, query).Scan(&count); err != nil {
			return false, failure("target_read_failed", table, err)
		}
		if count != 0 {
			return true, nil
		}
	}
	return false, nil
}

func writeImportReport(output io.Writer, data dataset) {
	tables := make([]string, 0, len(data.Stats))
	for table := range data.Stats {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		stat := data.Stats[table]
		_, _ = fmt.Fprintf(output, "IMPORT table=%s source_rows=%d target_rows=%d omitted_rows=%d\n",
			table, stat.SourceRows, stat.TargetRows, stat.OmittedRows)
	}
	_, _ = fmt.Fprintf(output, "IMPORT_OK users=%d posts=%d comments=%d images=%d notifications=%d\n",
		len(data.Users), len(data.Posts), len(data.Comments), len(data.Images), len(data.Notifications))
}
