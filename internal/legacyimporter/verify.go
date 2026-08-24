package legacyimporter

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"reflect"
	"sort"
	"time"
)

func collectMismatches(ctx context.Context, target *sql.DB, expected dataset, dict dictionaries) (_ []mismatch, collectErr error) {
	tx, err := target.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, failure("target_verify_begin_failed", "", err)
	}
	defer func() {
		if collectErr != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, `SET LOCAL statement_timeout='5min'`); err != nil {
		return nil, failure("target_verify_setup_failed", "", err)
	}
	actual, err := loadTargetDataset(ctx, tx)
	if err != nil {
		return nil, err
	}
	issues := compareDatasets(expected, actual)
	issues = append(issues, compareSourceFlavors(expected.SourceFlavors, dict)...)
	zeroIssues, err := verifyUntouchedTables(ctx, tx)
	if err != nil {
		return nil, err
	}
	issues = append(issues, zeroIssues...)
	if err = tx.Commit(); err != nil {
		return nil, failure("target_verify_commit_failed", "", err)
	}
	sortMismatches(issues)
	return issues, nil
}

func loadTargetDataset(ctx context.Context, tx *sql.Tx) (dataset, error) {
	result := newDataset(sourceData{})
	loaders := []func(context.Context, *sql.Tx, *dataset) error{
		loadTargetUsers, loadTargetRoles, loadTargetRoleRecords, loadTargetBanRecords,
		loadTargetImages, loadTargetPosts, loadTargetTags, loadTargetPostTags,
		loadTargetPostFlavors, loadTargetPostImages, loadTargetComments, loadTargetFollows,
		loadTargetFavorites, loadTargetPostLikes, loadTargetCommentLikes, loadTargetNotifications,
	}
	for _, loader := range loaders {
		if err := loader(ctx, tx, &result); err != nil {
			return dataset{}, err
		}
	}
	return result, nil
}

func loadTargetUsers(ctx context.Context, tx *sql.Tx, data *dataset) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id,email,password_hash,name,gender,bio,avatar_image_asset_id,ban_is_permanent,
		       banned_until,ban_reason,banned_by,deleted_at,created_at,updated_at
		FROM users ORDER BY id`)
	if err != nil {
		return failure("target_read_failed", "users", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var row userRow
		var gender, bio, reason sql.NullString
		var avatar, bannedBy sql.NullInt64
		var bannedUntil, deletedAt sql.NullTime
		if err = rows.Scan(&row.ID, &row.Email, &row.PasswordHash, &row.Name, &gender, &bio,
			&avatar, &row.BanIsPermanent, &bannedUntil, &reason, &bannedBy, &deletedAt,
			&row.CreatedAt, &row.UpdatedAt); err != nil {
			return failure("target_scan_failed", "users", err)
		}
		row.Gender, row.Bio, row.BanReason = nullString(gender), nullString(bio), nullString(reason)
		row.AvatarImageAssetID, row.BannedBy = nullInt64(avatar), nullInt64(bannedBy)
		row.BannedUntil, row.DeletedAt = nullTime(bannedUntil), nullTime(deletedAt)
		data.Users[row.ID] = row
	}
	return targetRowsError(rows, "users")
}

func loadTargetRoles(ctx context.Context, tx *sql.Tx, data *dataset) error {
	rows, err := tx.QueryContext(ctx, `SELECT user_id,role,granted_by,granted_at FROM user_roles ORDER BY user_id,role`)
	if err != nil {
		return failure("target_read_failed", "user_roles", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var row roleRow
		var grantedBy sql.NullInt64
		if err = rows.Scan(&row.UserID, &row.Role, &grantedBy, &row.GrantedAt); err != nil {
			return failure("target_scan_failed", "user_roles", err)
		}
		row.GrantedBy = nullInt64(grantedBy)
		data.Roles[roleKey(row)] = row
	}
	return targetRowsError(rows, "user_roles")
}

func loadTargetRoleRecords(ctx context.Context, tx *sql.Tx, data *dataset) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,user_id,role,action,actor_id,created_at FROM user_role_records ORDER BY id`)
	if err != nil {
		return failure("target_read_failed", "user_role_records", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var row roleRecordRow
		var actor sql.NullInt64
		if err = rows.Scan(&row.ID, &row.UserID, &row.Role, &row.Action, &actor, &row.CreatedAt); err != nil {
			return failure("target_scan_failed", "user_role_records", err)
		}
		row.ActorID = nullInt64(actor)
		data.RoleRecords[row.ID] = row
	}
	return targetRowsError(rows, "user_role_records")
}

func loadTargetBanRecords(ctx context.Context, tx *sql.Tx, data *dataset) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id,user_id,action,ban_is_permanent,banned_until,reason,actor_id,created_at
		FROM user_ban_records ORDER BY id`)
	if err != nil {
		return failure("target_read_failed", "user_ban_records", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var row banRecordRow
		var bannedUntil sql.NullTime
		var reason sql.NullString
		var actor sql.NullInt64
		if err = rows.Scan(&row.ID, &row.UserID, &row.Action, &row.BanPermanent, &bannedUntil,
			&reason, &actor, &row.CreatedAt); err != nil {
			return failure("target_scan_failed", "user_ban_records", err)
		}
		row.BannedUntil, row.Reason, row.ActorID = nullTime(bannedUntil), nullString(reason), nullInt64(actor)
		data.BanRecords[row.ID] = row
	}
	return targetRowsError(rows, "user_ban_records")
}

func loadTargetImages(ctx context.Context, tx *sql.Tx, data *dataset) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id,uploader_id,purpose,object_key,public_url,content_type,size,status,moderation,created_at,updated_at
		FROM image_assets ORDER BY id`)
	if err != nil {
		return failure("target_read_failed", "image_assets", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var row imageRow
		var uploader, size sql.NullInt64
		if err = rows.Scan(&row.ID, &uploader, &row.Purpose, &row.ObjectKey, &row.PublicURL,
			&row.ContentType, &size, &row.Status, &row.Moderation, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return failure("target_scan_failed", "image_assets", err)
		}
		row.UploaderID, row.Size = nullInt64(uploader), nullInt64(size)
		data.Images[row.ID] = row
	}
	return targetRowsError(rows, "image_assets")
}

func loadTargetPosts(ctx context.Context, tx *sql.Tx, data *dataset) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id,author_id,post_type,share_type,status,category,title,content,canteen_id,cuisine_id,
		       price::text,budget_min,budget_max,like_count,favorite_count,comment_count,view_count,
		       deleted_at,deleted_reason,deleted_by,created_at,updated_at
		FROM posts ORDER BY id`)
	if err != nil {
		return failure("target_read_failed", "posts", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var row postRow
		var shareType, price, deletedReason sql.NullString
		var canteenID, cuisineID, deletedBy sql.NullInt64
		var budgetMin, budgetMax sql.NullInt32
		var deletedAt sql.NullTime
		if err = rows.Scan(&row.ID, &row.AuthorID, &row.PostType, &shareType, &row.Status,
			&row.Category, &row.Title, &row.Content, &canteenID, &cuisineID, &price, &budgetMin,
			&budgetMax, &row.LikeCount, &row.FavoriteCount, &row.CommentCount, &row.ViewCount,
			&deletedAt, &deletedReason, &deletedBy, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return failure("target_scan_failed", "posts", err)
		}
		row.ShareType, row.Price, row.DeletedReason = nullString(shareType), nullString(price), nullString(deletedReason)
		row.CanteenID, row.CuisineID, row.DeletedBy = nullInt64(canteenID), nullInt64(cuisineID), nullInt64(deletedBy)
		row.BudgetMin, row.BudgetMax, row.DeletedAt = nullInt32(budgetMin), nullInt32(budgetMax), nullTime(deletedAt)
		data.Posts[row.ID] = row
	}
	return targetRowsError(rows, "posts")
}

func loadTargetTags(ctx context.Context, tx *sql.Tx, data *dataset) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,name,moderation,deleted_at,created_at,updated_at FROM tags ORDER BY id`)
	if err != nil {
		return failure("target_read_failed", "tags", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var row tagRow
		var deletedAt sql.NullTime
		if err = rows.Scan(&row.ID, &row.Name, &row.Moderation, &deletedAt, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return failure("target_scan_failed", "tags", err)
		}
		row.DeletedAt = nullTime(deletedAt)
		data.Tags[row.ID] = row
	}
	return targetRowsError(rows, "tags")
}

func loadTargetPostTags(ctx context.Context, tx *sql.Tx, data *dataset) error {
	rows, err := tx.QueryContext(ctx, `SELECT post_id,tag_id FROM post_tags ORDER BY post_id,tag_id`)
	if err != nil {
		return failure("target_read_failed", "post_tags", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var row postTagRow
		if err = rows.Scan(&row.PostID, &row.TagID); err != nil {
			return failure("target_scan_failed", "post_tags", err)
		}
		data.PostTags[relationKey(int64String(row.PostID), int64String(row.TagID))] = row
	}
	return targetRowsError(rows, "post_tags")
}

func loadTargetPostFlavors(ctx context.Context, tx *sql.Tx, data *dataset) error {
	rows, err := tx.QueryContext(ctx, `SELECT post_id,flavor_id,stance,post_type FROM post_flavors ORDER BY post_id,flavor_id`)
	if err != nil {
		return failure("target_read_failed", "post_flavors", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var row postFlavorRow
		if err = rows.Scan(&row.PostID, &row.FlavorID, &row.Stance, &row.PostType); err != nil {
			return failure("target_scan_failed", "post_flavors", err)
		}
		data.PostFlavors[relationKey(int64String(row.PostID), int64String(row.FlavorID))] = row
	}
	return targetRowsError(rows, "post_flavors")
}

func loadTargetPostImages(ctx context.Context, tx *sql.Tx, data *dataset) error {
	rows, err := tx.QueryContext(ctx, `SELECT post_id,position,image_asset_id,created_at FROM post_images ORDER BY post_id,position`)
	if err != nil {
		return failure("target_read_failed", "post_images", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var row postImageRow
		if err = rows.Scan(&row.PostID, &row.Position, &row.ImageAssetID, &row.CreatedAt); err != nil {
			return failure("target_scan_failed", "post_images", err)
		}
		data.PostImages[relationKey(int64String(row.PostID), intString(int(row.Position)))] = row
	}
	return targetRowsError(rows, "post_images")
}

func loadTargetComments(ctx context.Context, tx *sql.Tx, data *dataset) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id,post_id,author_id,parent_id,root_id,reply_to_user_id,content,moderation,
		       like_count,reply_count,deleted_at,deleted_reason,deleted_by,created_at,updated_at
		FROM comments ORDER BY id`)
	if err != nil {
		return failure("target_read_failed", "comments", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var row commentRow
		var parentID, rootID, deletedBy sql.NullInt64
		var deletedAt sql.NullTime
		var deletedReason sql.NullString
		if err = rows.Scan(&row.ID, &row.PostID, &row.AuthorID, &parentID, &rootID,
			&row.ReplyToUserID, &row.Content, &row.Moderation, &row.LikeCount, &row.ReplyCount,
			&deletedAt, &deletedReason, &deletedBy, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return failure("target_scan_failed", "comments", err)
		}
		row.ParentID, row.RootID, row.DeletedBy = nullInt64(parentID), nullInt64(rootID), nullInt64(deletedBy)
		row.DeletedAt, row.DeletedReason = nullTime(deletedAt), nullString(deletedReason)
		data.Comments[row.ID] = row
	}
	return targetRowsError(rows, "comments")
}

func loadTargetFollows(ctx context.Context, tx *sql.Tx, data *dataset) error {
	rows, err := tx.QueryContext(ctx, `SELECT follower_id,following_id,created_at FROM follows ORDER BY follower_id,following_id`)
	if err != nil {
		return failure("target_read_failed", "follows", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var row followRow
		if err = rows.Scan(&row.FollowerID, &row.FollowingID, &row.CreatedAt); err != nil {
			return failure("target_scan_failed", "follows", err)
		}
		data.Follows[relationKey(int64String(row.FollowerID), int64String(row.FollowingID))] = row
	}
	return targetRowsError(rows, "follows")
}

func loadTargetFavorites(ctx context.Context, tx *sql.Tx, data *dataset) error {
	rows, err := tx.QueryContext(ctx, `SELECT user_id,post_id,created_at FROM favorites ORDER BY user_id,post_id`)
	if err != nil {
		return failure("target_read_failed", "favorites", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var row favoriteRow
		if err = rows.Scan(&row.UserID, &row.PostID, &row.CreatedAt); err != nil {
			return failure("target_scan_failed", "favorites", err)
		}
		data.Favorites[relationKey(int64String(row.UserID), int64String(row.PostID))] = row
	}
	return targetRowsError(rows, "favorites")
}

func loadTargetPostLikes(ctx context.Context, tx *sql.Tx, data *dataset) error {
	rows, err := tx.QueryContext(ctx, `SELECT user_id,post_id,created_at FROM post_likes ORDER BY user_id,post_id`)
	if err != nil {
		return failure("target_read_failed", "post_likes", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var row postLikeRow
		if err = rows.Scan(&row.UserID, &row.PostID, &row.CreatedAt); err != nil {
			return failure("target_scan_failed", "post_likes", err)
		}
		data.PostLikes[relationKey(int64String(row.UserID), int64String(row.PostID))] = row
	}
	return targetRowsError(rows, "post_likes")
}

func loadTargetCommentLikes(ctx context.Context, tx *sql.Tx, data *dataset) error {
	rows, err := tx.QueryContext(ctx, `SELECT user_id,comment_id,created_at FROM comment_likes ORDER BY user_id,comment_id`)
	if err != nil {
		return failure("target_read_failed", "comment_likes", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var row commentLikeRow
		if err = rows.Scan(&row.UserID, &row.CommentID, &row.CreatedAt); err != nil {
			return failure("target_scan_failed", "comment_likes", err)
		}
		data.CommentLikes[relationKey(int64String(row.UserID), int64String(row.CommentID))] = row
	}
	return targetRowsError(rows, "comment_likes")
}

func loadTargetNotifications(ctx context.Context, tx *sql.Tx, data *dataset) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id,recipient_id,sender_id,type,related_post_id,related_comment_id,content,is_read,created_at,updated_at
		FROM notifications ORDER BY id`)
	if err != nil {
		return failure("target_read_failed", "notifications", err)
	}
	defer closeRows(rows)
	for rows.Next() {
		var row notificationRow
		var postID, commentID sql.NullInt64
		var content sql.NullString
		if err = rows.Scan(&row.ID, &row.RecipientID, &row.SenderID, &row.Type, &postID,
			&commentID, &content, &row.IsRead, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return failure("target_scan_failed", "notifications", err)
		}
		row.RelatedPostID, row.RelatedCommentID, row.Content = nullInt64(postID), nullInt64(commentID), nullString(content)
		data.Notifications[row.ID] = row
	}
	return targetRowsError(rows, "notifications")
}

func compareDatasets(expected, actual dataset) []mismatch {
	issues := make([]mismatch, 0)
	issues = append(issues, compareMap("users", expected.Users, actual.Users)...)
	issues = append(issues, compareMap("user_roles", expected.Roles, actual.Roles)...)
	issues = append(issues, compareMap("user_role_records", expected.RoleRecords, actual.RoleRecords)...)
	issues = append(issues, compareMap("user_ban_records", expected.BanRecords, actual.BanRecords)...)
	issues = append(issues, compareMap("image_assets", expected.Images, actual.Images)...)
	issues = append(issues, compareMap("posts", expected.Posts, actual.Posts)...)
	issues = append(issues, compareMap("tags", expected.Tags, actual.Tags)...)
	issues = append(issues, compareMap("post_tags", expected.PostTags, actual.PostTags)...)
	issues = append(issues, compareMap("post_flavors", expected.PostFlavors, actual.PostFlavors)...)
	issues = append(issues, compareMap("post_images", expected.PostImages, actual.PostImages)...)
	issues = append(issues, compareMap("comments", expected.Comments, actual.Comments)...)
	issues = append(issues, compareMap("follows", expected.Follows, actual.Follows)...)
	issues = append(issues, compareMap("favorites", expected.Favorites, actual.Favorites)...)
	issues = append(issues, compareMap("post_likes", expected.PostLikes, actual.PostLikes)...)
	issues = append(issues, compareMap("comment_likes", expected.CommentLikes, actual.CommentLikes)...)
	issues = append(issues, compareMap("notifications", expected.Notifications, actual.Notifications)...)
	return issues
}

func compareMap[K comparable, V any](table string, expected, actual map[K]V) []mismatch {
	issues := make([]mismatch, 0)
	for key, expectedRow := range expected {
		actualRow, exists := actual[key]
		sourceID := structSourceID(expectedRow)
		if !exists {
			issues = append(issues, mismatch{Table: table, SourceID: sourceID, Field: "row", Code: "missing"})
			continue
		}
		issues = append(issues, compareStruct(table, sourceID, expectedRow, actualRow)...)
	}
	for key := range actual {
		if _, exists := expected[key]; !exists {
			issues = append(issues, mismatch{Table: table, SourceID: "target:" + fmt.Sprint(key), Field: "row", Code: "unexpected"})
		}
	}
	return issues
}

func compareStruct(table, sourceID string, expected, actual any) []mismatch {
	expectedValue, actualValue := reflect.ValueOf(expected), reflect.ValueOf(actual)
	typeInfo := expectedValue.Type()
	issues := make([]mismatch, 0)
	for index := 0; index < expectedValue.NumField(); index++ {
		field := typeInfo.Field(index).Tag.Get("verify")
		if field == "-" || field == "" {
			continue
		}
		if !verifyValuesEqual(expectedValue.Field(index), actualValue.Field(index)) {
			issues = append(issues, mismatch{Table: table, SourceID: sourceID, Field: field, Code: "value_mismatch"})
		}
	}
	return issues
}

func verifyValuesEqual(expected, actual reflect.Value) bool {
	if expected.Kind() == reflect.Pointer {
		if expected.IsNil() || actual.IsNil() {
			return expected.IsNil() == actual.IsNil()
		}
		return verifyValuesEqual(expected.Elem(), actual.Elem())
	}
	if expected.Type() == reflect.TypeOf(time.Time{}) {
		return expected.Interface().(time.Time).Equal(actual.Interface().(time.Time))
	}
	return reflect.DeepEqual(expected.Interface(), actual.Interface())
}

func structSourceID(value any) string {
	field := reflect.ValueOf(value).FieldByName("SourceID")
	if field.IsValid() && field.String() != "" {
		return field.String()
	}
	return "mapped"
}

func compareSourceFlavors(source []sourceFlavor, dict dictionaries) []mismatch {
	issues := make([]mismatch, 0)
	for _, row := range source {
		target, exists := dict.Flavors[row.Name]
		if !exists {
			issues = append(issues, mismatch{Table: "flavors", SourceID: row.ID, Field: "row", Code: "missing_seed_equivalent"})
			continue
		}
		if target.IsActive != row.IsActive {
			issues = append(issues, mismatch{Table: "flavors", SourceID: row.ID, Field: "is_active", Code: "value_mismatch"})
		}
	}
	return issues
}

func verifyUntouchedTables(ctx context.Context, tx *sql.Tx) ([]mismatch, error) {
	tables := []string{
		"comment_mentions", "post_histories", "comment_histories", "moderation_records",
		"email_verification_codes", "user_sessions", "dictionary_suggestions",
	}
	issues := make([]mismatch, 0)
	for _, table := range tables {
		var count int
		query := fmt.Sprintf("SELECT count(*) FROM %s", table) // fixed allowlist.
		if err := tx.QueryRowContext(ctx, query).Scan(&count); err != nil {
			return nil, failure("target_read_failed", table, err)
		}
		if count != 0 {
			issues = append(issues, mismatch{Table: table, SourceID: "target", Field: "rows", Code: "unexpected_nonempty"})
		}
	}
	return issues, nil
}

func writeVerifyReport(output io.Writer, data dataset, issues []mismatch) {
	events := append([]decisionEvent(nil), data.Events...)
	sort.Slice(events, func(i, j int) bool {
		left := events[i].Kind + events[i].Table + events[i].SourceID + events[i].Field + events[i].Code
		right := events[j].Kind + events[j].Table + events[j].SourceID + events[j].Field + events[j].Code
		return left < right
	})
	for _, event := range events {
		_, _ = fmt.Fprintf(output, "%s table=%s source_id=%s field=%s code=%s\n",
			event.Kind, event.Table, event.SourceID, event.Field, event.Code)
	}
	tables := make([]string, 0, len(data.Stats))
	for table := range data.Stats {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		stat := data.Stats[table]
		_, _ = fmt.Fprintf(output, "VERIFY table=%s source_rows=%d expected_target_rows=%d omitted_rows=%d\n",
			table, stat.SourceRows, stat.TargetRows, stat.OmittedRows)
	}
	for _, issue := range issues {
		_, _ = fmt.Fprintf(output, "MISMATCH table=%s source_id=%s field=%s code=%s\n",
			issue.Table, issue.SourceID, issue.Field, issue.Code)
	}
	if len(issues) == 0 {
		_, _ = fmt.Fprintf(output, "VERIFY_OK mismatches=0 users=%d posts=%d comments=%d images=%d notifications=%d\n",
			len(data.Users), len(data.Posts), len(data.Comments), len(data.Images), len(data.Notifications))
	} else {
		_, _ = fmt.Fprintf(output, "VERIFY_FAILED mismatches=%d\n", len(issues))
	}
}

func sortMismatches(issues []mismatch) {
	sort.Slice(issues, func(i, j int) bool {
		left := issues[i].Table + issues[i].SourceID + issues[i].Field + issues[i].Code
		right := issues[j].Table + issues[j].SourceID + issues[j].Field + issues[j].Code
		return left < right
	})
}

func targetRowsError(rows *sql.Rows, table string) error {
	if err := rows.Err(); err != nil {
		return failure("target_rows_failed", table, err)
	}
	return nil
}

func nullTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}

func nullInt32(value sql.NullInt32) *int32 {
	if !value.Valid {
		return nil
	}
	result := value.Int32
	return &result
}
