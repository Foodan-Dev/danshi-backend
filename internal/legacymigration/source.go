package legacymigration

import (
	"context"
	"database/sql"
	"sort"
)

var legacyColumns = map[string][]string{
	"users":                    {"id", "email", "password", "name", "gender", "avatar_url", "hometown", "bio", "role", "is_active", "created_at", "updated_at"},
	"image_assets":             {"id", "uploader_id", "purpose", "object_key", "public_url", "content_type", "size", "status", "created_at", "updated_at"},
	"posts":                    {"id", "post_type", "title", "content", "category", "canteen", "tags", "share_type", "cuisine", "flavors", "price", "images", "like_count", "favorite_count", "budget_range", "preferences", "author_id", "status", "comment_count", "view_count", "created_at", "updated_at"},
	"comments":                 {"id", "content", "post_id", "author_id", "parent_id", "reply_to_user_id", "mentioned_user_ids", "like_count", "reply_count", "created_at", "updated_at"},
	"follows":                  {"id", "follower_id", "following_id", "created_at"},
	"favorites":                {"id", "user_id", "post_id", "created_at"},
	"likes":                    {"id", "user_id", "likeable_type", "likeable_id", "created_at"},
	"notifications":            {"id", "recipient_id", "sender_id", "type", "related_id", "related_type", "content", "is_read", "created_at", "updated_at"},
	"email_verification_codes": {"id", "email", "purpose", "code_digest", "expires_at", "created_at", "updated_at"},
}

func inspectSource(ctx context.Context, tx *sql.Tx, transaction TransactionInspection) (SourceInspection, error) {
	major, err := postgresMajor(ctx, tx, "source")
	if err != nil {
		return SourceInspection{}, err
	}
	if major < MinimumSourceMajor {
		return SourceInspection{}, gateError("source_postgres_version_unsupported", "来源库 PostgreSQL 主版本不得低于 16")
	}
	if err := validateLegacySchema(ctx, tx); err != nil {
		return SourceInspection{}, err
	}
	rows, err := legacyTableRows(ctx, tx)
	if err != nil {
		return SourceInspection{}, err
	}
	metrics, blockers, err := legacyMetrics(ctx, tx)
	if err != nil {
		return SourceInspection{}, err
	}
	return SourceInspection{
		PostgresMajor: major,
		Transaction:   transaction,
		TableRows:     rows,
		Metrics:       metrics,
		Blockers:      blockers,
	}, nil
}

func validateLegacySchema(ctx context.Context, tx *sql.Tx) error {
	tables := sortedKeys(legacyColumns)
	for _, table := range tables {
		for _, column := range legacyColumns[table] {
			var found bool
			err := tx.QueryRowContext(ctx, `SELECT EXISTS (
SELECT 1 FROM information_schema.columns
WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2
)`, table, column).Scan(&found)
			if err != nil {
				return gateError("source_schema_inspection_failed", "无法核验来源库 schema")
			}
			if !found {
				return gateError("source_schema_mismatch", "来源库不符合已归档 FastAPI schema")
			}
		}
	}
	return nil
}

func legacyTableRows(ctx context.Context, tx *sql.Tx) ([]AggregateCount, error) {
	queries := []struct {
		code string
		sql  string
	}{
		{"users", "SELECT pg_catalog.count(*) FROM public.users"},
		{"image_assets", "SELECT pg_catalog.count(*) FROM public.image_assets"},
		{"posts", "SELECT pg_catalog.count(*) FROM public.posts"},
		{"comments", "SELECT pg_catalog.count(*) FROM public.comments"},
		{"follows", "SELECT pg_catalog.count(*) FROM public.follows"},
		{"favorites", "SELECT pg_catalog.count(*) FROM public.favorites"},
		{"likes", "SELECT pg_catalog.count(*) FROM public.likes"},
		{"notifications", "SELECT pg_catalog.count(*) FROM public.notifications"},
		{"email_verification_codes", "SELECT pg_catalog.count(*) FROM public.email_verification_codes"},
	}
	return aggregateQueries(ctx, tx, queries, "source_count_failed", "无法聚合来源库行数")
}

func legacyMetrics(ctx context.Context, tx *sql.Tx) ([]AggregateCount, []AggregateCount, error) {
	queries := []struct {
		code string
		sql  string
	}{
		{"inactive_users", "SELECT pg_catalog.count(*) FROM public.users WHERE NOT is_active"},
		{"admin_users", "SELECT pg_catalog.count(*) FROM public.users WHERE role = 'admin'"},
		{"super_admin_users", "SELECT pg_catalog.count(*) FROM public.users WHERE role = 'super_admin'"},
		{"legacy_comment_moderation_rows", "SELECT pg_catalog.count(*) FROM public.comments"},
		{"legacy_image_moderation_rows", "SELECT pg_catalog.count(*) FROM public.image_assets"},
		{"posts_with_view_count", "SELECT pg_catalog.count(*) FROM public.posts WHERE view_count > 0"},
		{"view_count_total", "SELECT COALESCE(pg_catalog.sum(view_count), 0) FROM public.posts"},
		{"current_post_rows_not_histories", "SELECT pg_catalog.count(*) FROM public.posts"},
		{"current_comment_rows_not_histories", "SELECT pg_catalog.count(*) FROM public.comments"},
	}
	metrics, err := aggregateQueries(ctx, tx, queries, "source_metric_failed", "无法聚合来源库迁移指标")
	if err != nil {
		return nil, nil, err
	}
	blockerQueries := []struct {
		code string
		sql  string
	}{
		{"unknown_user_roles", "SELECT pg_catalog.count(*) FROM public.users WHERE role IS NULL OR role NOT IN ('user', 'admin', 'super_admin')"},
		{"invalid_post_types", "SELECT pg_catalog.count(*) FROM public.posts WHERE post_type IS NULL OR post_type NOT IN ('share', 'seeking')"},
		{"invalid_budget_shapes", "SELECT pg_catalog.count(*) FROM public.posts WHERE budget_range IS NOT NULL AND pg_catalog.jsonb_typeof(budget_range) NOT IN ('null', 'object')"},
		{"invalid_preference_shapes", "SELECT pg_catalog.count(*) FROM public.posts WHERE preferences IS NOT NULL AND pg_catalog.jsonb_typeof(preferences) NOT IN ('null', 'object')"},
		{"invalid_tag_shapes", "SELECT pg_catalog.count(*) FROM public.posts WHERE tags IS NOT NULL AND pg_catalog.jsonb_typeof(tags) IS DISTINCT FROM 'array'"},
		{"invalid_flavor_shapes", "SELECT pg_catalog.count(*) FROM public.posts WHERE flavors IS NOT NULL AND pg_catalog.jsonb_typeof(flavors) IS DISTINCT FROM 'array'"},
		{"invalid_image_shapes", "SELECT pg_catalog.count(*) FROM public.posts WHERE images IS NOT NULL AND pg_catalog.jsonb_typeof(images) IS DISTINCT FROM 'array'"},
		{"unknown_like_types", "SELECT pg_catalog.count(*) FROM public.likes WHERE likeable_type IS NULL OR likeable_type NOT IN ('post', 'comment')"},
		{"negative_view_counts", "SELECT pg_catalog.count(*) FROM public.posts WHERE view_count IS NULL OR view_count < 0"},
	}
	blockers, err := aggregateQueries(ctx, tx, blockerQueries, "source_blocker_failed", "无法聚合来源库阻断项")
	if err != nil {
		return nil, nil, err
	}
	return metrics, nonzeroCounts(blockers), nil
}

func sortedKeys(values map[string][]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func nonzeroCounts(values []AggregateCount) []AggregateCount {
	result := make([]AggregateCount, 0, len(values))
	for _, value := range values {
		if value.Count > 0 {
			result = append(result, value)
		}
	}
	return result
}
