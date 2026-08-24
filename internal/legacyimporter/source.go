package legacyimporter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

func loadSourceData(ctx context.Context, db *sql.DB) (_ sourceData, loadErr error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return sourceData{}, failure("source_readonly_begin_failed", "", err)
	}
	defer func() {
		if loadErr != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, "SET LOCAL statement_timeout = '2min'"); err != nil {
		return sourceData{}, failure("source_readonly_setup_failed", "", err)
	}
	var readOnly string
	if err = tx.QueryRowContext(ctx, "SHOW transaction_read_only").Scan(&readOnly); err != nil || readOnly != "on" {
		return sourceData{}, failure("source_not_readonly", "", err)
	}

	data := sourceData{}
	loaders := []func(context.Context, *sql.Tx, *sourceData) error{
		loadUsers, loadImages, loadPosts, loadComments, loadLikes,
		loadFavorites, loadFollows, loadNotifications, loadSourceFlavors,
	}
	for _, loader := range loaders {
		if err = loader(ctx, tx, &data); err != nil {
			return sourceData{}, err
		}
	}
	if err = ensureSourceMentionsEmpty(ctx, tx); err != nil {
		return sourceData{}, err
	}
	if err = ensureSourceTablesEmpty(ctx, tx); err != nil {
		return sourceData{}, err
	}
	if err = tx.Commit(); err != nil {
		return sourceData{}, failure("source_readonly_commit_failed", "", err)
	}
	return data, nil
}

func loadUsers(ctx context.Context, tx *sql.Tx, data *sourceData) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT ROW_NUMBER() OVER (ORDER BY created_at, id::text),
		       id::text, email, password, name, gender, hometown, avatar_url, bio,
		       role, is_active, created_at, updated_at
		FROM users ORDER BY created_at, id::text`)
	if err != nil {
		return failure("source_read_failed", "users", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var row sourceUser
		var gender, hometown, avatar, bio sql.NullString
		if err = rows.Scan(&row.TargetID, &row.ID, &row.Email, &row.Password, &row.Name, &gender, &hometown,
			&avatar, &bio, &row.Role, &row.IsActive, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return failure("source_scan_failed", "users", err)
		}
		row.Gender, row.Hometown = nullString(gender), nullString(hometown)
		row.AvatarURL, row.Bio = nullString(avatar), nullString(bio)
		data.Users = append(data.Users, row)
	}
	return rowsError(rows, "users")
}

func loadImages(ctx context.Context, tx *sql.Tx, data *sourceData) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT ROW_NUMBER() OVER (ORDER BY created_at, id::text),
		       id::text, uploader_id::text, purpose, object_key, public_url, content_type,
		       size, status, created_at, updated_at
		FROM image_assets ORDER BY created_at, id::text`)
	if err != nil {
		return failure("source_read_failed", "image_assets", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var row sourceImage
		var uploader sql.NullString
		var size sql.NullInt64
		if err = rows.Scan(&row.TargetID, &row.ID, &uploader, &row.Purpose, &row.ObjectKey, &row.PublicURL,
			&row.ContentType, &size, &row.Status, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return failure("source_scan_failed", "image_assets", err)
		}
		row.UploaderID, row.Size = nullString(uploader), nullInt64(size)
		data.Images = append(data.Images, row)
	}
	return rowsError(rows, "image_assets")
}

func loadPosts(ctx context.Context, tx *sql.Tx, data *sourceData) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT ROW_NUMBER() OVER (ORDER BY created_at, id::text),
		       id::text, post_type, title, content, category, canteen,
		       COALESCE(tags, '[]'::jsonb)::text, share_type, cuisine,
		       flavors::text, jsonb_typeof(flavors), price::text,
		       COALESCE(images, '[]'::jsonb)::text, like_count, favorite_count,
		       budget_range::text, preferences::text, jsonb_typeof(preferences), author_id::text, status,
		       comment_count, view_count, created_at, updated_at
		FROM posts ORDER BY created_at, id::text`)
	if err != nil {
		return failure("source_read_failed", "posts", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var row sourcePost
		var canteen, shareType, cuisine, price, budget sql.NullString
		var flavorsJSON, flavorsType, preferencesJSON, preferencesType sql.NullString
		var tagsJSON, imagesJSON string
		if err = rows.Scan(&row.TargetID, &row.ID, &row.PostType, &row.Title, &row.Content, &row.Category,
			&canteen, &tagsJSON, &shareType, &cuisine, &flavorsJSON, &flavorsType, &price, &imagesJSON,
			&row.LikeCount, &row.FavoriteCount, &budget, &preferencesJSON, &preferencesType, &row.AuthorID,
			&row.Status, &row.CommentCount, &row.ViewCount, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return failure("source_scan_failed", "posts", err)
		}
		row.Canteen, row.ShareType, row.Cuisine, row.Price = nullString(canteen), nullString(shareType), nullString(cuisine), nullString(price)
		if err = decodeStringArray(tagsJSON, &row.Tags); err != nil {
			return rowFailure("source_json_invalid", "posts", row.ID, "tags")
		}
		if err = decodeNullableStringArray(flavorsJSON, flavorsType, &row.Flavors); err != nil {
			return rowFailure("source_json_invalid", "posts", row.ID, "flavors")
		}
		if err = decodeStringArray(imagesJSON, &row.Images); err != nil {
			return rowFailure("source_json_invalid", "posts", row.ID, "images")
		}
		if budget.Valid && budget.String != "null" {
			var value struct {
				Min int32 `json:"min"`
				Max int32 `json:"max"`
			}
			if err = json.Unmarshal([]byte(budget.String), &value); err != nil {
				return rowFailure("source_json_invalid", "posts", row.ID, "budget_range")
			}
			row.BudgetMin, row.BudgetMax = &value.Min, &value.Max
		}
		if err = decodeNullablePreferences(preferencesJSON, preferencesType, &row.Preferences); err != nil {
			return rowFailure("source_json_invalid", "posts", row.ID, "preferences")
		}
		data.Posts = append(data.Posts, row)
	}
	return rowsError(rows, "posts")
}

func loadComments(ctx context.Context, tx *sql.Tx, data *sourceData) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT ROW_NUMBER() OVER (ORDER BY created_at, id::text),
		       id::text, content, post_id::text, author_id::text, parent_id::text,
		       reply_to_user_id::text, like_count, reply_count, created_at, updated_at
		FROM comments ORDER BY created_at, id::text`)
	if err != nil {
		return failure("source_read_failed", "comments", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var row sourceComment
		var parent, replyTo sql.NullString
		if err = rows.Scan(&row.TargetID, &row.ID, &row.Content, &row.PostID, &row.AuthorID, &parent,
			&replyTo, &row.LikeCount, &row.ReplyCount, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return failure("source_scan_failed", "comments", err)
		}
		row.ParentID, row.ReplyToUserID = nullString(parent), nullString(replyTo)
		data.Comments = append(data.Comments, row)
	}
	return rowsError(rows, "comments")
}

func loadLikes(ctx context.Context, tx *sql.Tx, data *sourceData) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id::text, user_id::text, likeable_type, likeable_id::text, created_at
		FROM likes ORDER BY created_at, id::text`)
	if err != nil {
		return failure("source_read_failed", "likes", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var row sourceLike
		if err = rows.Scan(&row.ID, &row.UserID, &row.Type, &row.TargetID, &row.CreatedAt); err != nil {
			return failure("source_scan_failed", "likes", err)
		}
		data.Likes = append(data.Likes, row)
	}
	return rowsError(rows, "likes")
}

func loadFavorites(ctx context.Context, tx *sql.Tx, data *sourceData) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id::text, user_id::text, post_id::text, created_at
		FROM favorites ORDER BY created_at, id::text`)
	if err != nil {
		return failure("source_read_failed", "favorites", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var row sourceFavorite
		if err = rows.Scan(&row.ID, &row.UserID, &row.PostID, &row.CreatedAt); err != nil {
			return failure("source_scan_failed", "favorites", err)
		}
		data.Favorites = append(data.Favorites, row)
	}
	return rowsError(rows, "favorites")
}

func loadFollows(ctx context.Context, tx *sql.Tx, data *sourceData) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id::text, follower_id::text, following_id::text, created_at
		FROM follows ORDER BY created_at, id::text`)
	if err != nil {
		return failure("source_read_failed", "follows", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var row sourceFollow
		if err = rows.Scan(&row.ID, &row.FollowerID, &row.FollowingID, &row.CreatedAt); err != nil {
			return failure("source_scan_failed", "follows", err)
		}
		data.Follows = append(data.Follows, row)
	}
	return rowsError(rows, "follows")
}

func loadNotifications(ctx context.Context, tx *sql.Tx, data *sourceData) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT ROW_NUMBER() OVER (ORDER BY created_at, id::text),
		       id::text, recipient_id::text, sender_id::text, type, related_id::text,
		       related_type, content, is_read, created_at, updated_at
		FROM notifications ORDER BY created_at, id::text`)
	if err != nil {
		return failure("source_read_failed", "notifications", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var row sourceNotification
		var relatedID, relatedType, content sql.NullString
		if err = rows.Scan(&row.TargetID, &row.ID, &row.RecipientID, &row.SenderID, &row.Type, &relatedID,
			&relatedType, &content, &row.IsRead, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return failure("source_scan_failed", "notifications", err)
		}
		row.RelatedID, row.RelatedType, row.Content = nullString(relatedID), nullString(relatedType), nullString(content)
		data.Notifications = append(data.Notifications, row)
	}
	return rowsError(rows, "notifications")
}

func loadSourceFlavors(ctx context.Context, tx *sql.Tx, data *sourceData) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id::text, name, is_active, sort_order, created_at
		FROM flavors ORDER BY created_at, id::text`)
	if err != nil {
		return failure("source_read_failed", "flavors", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var row sourceFlavor
		if err = rows.Scan(&row.ID, &row.Name, &row.IsActive, &row.SortOrder, &row.CreatedAt); err != nil {
			return failure("source_scan_failed", "flavors", err)
		}
		data.Flavors = append(data.Flavors, row)
	}
	return rowsError(rows, "flavors")
}

func ensureSourceMentionsEmpty(ctx context.Context, tx *sql.Tx) error {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM comments WHERE mentioned_user_ids IS DISTINCT FROM '[]'::jsonb`).Scan(&count)
	if err != nil {
		return failure("source_read_failed", "comments", err)
	}
	if count != 0 {
		return rowFailure("unexpected_nonempty_mentions", "comments", "count", "mentioned_user_ids")
	}
	return nil
}

func ensureSourceTablesEmpty(ctx context.Context, tx *sql.Tx) error {
	var canteens, cuisines, verificationCodes int
	err := tx.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM canteens),
		       (SELECT count(*) FROM cuisines),
		       (SELECT count(*) FROM email_verification_codes)`).
		Scan(&canteens, &cuisines, &verificationCodes)
	if err != nil {
		return failure("source_read_failed", "empty_tables", err)
	}
	if canteens != 0 || cuisines != 0 || verificationCodes != 0 {
		return rowFailure("source_expected_empty_table_nonempty", "empty_tables", "count", "rows")
	}
	return nil
}

func decodeStringArray(raw string, target *[]string) error {
	if raw == "null" {
		*target = []string{}
		return nil
	}
	return json.Unmarshal([]byte(raw), target)
}

func decodeNullableStringArray(raw, jsonType sql.NullString, target *[]string) error {
	if !raw.Valid && !jsonType.Valid {
		return nil
	}
	if !raw.Valid || !jsonType.Valid {
		return errors.New("inconsistent JSONB value and type")
	}
	if jsonType.String == "null" {
		return nil
	}
	if jsonType.String != "array" {
		return errors.New("expected JSONB array or null")
	}
	return decodeStringArray(raw.String, target)
}

func decodeNullablePreferences(raw, jsonType sql.NullString, target **sourcePreferences) error {
	if !raw.Valid && !jsonType.Valid {
		return nil
	}
	if !raw.Valid || !jsonType.Valid {
		return errors.New("inconsistent JSONB value and type")
	}
	if jsonType.String == "null" {
		return nil
	}
	if jsonType.String != "object" {
		return errors.New("expected JSONB object or null")
	}
	value := &sourcePreferences{}
	if err := json.Unmarshal([]byte(raw.String), value); err != nil {
		return errors.New("invalid preferences object")
	}
	*target = value
	return nil
}

func nullString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func nullInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func rowsError(rows *sql.Rows, table string) error {
	if err := rows.Err(); err != nil {
		return failure("source_rows_failed", table, err)
	}
	if err := rows.Close(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return failure("source_rows_close_failed", table, err)
	}
	return nil
}

func closeRows(rows *sql.Rows) {
	_ = rows.Close()
}
