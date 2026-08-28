package legacyimporter

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

var explicitlyImportedIdentityTables = []string{
	"users",
	"user_role_records",
	"user_ban_records",
	"image_assets",
	"cuisines",
	"flavors",
	"posts",
	"tags",
	"comments",
	"notifications",
}

func writeDataset(ctx context.Context, target *sql.DB, data dataset) (writeErr error) {
	tx, err := target.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return failure("target_transaction_begin_failed", "", err)
	}
	defer func() {
		if writeErr != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(682347120240824)`); err != nil {
		return failure("target_lock_failed", "", err)
	}
	if _, err = tx.ExecContext(ctx, `SET LOCAL statement_timeout='5min'`); err != nil {
		return failure("target_setup_failed", "", err)
	}
	if _, err = tx.ExecContext(ctx, `SET LOCAL danshi.allow_counter_write='on'`); err != nil {
		return failure("target_setup_failed", "", err)
	}
	writers := []func(context.Context, *sql.Tx, dataset) error{
		writeUsers, writeRolesAndAudit, writeImages, writeUserAvatars, writeHistoricalDictionaries, writePosts, writeTags,
		writePostRelations, writeComments, writeInitialContentVersions, writeActions, writeNotifications,
		advanceIdentitySequences, recountAll,
	}
	for _, writer := range writers {
		if err = writer(ctx, tx, data); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return failure("target_commit_failed", "", err)
	}
	return nil
}

func writeUsers(ctx context.Context, tx *sql.Tx, data dataset) error {
	rows := sortedUsers(data.Users)
	for _, row := range rows {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO users
			  (id,email,password_hash,name,gender,bio,avatar_image_asset_id,ban_is_permanent,
			   banned_until,ban_reason,banned_by,deleted_at,created_at,updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,NULL,$7,$8,$9,$10,$11,$12,$13)
			ON CONFLICT (id) DO UPDATE SET
			  email=EXCLUDED.email,password_hash=EXCLUDED.password_hash,name=EXCLUDED.name,
			  gender=EXCLUDED.gender,bio=EXCLUDED.bio,ban_is_permanent=EXCLUDED.ban_is_permanent,
			  banned_until=EXCLUDED.banned_until,ban_reason=EXCLUDED.ban_reason,banned_by=EXCLUDED.banned_by,
			  deleted_at=EXCLUDED.deleted_at,created_at=EXCLUDED.created_at,updated_at=EXCLUDED.updated_at`,
			row.ID, row.Email, row.PasswordHash, row.Name, row.Gender, row.Bio, row.BanIsPermanent,
			row.BannedUntil, row.BanReason, row.BannedBy, row.DeletedAt, row.CreatedAt, row.UpdatedAt)
		if err != nil {
			return failure("target_write_failed", "users", err)
		}
	}
	return nil
}

func writeRolesAndAudit(ctx context.Context, tx *sql.Tx, data dataset) error {
	roles := mapValues(data.Roles)
	sort.Slice(roles, func(i, j int) bool { return roleKey(roles[i]) < roleKey(roles[j]) })
	for _, row := range roles {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO user_roles (user_id,role,granted_by,granted_at) VALUES ($1,$2,$3,$4)
			ON CONFLICT (user_id,role) DO UPDATE SET granted_by=EXCLUDED.granted_by,granted_at=EXCLUDED.granted_at`,
			row.UserID, row.Role, row.GrantedBy, row.GrantedAt)
		if err != nil {
			return failure("target_write_failed", "user_roles", err)
		}
	}
	roleRecords := mapValues(data.RoleRecords)
	sort.Slice(roleRecords, func(i, j int) bool { return roleRecords[i].ID < roleRecords[j].ID })
	for _, row := range roleRecords {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO user_role_records (id,user_id,role,action,actor_id,created_at)
			VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (id) DO NOTHING`,
			row.ID, row.UserID, row.Role, row.Action, row.ActorID, row.CreatedAt)
		if err != nil {
			return failure("target_write_failed", "user_role_records", err)
		}
	}
	banRecords := mapValues(data.BanRecords)
	sort.Slice(banRecords, func(i, j int) bool { return banRecords[i].ID < banRecords[j].ID })
	for _, row := range banRecords {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO user_ban_records
			  (id,user_id,action,ban_is_permanent,banned_until,reason,actor_id,created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (id) DO NOTHING`,
			row.ID, row.UserID, row.Action, row.BanPermanent, row.BannedUntil, row.Reason, row.ActorID, row.CreatedAt)
		if err != nil {
			return failure("target_write_failed", "user_ban_records", err)
		}
	}
	return nil
}

func writeImages(ctx context.Context, tx *sql.Tx, data dataset) error {
	rows := mapValues(data.Images)
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	for _, row := range rows {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO image_assets
			  (id,uploader_id,purpose,object_key,public_url,content_type,size,status,moderation,created_at,updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			ON CONFLICT (id) DO UPDATE SET uploader_id=EXCLUDED.uploader_id,purpose=EXCLUDED.purpose,
			  object_key=EXCLUDED.object_key,public_url=EXCLUDED.public_url,content_type=EXCLUDED.content_type,
			  size=EXCLUDED.size,status=EXCLUDED.status,moderation=EXCLUDED.moderation,
			  created_at=EXCLUDED.created_at,updated_at=EXCLUDED.updated_at`,
			row.ID, row.UploaderID, row.Purpose, row.ObjectKey, row.PublicURL, row.ContentType,
			row.Size, row.Status, row.Moderation, row.CreatedAt, row.UpdatedAt)
		if err != nil {
			return failure("target_write_failed", "image_assets", err)
		}
	}
	return nil
}

func writeUserAvatars(ctx context.Context, tx *sql.Tx, data dataset) error {
	for _, row := range sortedUsers(data.Users) {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET avatar_image_asset_id=$2 WHERE id=$1`, row.ID, row.AvatarImageAssetID); err != nil {
			return failure("target_write_failed", "users", err)
		}
	}
	return nil
}

func writeHistoricalDictionaries(ctx context.Context, tx *sql.Tx, data dataset) error {
	for _, row := range sortedDictionaryRows(data.Cuisines) {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO cuisines (id,name,sort_order,is_active,created_at,updated_at)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name,sort_order=EXCLUDED.sort_order,
			  is_active=EXCLUDED.is_active,created_at=EXCLUDED.created_at,updated_at=EXCLUDED.updated_at`,
			row.ID, row.Name, row.SortOrder, row.IsActive, row.CreatedAt, row.UpdatedAt)
		if err != nil {
			return failure("target_write_failed", "cuisines", err)
		}
	}
	for _, row := range sortedDictionaryRows(data.Flavors) {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO flavors (id,name,sort_order,is_active,created_at,updated_at)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name,sort_order=EXCLUDED.sort_order,
			  is_active=EXCLUDED.is_active,created_at=EXCLUDED.created_at,updated_at=EXCLUDED.updated_at`,
			row.ID, row.Name, row.SortOrder, row.IsActive, row.CreatedAt, row.UpdatedAt)
		if err != nil {
			return failure("target_write_failed", "flavors", err)
		}
	}
	return nil
}

func writePosts(ctx context.Context, tx *sql.Tx, data dataset) error {
	rows := mapValues(data.Posts)
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	for _, row := range rows {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO posts
			  (id,author_id,post_type,share_type,status,category,title,content,canteen_id,cuisine_id,
			   price,budget_min,budget_max,like_count,favorite_count,comment_count,view_count,
			   deleted_at,deleted_reason,deleted_by,created_at,updated_at,current_revision)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,0,0,0,$14,$15,$16,$17,$18,$19,1)
			ON CONFLICT (id) DO UPDATE SET author_id=EXCLUDED.author_id,post_type=EXCLUDED.post_type,
			  share_type=EXCLUDED.share_type,status=EXCLUDED.status,category=EXCLUDED.category,
			  title=EXCLUDED.title,content=EXCLUDED.content,canteen_id=EXCLUDED.canteen_id,
			  cuisine_id=EXCLUDED.cuisine_id,price=EXCLUDED.price,budget_min=EXCLUDED.budget_min,
			  budget_max=EXCLUDED.budget_max,view_count=EXCLUDED.view_count,deleted_at=EXCLUDED.deleted_at,
			  deleted_reason=EXCLUDED.deleted_reason,deleted_by=EXCLUDED.deleted_by,
			  created_at=EXCLUDED.created_at,updated_at=EXCLUDED.updated_at,current_revision=1`,
			row.ID, row.AuthorID, row.PostType, row.ShareType, row.Status, row.Category,
			row.Title, row.Content, row.CanteenID, row.CuisineID, row.Price, row.BudgetMin,
			row.BudgetMax, row.ViewCount, row.DeletedAt, row.DeletedReason, row.DeletedBy,
			row.CreatedAt, row.UpdatedAt)
		if err != nil {
			return failure("target_write_failed", "posts", err)
		}
	}
	return nil
}

func writeTags(ctx context.Context, tx *sql.Tx, data dataset) error {
	rows := mapValues(data.Tags)
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	for _, row := range rows {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO tags (id,name,moderation,deleted_at,created_at,updated_at) VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name,moderation=EXCLUDED.moderation,
			  deleted_at=EXCLUDED.deleted_at,created_at=EXCLUDED.created_at,updated_at=EXCLUDED.updated_at`,
			row.ID, row.Name, row.Moderation, row.DeletedAt, row.CreatedAt, row.UpdatedAt)
		if err != nil {
			return failure("target_write_failed", "tags", err)
		}
	}
	return nil
}

func writePostRelations(ctx context.Context, tx *sql.Tx, data dataset) error {
	for _, row := range sortedPostTags(data.PostTags) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO post_tags (post_id,tag_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, row.PostID, row.TagID); err != nil {
			return failure("target_write_failed", "post_tags", err)
		}
	}
	for _, row := range sortedPostFlavors(data.PostFlavors) {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO post_flavors (post_id,flavor_id,stance,post_type) VALUES ($1,$2,$3,$4)
			ON CONFLICT (post_id,flavor_id) DO UPDATE SET stance=EXCLUDED.stance,post_type=EXCLUDED.post_type`,
			row.PostID, row.FlavorID, row.Stance, row.PostType)
		if err != nil {
			return failure("target_write_failed", "post_flavors", err)
		}
	}
	for _, row := range sortedPostImages(data.PostImages) {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO post_images (post_id,position,image_asset_id,created_at) VALUES ($1,$2,$3,$4)
			ON CONFLICT (post_id,position) DO UPDATE SET image_asset_id=EXCLUDED.image_asset_id,created_at=EXCLUDED.created_at`,
			row.PostID, row.Position, row.ImageAssetID, row.CreatedAt)
		if err != nil {
			return failure("target_write_failed", "post_images", err)
		}
	}
	return nil
}

func writeComments(ctx context.Context, tx *sql.Tx, data dataset) error {
	rows := mapValues(data.Comments)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].CreatedAt.Before(rows[j].CreatedAt)
	})
	for _, row := range rows {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO comments
			  (id,post_id,author_id,parent_id,root_id,reply_to_user_id,content,moderation,
			   like_count,reply_count,deleted_at,deleted_reason,deleted_by,created_at,updated_at,current_revision)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,0,0,$9,$10,$11,$12,$13,1)
			ON CONFLICT (id) DO UPDATE SET post_id=EXCLUDED.post_id,author_id=EXCLUDED.author_id,
			  parent_id=EXCLUDED.parent_id,root_id=EXCLUDED.root_id,reply_to_user_id=EXCLUDED.reply_to_user_id,
			  content=EXCLUDED.content,moderation=EXCLUDED.moderation,deleted_at=EXCLUDED.deleted_at,
			  deleted_reason=EXCLUDED.deleted_reason,deleted_by=EXCLUDED.deleted_by,
			  created_at=EXCLUDED.created_at,updated_at=EXCLUDED.updated_at,current_revision=1`,
			row.ID, row.PostID, row.AuthorID, row.ParentID, row.RootID, row.ReplyToUserID,
			row.Content, row.Moderation, row.DeletedAt, row.DeletedReason, row.DeletedBy,
			row.CreatedAt, row.UpdatedAt)
		if err != nil {
			return failure("target_write_failed", "comments", err)
		}
	}
	return nil
}

func writeInitialContentVersions(ctx context.Context, tx *sql.Tx, _ dataset) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO post_histories (post_id,revision,edited_by,edited_at,snapshot)
		SELECT p.id,1,p.author_id,p.updated_at,jsonb_build_object(
			'post_type',p.post_type,'share_type',p.share_type,'title',p.title,'content',p.content,
			'category',p.category,'canteen_id',p.canteen_id,'canteen_window_id',p.canteen_window_id,
			'cuisine_id',p.cuisine_id,
			'price',CASE WHEN p.price IS NULL THEN 'null'::jsonb ELSE to_jsonb(p.price::text) END,
			'budget_min',p.budget_min,'budget_max',p.budget_max,
			'tags',COALESCE((SELECT jsonb_agg(t.name ORDER BY lower(t.name),t.name)
				FROM post_tags pt JOIN tags t ON t.id=pt.tag_id WHERE pt.post_id=p.id),'[]'::jsonb),
			'flavors',COALESCE((SELECT jsonb_agg(jsonb_build_object('name',f.name,'stance',pf.stance)
				ORDER BY f.sort_order,f.id) FROM post_flavors pf JOIN flavors f ON f.id=pf.flavor_id
				WHERE pf.post_id=p.id),'[]'::jsonb),
			'images',COALESCE((SELECT jsonb_agg(a.public_url ORDER BY pi.position)
				FROM post_images pi JOIN image_assets a ON a.id=pi.image_asset_id
				WHERE pi.post_id=p.id),'[]'::jsonb)
		)
		FROM posts p
		ON CONFLICT (post_id,revision) DO NOTHING;

		INSERT INTO comment_histories (comment_id,revision,edited_by,edited_at,content)
		SELECT c.id,1,c.author_id,c.updated_at,c.content FROM comments c
		ON CONFLICT (comment_id,revision) DO NOTHING;
	`)
	if err != nil {
		return failure("target_write_failed", "content_histories", err)
	}
	return nil
}

func writeActions(ctx context.Context, tx *sql.Tx, data dataset) error {
	for _, row := range sortedFollows(data.Follows) {
		_, err := tx.ExecContext(ctx, `INSERT INTO follows (follower_id,following_id,created_at) VALUES ($1,$2,$3)
			ON CONFLICT (follower_id,following_id) DO UPDATE SET created_at=EXCLUDED.created_at`, row.FollowerID, row.FollowingID, row.CreatedAt)
		if err != nil {
			return failure("target_write_failed", "follows", err)
		}
	}
	for _, row := range sortedFavorites(data.Favorites) {
		_, err := tx.ExecContext(ctx, `INSERT INTO favorites (user_id,post_id,created_at) VALUES ($1,$2,$3)
			ON CONFLICT (user_id,post_id) DO UPDATE SET created_at=EXCLUDED.created_at`, row.UserID, row.PostID, row.CreatedAt)
		if err != nil {
			return failure("target_write_failed", "favorites", err)
		}
	}
	for _, row := range sortedPostLikes(data.PostLikes) {
		_, err := tx.ExecContext(ctx, `INSERT INTO post_likes (user_id,post_id,created_at) VALUES ($1,$2,$3)
			ON CONFLICT (user_id,post_id) DO UPDATE SET created_at=EXCLUDED.created_at`, row.UserID, row.PostID, row.CreatedAt)
		if err != nil {
			return failure("target_write_failed", "post_likes", err)
		}
	}
	for _, row := range sortedCommentLikes(data.CommentLikes) {
		_, err := tx.ExecContext(ctx, `INSERT INTO comment_likes (user_id,comment_id,created_at) VALUES ($1,$2,$3)
			ON CONFLICT (user_id,comment_id) DO UPDATE SET created_at=EXCLUDED.created_at`, row.UserID, row.CommentID, row.CreatedAt)
		if err != nil {
			return failure("target_write_failed", "comment_likes", err)
		}
	}
	return nil
}

func writeNotifications(ctx context.Context, tx *sql.Tx, data dataset) error {
	rows := mapValues(data.Notifications)
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	for _, row := range rows {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO notifications
			  (id,recipient_id,sender_id,type,related_post_id,related_comment_id,content,is_read,created_at,updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT (id) DO UPDATE SET recipient_id=EXCLUDED.recipient_id,sender_id=EXCLUDED.sender_id,
			  type=EXCLUDED.type,related_post_id=EXCLUDED.related_post_id,
			  related_comment_id=EXCLUDED.related_comment_id,content=EXCLUDED.content,
			  is_read=EXCLUDED.is_read,created_at=EXCLUDED.created_at,updated_at=EXCLUDED.updated_at`,
			row.ID, row.RecipientID, row.SenderID, row.Type, row.RelatedPostID,
			row.RelatedCommentID, row.Content, row.IsRead, row.CreatedAt, row.UpdatedAt)
		if err != nil {
			return failure("target_write_failed", "notifications", err)
		}
	}
	return nil
}

func advanceIdentitySequences(ctx context.Context, tx *sql.Tx, _ dataset) error {
	for _, table := range explicitlyImportedIdentityTables {
		query := fmt.Sprintf(`
			SELECT setval(
				pg_get_serial_sequence($1, 'id'),
				COALESCE(MAX(id), 1),
				MAX(id) IS NOT NULL
			)
			FROM public.%s`, table) // table comes only from the fixed allowlist above.
		var sequenceValue sql.NullInt64
		if err := tx.QueryRowContext(ctx, query, "public."+table).Scan(&sequenceValue); err != nil {
			return failure("target_sequence_advance_failed", table, err)
		}
		if !sequenceValue.Valid {
			return failure("target_identity_sequence_missing", table, nil)
		}
	}
	return nil
}

func recountAll(ctx context.Context, tx *sql.Tx, _ dataset) error {
	rows, err := tx.QueryContext(ctx, `SELECT table_name,fixed_rows FROM danshi_recount_all() ORDER BY table_name`)
	if err != nil {
		return failure("counter_recount_failed", "", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var table string
		var count int64
		if err = rows.Scan(&table, &count); err != nil {
			return failure("counter_recount_scan_failed", "", err)
		}
	}
	if err = rows.Err(); err != nil {
		return failure("counter_recount_rows_failed", "", err)
	}
	return nil
}

func mapValues[K comparable, V any](source map[K]V) []V {
	result := make([]V, 0, len(source))
	for _, value := range source {
		result = append(result, value)
	}
	return result
}

func sortedUsers(source map[int64]userRow) []userRow {
	rows := mapValues(source)
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

func sortedDictionaryRows(source map[int64]dictionaryRow) []dictionaryRow {
	rows := mapValues(source)
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

func roleKey(row roleRow) string { return int64String(row.UserID) + ":" + row.Role }

func sortedPostTags(source map[string]postTagRow) []postTagRow {
	rows := mapValues(source)
	sort.Slice(rows, func(i, j int) bool {
		return relationKey(int64String(rows[i].PostID), int64String(rows[i].TagID)) < relationKey(int64String(rows[j].PostID), int64String(rows[j].TagID))
	})
	return rows
}

func sortedPostFlavors(source map[string]postFlavorRow) []postFlavorRow {
	rows := mapValues(source)
	sort.Slice(rows, func(i, j int) bool {
		return relationKey(int64String(rows[i].PostID), int64String(rows[i].FlavorID)) < relationKey(int64String(rows[j].PostID), int64String(rows[j].FlavorID))
	})
	return rows
}

func sortedPostImages(source map[string]postImageRow) []postImageRow {
	rows := mapValues(source)
	sort.Slice(rows, func(i, j int) bool {
		return relationKey(int64String(rows[i].PostID), intString(int(rows[i].Position))) < relationKey(int64String(rows[j].PostID), intString(int(rows[j].Position)))
	})
	return rows
}

func sortedFollows(source map[string]followRow) []followRow {
	rows := mapValues(source)
	sort.Slice(rows, func(i, j int) bool {
		return relationKey(int64String(rows[i].FollowerID), int64String(rows[i].FollowingID)) < relationKey(int64String(rows[j].FollowerID), int64String(rows[j].FollowingID))
	})
	return rows
}

func sortedFavorites(source map[string]favoriteRow) []favoriteRow {
	rows := mapValues(source)
	sort.Slice(rows, func(i, j int) bool {
		return relationKey(int64String(rows[i].UserID), int64String(rows[i].PostID)) < relationKey(int64String(rows[j].UserID), int64String(rows[j].PostID))
	})
	return rows
}

func sortedPostLikes(source map[string]postLikeRow) []postLikeRow {
	rows := mapValues(source)
	sort.Slice(rows, func(i, j int) bool {
		return relationKey(int64String(rows[i].UserID), int64String(rows[i].PostID)) < relationKey(int64String(rows[j].UserID), int64String(rows[j].PostID))
	})
	return rows
}

func sortedCommentLikes(source map[string]commentLikeRow) []commentLikeRow {
	rows := mapValues(source)
	sort.Slice(rows, func(i, j int) bool {
		return relationKey(int64String(rows[i].UserID), int64String(rows[i].CommentID)) < relationKey(int64String(rows[j].UserID), int64String(rows[j].CommentID))
	})
	return rows
}
