package legacymigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"strings"
)

const sourceDatasetContentVersion = 1

// sourceDatasetSnapshotDigest 对所有会进入迁移决策的数据列建立确定性摘要。
// 短期验证码与 session 不迁移，因此只绑定表是否存在和行数，不读取其凭据值。
func sourceDatasetSnapshotDigest(ctx context.Context, tx *sql.Tx) (string, error) {
	digest := sha256.New()
	_, _ = digest.Write([]byte("danshi-legacy-source-dataset-v1\n"))
	for _, table := range sortedKeys(legacyColumns) {
		if table == "email_verification_codes" {
			continue
		}
		if err := appendSourceDatasetTable(ctx, tx, digest, table, legacyColumns[table]); err != nil {
			return "", err
		}
	}
	if err := appendEphemeralTableCount(ctx, tx, digest, "email_verification_codes", true); err != nil {
		return "", err
	}
	if err := appendEphemeralTableCount(ctx, tx, digest, "user_sessions", false); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func appendSourceDatasetTable(
	ctx context.Context,
	tx *sql.Tx,
	digest hash.Hash,
	table string,
	columns []string,
) (resultErr error) {
	query := "SELECT pg_catalog.jsonb_build_array(" + strings.Join(columns, ",") +
		")::text FROM public." + table + " ORDER BY id"
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return gateError("source_dataset_digest_failed", "无法读取来源迁移数据集")
	}
	defer func() {
		if err := rows.Close(); err != nil && resultErr == nil {
			resultErr = gateError("source_dataset_digest_failed", "无法结束来源迁移数据集读取")
		}
	}()
	writeDigestField(digest, []byte("table"), []byte(table))
	var count uint64
	for rows.Next() {
		var canonicalRow string
		if err := rows.Scan(&canonicalRow); err != nil {
			return gateError("source_dataset_digest_failed", "无法规范化来源迁移数据集")
		}
		writeDigestField(digest, []byte("row"), []byte(canonicalRow))
		count++
	}
	if err := rows.Err(); err != nil {
		return gateError("source_dataset_digest_failed", "读取来源迁移数据集时发生错误")
	}
	var encodedCount [8]byte
	binary.BigEndian.PutUint64(encodedCount[:], count)
	writeDigestField(digest, []byte("count"), encodedCount[:])
	return nil
}

func appendEphemeralTableCount(
	ctx context.Context,
	tx *sql.Tx,
	digest hash.Hash,
	table string,
	required bool,
) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT pg_catalog.to_regclass($1) IS NOT NULL", "public."+table).Scan(&exists); err != nil {
		return gateError("source_dataset_digest_failed", "无法核验来源短期凭据表")
	}
	if required && !exists {
		return gateError("source_schema_mismatch", "来源库不符合已归档 FastAPI schema")
	}
	var count int64
	if exists {
		query := "SELECT pg_catalog.count(*) FROM public." + table
		if err := tx.QueryRowContext(ctx, query).Scan(&count); err != nil {
			return gateError("source_dataset_digest_failed", "无法统计来源短期凭据")
		}
	}
	writeDigestField(digest, []byte("ephemeral_table"), []byte(table))
	if exists {
		writeDigestField(digest, []byte("exists"), []byte{1})
	} else {
		writeDigestField(digest, []byte("exists"), []byte{0})
	}
	var encodedCount [8]byte
	binary.BigEndian.PutUint64(encodedCount[:], uint64(count))
	writeDigestField(digest, []byte("count"), encodedCount[:])
	return nil
}

func writeDigestField(digest hash.Hash, label, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(label)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(label)
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(value)
}

func databaseIdentityDigest(identity databaseIdentity) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("danshi-database-identity-v1\n"))
	writeDigestField(digest, []byte("system_identifier"), []byte(identity.systemIdentifier))
	writeDigestField(digest, []byte("database_oid"), []byte(identity.databaseOID))
	return hex.EncodeToString(digest.Sum(nil))
}
