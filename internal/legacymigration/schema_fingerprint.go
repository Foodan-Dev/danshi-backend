package legacymigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"hash"
)

// expectedTargetSchemaFingerprint 由 PostgreSQL 18 隔离库执行仓库 goose v1..v11 后生成。
// TestTargetV11SchemaFingerprintMatchesMigrations 会持续证明该 golden 来自正式 migrations。
const expectedTargetSchemaFingerprint = "8bfcf431f41fb2dc66a7a2613e6770bac6c31167350de548e45fa3c11277fbe6"

type schemaCatalogSection struct {
	name  string
	query string
}

var targetSchemaCatalogSections = []schemaCatalogSection{
	{name: "tables", query: `SELECT pg_catalog.jsonb_build_array(
  relation.relname, relation.relkind, relation.relpersistence,
  relation.relrowsecurity, relation.relforcerowsecurity
)::text
FROM pg_catalog.pg_class AS relation
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = relation.relnamespace
WHERE namespace.nspname = 'public'
  AND relation.relkind IN ('r', 'p')
  AND relation.relname <> 'goose_db_version'
ORDER BY relation.relname`},
	{name: "columns", query: `SELECT pg_catalog.jsonb_build_array(
  c.relname, a.attnum, a.attname,
  pg_catalog.format_type(a.atttypid, a.atttypmod),
  a.attnotnull, a.attidentity, a.attgenerated,
  a.attstorage, a.attcompression,
  COALESCE(pg_catalog.pg_get_expr(ad.adbin, ad.adrelid, false), ''),
  COALESCE(cn.nspname || '.' || co.collname, '')
)::text
FROM pg_catalog.pg_attribute AS a
JOIN pg_catalog.pg_class AS c ON c.oid = a.attrelid
JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
LEFT JOIN pg_catalog.pg_attrdef AS ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
LEFT JOIN pg_catalog.pg_collation AS co ON co.oid = a.attcollation
LEFT JOIN pg_catalog.pg_namespace AS cn ON cn.oid = co.collnamespace
WHERE n.nspname = 'public'
  AND c.relkind IN ('r', 'p')
  AND c.relname <> 'goose_db_version'
  AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY c.relname, a.attnum`},
	{name: "constraints", query: `SELECT pg_catalog.jsonb_build_array(
  relation.relname, constraint_value.conname, constraint_value.contype,
  constraint_value.condeferrable, constraint_value.condeferred,
  constraint_value.convalidated,
  pg_catalog.pg_get_constraintdef(constraint_value.oid, false)
)::text
FROM pg_catalog.pg_constraint AS constraint_value
JOIN pg_catalog.pg_class AS relation ON relation.oid = constraint_value.conrelid
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = relation.relnamespace
WHERE namespace.nspname = 'public' AND relation.relname <> 'goose_db_version'
ORDER BY relation.relname, constraint_value.conname`},
	{name: "indexes", query: `SELECT pg_catalog.jsonb_build_array(
  relation.relname, index_relation.relname,
  index_value.indisunique, index_value.indisprimary, index_value.indisexclusion,
  index_value.indimmediate, index_value.indisclustered, index_value.indisvalid,
  index_value.indisready, index_value.indislive, index_value.indisreplident,
  pg_catalog.pg_get_indexdef(index_relation.oid, 0, false)
)::text
FROM pg_catalog.pg_index AS index_value
JOIN pg_catalog.pg_class AS relation ON relation.oid = index_value.indrelid
JOIN pg_catalog.pg_class AS index_relation ON index_relation.oid = index_value.indexrelid
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = relation.relnamespace
WHERE namespace.nspname = 'public' AND relation.relname <> 'goose_db_version'
ORDER BY relation.relname, index_relation.relname`},
	{name: "triggers", query: `SELECT pg_catalog.jsonb_build_array(
  relation.relname, trigger_value.tgname, trigger_value.tgenabled,
  function_namespace.nspname, function_value.proname,
  pg_catalog.pg_get_function_identity_arguments(function_value.oid),
  pg_catalog.pg_get_triggerdef(trigger_value.oid, false)
)::text
FROM pg_catalog.pg_trigger AS trigger_value
JOIN pg_catalog.pg_class AS relation ON relation.oid = trigger_value.tgrelid
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = relation.relnamespace
JOIN pg_catalog.pg_proc AS function_value ON function_value.oid = trigger_value.tgfoid
JOIN pg_catalog.pg_namespace AS function_namespace ON function_namespace.oid = function_value.pronamespace
WHERE namespace.nspname = 'public' AND NOT trigger_value.tgisinternal
ORDER BY relation.relname, trigger_value.tgname`},
	{name: "functions", query: `SELECT pg_catalog.jsonb_build_array(
  function_value.proname,
  pg_catalog.pg_get_function_identity_arguments(function_value.oid),
  pg_catalog.pg_get_function_result(function_value.oid),
  function_value.prokind, function_value.provolatile, function_value.proparallel,
  function_value.prosecdef, function_value.proleakproof, function_value.proisstrict,
  function_value.proretset,
  pg_catalog.pg_get_functiondef(function_value.oid)
)::text
FROM pg_catalog.pg_proc AS function_value
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = function_value.pronamespace
WHERE namespace.nspname = 'public'
  AND NOT EXISTS (
    SELECT 1 FROM pg_catalog.pg_depend AS dependency
    WHERE dependency.classid = 'pg_catalog.pg_proc'::pg_catalog.regclass
      AND dependency.objid = function_value.oid AND dependency.deptype = 'e'
  )
ORDER BY function_value.proname, pg_catalog.pg_get_function_identity_arguments(function_value.oid)`},
	{name: "sequences", query: `SELECT pg_catalog.jsonb_build_array(
  relation.relname, pg_catalog.format_type(sequence_value.seqtypid, -1),
  sequence_value.seqstart, sequence_value.seqincrement, sequence_value.seqmax,
  sequence_value.seqmin, sequence_value.seqcache, sequence_value.seqcycle
)::text
FROM pg_catalog.pg_sequence AS sequence_value
JOIN pg_catalog.pg_class AS relation ON relation.oid = sequence_value.seqrelid
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = relation.relnamespace
WHERE namespace.nspname = 'public'
ORDER BY relation.relname`},
	{name: "policies", query: `SELECT pg_catalog.jsonb_build_array(
  relation.relname, policy.polname, policy.polcmd, policy.polpermissive,
  pg_catalog.pg_get_expr(policy.polqual, policy.polrelid, false),
  pg_catalog.pg_get_expr(policy.polwithcheck, policy.polrelid, false)
)::text
FROM pg_catalog.pg_policy AS policy
JOIN pg_catalog.pg_class AS relation ON relation.oid = policy.polrelid
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = relation.relnamespace
WHERE namespace.nspname = 'public'
ORDER BY relation.relname, policy.polname`},
	{name: "views", query: `SELECT pg_catalog.jsonb_build_array(
  relation.relname, relation.relkind, pg_catalog.pg_get_viewdef(relation.oid, false)
)::text
FROM pg_catalog.pg_class AS relation
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = relation.relnamespace
WHERE namespace.nspname = 'public' AND relation.relkind IN ('v', 'm')
ORDER BY relation.relname`},
	{name: "types", query: `SELECT pg_catalog.jsonb_build_array(
  type_value.typname, type_value.typtype, type_value.typcategory,
  pg_catalog.format_type(type_value.oid, NULL)
)::text
FROM pg_catalog.pg_type AS type_value
JOIN pg_catalog.pg_namespace AS namespace ON namespace.oid = type_value.typnamespace
WHERE namespace.nspname = 'public' AND type_value.typrelid = 0
  AND NOT EXISTS (
    SELECT 1 FROM pg_catalog.pg_depend AS dependency
    WHERE dependency.classid = 'pg_catalog.pg_type'::pg_catalog.regclass
      AND dependency.objid = type_value.oid AND dependency.deptype = 'e'
  )
ORDER BY type_value.typname`},
	{name: "extensions", query: `SELECT pg_catalog.jsonb_build_array(
  extension.extname, extension.extversion
)::text
FROM pg_catalog.pg_extension AS extension
WHERE extension.extname = 'pg_trgm'
ORDER BY extension.extname`},
}

func targetSchemaFingerprint(ctx context.Context, tx *sql.Tx) (string, error) {
	digest := sha256.New()
	for _, section := range targetSchemaCatalogSections {
		if err := appendSchemaCatalogSection(ctx, tx, digest, section); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func appendSchemaCatalogSection(
	ctx context.Context,
	tx *sql.Tx,
	digest hash.Hash,
	section schemaCatalogSection,
) (resultErr error) {
	rows, err := tx.QueryContext(ctx, section.query)
	if err != nil {
		return gateError("target_schema_fingerprint_failed", "无法读取目标库 canonical schema")
	}
	defer func() {
		if err := rows.Close(); err != nil && resultErr == nil {
			resultErr = gateError("target_schema_fingerprint_failed", "无法结束目标库 canonical schema 读取")
		}
	}()
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return gateError("target_schema_fingerprint_failed", "无法规范化目标库 canonical schema")
		}
		_, _ = digest.Write([]byte(section.name + "\t" + line + "\n"))
	}
	if err := rows.Err(); err != nil {
		return gateError("target_schema_fingerprint_failed", "读取目标库 canonical schema 时发生错误")
	}
	return nil
}
