package legacyimporter

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

func transformSource(source sourceData, dict dictionaries) (dataset, error) {
	result := newDataset(source)
	userIDs, postIDs, commentIDs := map[string]int64{}, map[string]int64{}, map[string]int64{}
	if err := registerSequentialIDs(source.Users, userIDs,
		func(row sourceUser) string { return row.ID }, func(row sourceUser) int64 { return row.TargetID }, "users"); err != nil {
		return dataset{}, err
	}
	if err := registerSequentialIDs(source.Posts, postIDs,
		func(row sourcePost) string { return row.ID }, func(row sourcePost) int64 { return row.TargetID }, "posts"); err != nil {
		return dataset{}, err
	}
	if err := registerSequentialIDs(source.Comments, commentIDs,
		func(row sourceComment) string { return row.ID }, func(row sourceComment) int64 { return row.TargetID }, "comments"); err != nil {
		return dataset{}, err
	}
	imageIDs, imageByURL, err := transformImages(source, userIDs, &result)
	if err != nil {
		return dataset{}, err
	}
	if err = transformUsers(source, userIDs, imageByURL, &result); err != nil {
		return dataset{}, err
	}
	if err = transformPosts(source, dict, userIDs, postIDs, imageByURL, &result); err != nil {
		return dataset{}, err
	}
	if err = transformComments(source, userIDs, postIDs, commentIDs, &result); err != nil {
		return dataset{}, err
	}
	if err = transformRelations(source, userIDs, postIDs, commentIDs, &result); err != nil {
		return dataset{}, err
	}
	if err = transformNotifications(source, userIDs, postIDs, commentIDs, &result); err != nil {
		return dataset{}, err
	}
	if err = validateImageReferences(imageIDs, &result); err != nil {
		return dataset{}, err
	}
	for _, flavor := range source.Flavors {
		addEvent(&result, "TRANSFORM", "flavors", flavor.ID, "sort_order", "target_seed_order_authoritative")
	}
	deriveCounters(&result)
	populateDetailedStats(source, &result)
	if err = validateDecisionCounts(source, result); err != nil {
		return dataset{}, err
	}
	return result, nil
}

func newDataset(source sourceData) dataset {
	return dataset{
		Users: map[int64]userRow{}, Roles: map[string]roleRow{}, RoleRecords: map[int64]roleRecordRow{},
		BanRecords: map[int64]banRecordRow{}, Images: map[int64]imageRow{},
		Cuisines: map[int64]dictionaryRow{}, Flavors: map[int64]dictionaryRow{}, Posts: map[int64]postRow{},
		Tags: map[int64]tagRow{}, PostTags: map[string]postTagRow{}, PostFlavors: map[string]postFlavorRow{},
		PostImages: map[string]postImageRow{}, Comments: map[int64]commentRow{}, Follows: map[string]followRow{},
		Favorites: map[string]favoriteRow{}, PostLikes: map[string]postLikeRow{}, CommentLikes: map[string]commentLikeRow{},
		Notifications: map[int64]notificationRow{}, SourceFlavors: source.Flavors, Stats: map[string]tableStat{},
	}
}

func registerSequentialIDs[T any](
	rows []T,
	target map[string]int64,
	sourceID func(T) string,
	targetID func(T) int64,
	table string,
) error {
	reverse := map[int64]string{}
	for _, row := range rows {
		source := sourceID(row)
		mapped := targetID(row)
		if !isUUID(source) {
			return rowFailure("invalid_uuid", table, source, "id")
		}
		if mapped <= 0 {
			return rowFailure("invalid_sequential_id", table, source, "id")
		}
		if previous, exists := target[source]; exists && previous != mapped {
			return rowFailure("duplicate_source_id", table, source, "id")
		}
		if previous, exists := reverse[mapped]; exists && previous != source {
			return rowFailure("duplicate_sequential_id", table, source, "id")
		}
		reverse[mapped], target[source] = source, mapped
	}
	return nil
}

func transformImages(source sourceData, userIDs map[string]int64, result *dataset) (map[string]int64, map[string]int64, error) {
	imageIDs, imageByURL, reverse := map[string]int64{}, map[string]int64{}, map[int64]string{}
	for _, sourceRow := range source.Images {
		if sourceRow.PublicURL == "" && sourceRow.Status == "pending" {
			addEvent(result, "OMIT", "image_assets", sourceRow.ID, "public_url", "unfinished_upload")
			continue
		}
		if sourceRow.PublicURL == "" {
			return nil, nil, rowFailure("unexpected_empty_url", "image_assets", sourceRow.ID, "public_url")
		}
		if sourceRow.Status != "ready" || (sourceRow.Purpose != "post" && sourceRow.Purpose != "avatar") ||
			!strings.HasPrefix(sourceRow.PublicURL, "https://img.fdueat.com/") {
			return nil, nil, rowFailure("unexpected_image_asset", "image_assets", sourceRow.ID, "shape")
		}
		if !isUUID(sourceRow.ID) {
			return nil, nil, rowFailure("invalid_uuid", "image_assets", sourceRow.ID, "id")
		}
		id := sourceRow.TargetID
		if id <= 0 {
			return nil, nil, rowFailure("invalid_sequential_id", "image_assets", sourceRow.ID, "id")
		}
		if previous, exists := reverse[id]; exists && previous != sourceRow.ID {
			return nil, nil, rowFailure("deterministic_id_collision", "image_assets", sourceRow.ID, "id")
		}
		var uploaderID *int64
		if sourceRow.UploaderID != nil {
			mapped, exists := userIDs[*sourceRow.UploaderID]
			if !exists {
				return nil, nil, rowFailure("missing_user", "image_assets", sourceRow.ID, "uploader_id")
			}
			uploaderID = int64Pointer(mapped)
		}
		if _, duplicate := imageByURL[sourceRow.PublicURL]; duplicate {
			return nil, nil, rowFailure("duplicate_public_url", "image_assets", sourceRow.ID, "public_url")
		}
		result.Images[id] = imageRow{
			SourceID: sourceRow.ID, ID: id, UploaderID: uploaderID,
			Purpose: sourceRow.Purpose, ObjectKey: sourceRow.ObjectKey, PublicURL: sourceRow.PublicURL,
			ContentType: sourceRow.ContentType, Size: sourceRow.Size, Status: "ready", Moderation: "pass",
			CreatedAt: sourceRow.CreatedAt, UpdatedAt: sourceRow.UpdatedAt,
		}
		imageIDs[sourceRow.ID], imageByURL[sourceRow.PublicURL], reverse[id] = id, id, sourceRow.ID
	}
	result.Stats["image_assets"] = tableStat{SourceRows: len(source.Images), TargetRows: len(result.Images), OmittedRows: len(source.Images) - len(result.Images)}
	return imageIDs, imageByURL, nil
}

func transformUsers(source sourceData, userIDs, imageByURL map[string]int64, result *dataset) error {
	for _, sourceRow := range source.Users {
		id := userIDs[sourceRow.ID]
		avatarID, err := resolveAvatar(sourceRow, id, imageByURL, result)
		if err != nil {
			return err
		}
		var banReason *string
		if !sourceRow.IsActive {
			banReason = stringPointer(legacyBanReason)
			addEvent(result, "TRANSFORM", "users", sourceRow.ID, "is_active", "inactive_to_permanent_ban")
		}
		result.Users[id] = userRow{
			SourceID: sourceRow.ID, ID: id, Email: sourceRow.Email,
			PasswordHash: sourceRow.Password, Name: sourceRow.Name, Gender: sourceRow.Gender, Bio: sourceRow.Bio,
			AvatarImageAssetID: avatarID, BanIsPermanent: !sourceRow.IsActive, BanReason: banReason,
			CreatedAt: sourceRow.CreatedAt, UpdatedAt: sourceRow.UpdatedAt,
		}
		if sourceRow.Hometown != nil {
			addEvent(result, "OMIT", "users", sourceRow.ID, "hometown", "target_field_absent")
		}
		if err := transformUserRoleAndBan(sourceRow, id, result); err != nil {
			return err
		}
	}
	result.Stats["users"] = tableStat{SourceRows: len(source.Users), TargetRows: len(result.Users)}
	return nil
}

func resolveAvatar(sourceRow sourceUser, userID int64, imageByURL map[string]int64, result *dataset) (*int64, error) {
	if sourceRow.AvatarURL == nil {
		return nil, nil
	}
	if isFakeAvatar(*sourceRow.AvatarURL) {
		addEvent(result, "TRANSFORM", "users", sourceRow.ID, "avatar_url", "fake_avatar_to_null")
		return nil, nil
	}
	mapped, exists := imageByURL[*sourceRow.AvatarURL]
	if !exists {
		return nil, rowFailure("avatar_asset_not_found", "users", sourceRow.ID, "avatar_url")
	}
	asset := result.Images[mapped]
	if asset.Purpose != "avatar" || asset.UploaderID == nil || *asset.UploaderID != userID {
		return nil, rowFailure("invalid_avatar_asset", "users", sourceRow.ID, "avatar_url")
	}
	return int64Pointer(mapped), nil
}

func transformUserRoleAndBan(sourceRow sourceUser, userID int64, result *dataset) error {
	role := ""
	switch sourceRow.Role {
	case "user":
	case "admin":
		role = "moderator"
	case "super_admin":
		role = "super_admin"
	default:
		return rowFailure("unknown_role", "users", sourceRow.ID, "role")
	}
	if role != "" {
		key := relationKey(int64String(userID), role)
		result.Roles[key] = roleRow{SourceID: sourceRow.ID, UserID: userID, Role: role, GrantedAt: sourceRow.UpdatedAt}
		recordID := int64(len(result.RoleRecords) + 1)
		result.RoleRecords[recordID] = roleRecordRow{
			SourceID: sourceRow.ID, ID: recordID,
			UserID: userID, Role: role, Action: "grant", CreatedAt: sourceRow.UpdatedAt,
		}
		addEvent(result, "TRANSFORM", "users", sourceRow.ID, "role", "role_to_capability_binding")
	}
	if !sourceRow.IsActive {
		recordID := int64(len(result.BanRecords) + 1)
		result.BanRecords[recordID] = banRecordRow{
			SourceID: sourceRow.ID, ID: recordID,
			UserID: userID, Action: "ban", BanPermanent: true, Reason: stringPointer(legacyBanReason),
			CreatedAt: sourceRow.UpdatedAt,
		}
	}
	return nil
}

func transformPosts(source sourceData, dict dictionaries, userIDs, postIDs, imageByURL map[string]int64, result *dataset) error {
	for _, sourceRow := range source.Posts {
		postType := sourceRow.PostType
		if postType == "companion" {
			postType = "seeking"
			addEvent(result, "TRANSFORM", "posts", sourceRow.ID, "post_type", "companion_to_seeking")
		}
		if postType != "share" && postType != "seeking" {
			return rowFailure("unknown_post_type", "posts", sourceRow.ID, "post_type")
		}
		postID, authorID := postIDs[sourceRow.ID], userIDs[sourceRow.AuthorID]
		if authorID == 0 {
			return rowFailure("missing_user", "posts", sourceRow.ID, "author_id")
		}
		canteenID, err := resolveCanteen(sourceRow, dict)
		if err != nil {
			return err
		}
		cuisineID, err := resolveCuisine(sourceRow, dict, result)
		if err != nil {
			return err
		}
		price, err := normalizePrice(sourceRow)
		if err != nil {
			return err
		}
		result.Posts[postID] = postRow{
			SourceID: sourceRow.ID, ID: postID, AuthorID: authorID,
			PostType: postType, ShareType: sourceRow.ShareType, Status: sourceRow.Status, Category: sourceRow.Category,
			Title: sourceRow.Title, Content: sourceRow.Content, CanteenID: canteenID, CuisineID: cuisineID,
			Price: price, BudgetMin: sourceRow.BudgetMin, BudgetMax: sourceRow.BudgetMax,
			ViewCount: sourceRow.ViewCount, CreatedAt: sourceRow.CreatedAt, UpdatedAt: sourceRow.UpdatedAt,
		}
		if err = transformPostTags(sourceRow, postID, result); err != nil {
			return err
		}
		if err = transformPostFlavors(sourceRow, postType, postID, dict, result); err != nil {
			return err
		}
		if err = transformPostImages(sourceRow, postID, imageByURL, result); err != nil {
			return err
		}
	}
	result.Stats["posts"] = tableStat{SourceRows: len(source.Posts), TargetRows: len(result.Posts)}
	return nil
}

func resolveCanteen(sourceRow sourcePost, dict dictionaries) (*int64, error) {
	if sourceRow.Canteen == nil {
		return nil, nil
	}
	id, exists := dict.Canteens[*sourceRow.Canteen]
	if !exists {
		return nil, rowFailure("canteen_not_found", "posts", sourceRow.ID, "canteen")
	}
	return int64Pointer(id), nil
}

func resolveCuisine(sourceRow sourcePost, dict dictionaries, result *dataset) (*int64, error) {
	if sourceRow.Cuisine == nil {
		return nil, nil
	}
	var targetName, eventCode string
	switch *sourceRow.Cuisine {
	case "西餐":
		targetName, eventCode = "西式", "cuisine_seed_alias"
	case "快餐":
		targetName, eventCode = "其他", "cuisine_to_other"
	case "云南菜", "台湾菜", "江西菜":
		id, err := ensureHistoricalDictionary(
			"cuisines", *sourceRow.Cuisine, sourceRow.ID, sourceRow.CreatedAt,
			legacyCuisineSortOrder(*sourceRow.Cuisine), dict.Cuisines, result.Cuisines,
		)
		if err != nil {
			return nil, err
		}
		addEvent(result, "TRANSFORM", "posts", sourceRow.ID, "cuisine", "cuisine_historical_inactive")
		return int64Pointer(id), nil
	default:
		return nil, rowFailure("cuisine_mapping_undefined", "posts", sourceRow.ID, "cuisine")
	}
	item, exists := dict.Cuisines[targetName]
	if !exists {
		return nil, rowFailure("cuisine_seed_not_found", "posts", sourceRow.ID, "cuisine")
	}
	addEvent(result, "TRANSFORM", "posts", sourceRow.ID, "cuisine", eventCode)
	return int64Pointer(item.ID), nil
}

func legacyCuisineSortOrder(name string) int32 {
	switch name {
	case "云南菜":
		return 10_000
	case "台湾菜":
		return 10_010
	case "江西菜":
		return 10_020
	default:
		return 0
	}
}

func normalizePrice(sourceRow sourcePost) (*string, error) {
	if sourceRow.Price == nil {
		return nil, nil
	}
	value, err := decimal.NewFromString(*sourceRow.Price)
	if err != nil || value.Exponent() < -2 {
		return nil, rowFailure("invalid_price", "posts", sourceRow.ID, "price")
	}
	canonical := value.StringFixed(2)
	return &canonical, nil
}

func transformPostTags(sourceRow sourcePost, postID int64, result *dataset) error {
	for _, name := range sourceRow.Tags {
		if name == "" || strings.TrimSpace(name) != name || len([]rune(name)) > 10 {
			return rowFailure("invalid_tag", "posts", sourceRow.ID, "tags")
		}
		tagID := int64(0)
		for existingID, existing := range result.Tags {
			if strings.EqualFold(existing.Name, name) {
				tagID = existingID
				break
			}
		}
		if tagID == 0 {
			tagID = int64(len(result.Tags) + 1)
		}
		if existing, exists := result.Tags[tagID]; !exists {
			result.Tags[tagID] = tagRow{
				SourceID: sourceRow.ID, ID: tagID, Name: name, Moderation: "pass",
				CreatedAt: sourceRow.CreatedAt, UpdatedAt: sourceRow.CreatedAt,
			}
		} else if sourceRow.CreatedAt.Before(existing.CreatedAt) {
			existing.CreatedAt, existing.UpdatedAt, existing.SourceID = sourceRow.CreatedAt, sourceRow.CreatedAt, sourceRow.ID
			result.Tags[tagID] = existing
		}
		key := relationKey(int64String(postID), int64String(tagID))
		if _, duplicate := result.PostTags[key]; duplicate {
			return rowFailure("duplicate_tag_reference", "posts", sourceRow.ID, "tags")
		}
		result.PostTags[key] = postTagRow{SourceID: sourceRow.ID, PostID: postID, TagID: tagID}
	}
	return nil
}

func transformPostFlavors(sourceRow sourcePost, postType string, postID int64, dict dictionaries, result *dataset) error {
	if len(sourceRow.Flavors) != 0 && postType != "share" {
		return rowFailure("flavor_stance_undefined", "posts", sourceRow.ID, "flavors")
	}
	for _, name := range sourceRow.Flavors {
		if err := transformPostFlavorValue(sourceRow, postType, postID, name, "has", "flavors", dict, result); err != nil {
			return err
		}
	}
	if sourceRow.Preferences == nil {
		return nil
	}
	if postType != "seeking" {
		return rowFailure("preference_stance_undefined", "posts", sourceRow.ID, "preferences")
	}
	for _, name := range sourceRow.Preferences.PreferFlavors {
		if err := transformPostFlavorValue(
			sourceRow, postType, postID, name, "prefer", "preferences.prefer_flavors", dict, result,
		); err != nil {
			return err
		}
	}
	for _, name := range sourceRow.Preferences.AvoidFlavors {
		if err := transformPostFlavorValue(
			sourceRow, postType, postID, name, "avoid", "preferences.avoid_flavors", dict, result,
		); err != nil {
			return err
		}
	}
	return nil
}

func transformPostFlavorValue(
	sourceRow sourcePost,
	postType string,
	postID int64,
	name, stance, field string,
	dict dictionaries,
	result *dataset,
) error {
	var flavorID int64
	var targetName, eventCode string
	switch {
	case stance == "has" && (name == "咸" || name == "辣" || name == "酸甜"):
		var err error
		flavorID, err = ensureHistoricalDictionary(
			"flavors", name, sourceRow.ID, sourceRow.CreatedAt,
			legacyFlavorSortOrder(name), dict.Flavors, result.Flavors,
		)
		if err != nil {
			return err
		}
		eventCode = "flavor_historical_inactive"
	case stance == "prefer" && name == "清淡":
		targetName, eventCode = "清淡", "flavor_seed_match"
	case stance == "avoid" && name == "麻辣":
		targetName, eventCode = "麻辣", "flavor_seed_match"
	case stance == "avoid" && name == "重辣":
		targetName, eventCode = "特辣", "flavor_seed_alias"
	default:
		return rowFailure("flavor_mapping_undefined", "posts", sourceRow.ID, field)
	}
	if targetName != "" {
		item, exists := dict.Flavors[targetName]
		if !exists {
			return rowFailure("flavor_seed_not_found", "posts", sourceRow.ID, field)
		}
		flavorID = item.ID
	}
	addEvent(result, "TRANSFORM", "posts", sourceRow.ID, field, eventCode)
	return addPostFlavor(sourceRow.ID, field, postID, flavorID, stance, postType, result)
}

func addPostFlavor(sourceID, field string, postID, flavorID int64, stance, postType string, result *dataset) error {
	if (postType == "share") != (stance == "has") {
		return rowFailure("flavor_stance_post_type_mismatch", "posts", sourceID, field)
	}
	key := relationKey(int64String(postID), int64String(flavorID))
	if existing, duplicate := result.PostFlavors[key]; duplicate {
		if existing.Stance != stance || existing.PostType != postType {
			return rowFailure("conflicting_flavor_stance", "posts", sourceID, field)
		}
		addEvent(result, "TRANSFORM", "posts", sourceID, field, "flavor_mapping_deduplicated")
		return nil
	}
	result.PostFlavors[key] = postFlavorRow{
		SourceID: sourceID, PostID: postID, FlavorID: flavorID, Stance: stance, PostType: postType,
	}
	return nil
}

func ensureHistoricalDictionary(
	table, name, sourceID string,
	createdAt time.Time,
	sortOrder int32,
	existing map[string]dictionaryItem,
	target map[int64]dictionaryRow,
) (int64, error) {
	id, err := historicalDictionaryID(table, name, existing)
	if err != nil {
		return 0, rowFailure("historical_dictionary_id_undefined", table, sourceID, "id")
	}
	for existingName, item := range existing {
		if item.ID == id && existingName != name {
			return 0, rowFailure("deterministic_id_collision", table, sourceID, "id")
		}
	}
	if item, exists := existing[name]; exists && item.ID != id {
		return 0, rowFailure("historical_dictionary_id_mismatch", table, sourceID, "id")
	}
	if current, exists := target[id]; exists {
		if current.Name != name {
			return 0, rowFailure("deterministic_id_collision", table, sourceID, "id")
		}
		if createdAt.Before(current.CreatedAt) {
			current.SourceID, current.CreatedAt, current.UpdatedAt = sourceID, createdAt, createdAt
			target[id] = current
		}
		return id, nil
	}
	target[id] = dictionaryRow{
		SourceID: sourceID, ID: id, Name: name, SortOrder: sortOrder, IsActive: false,
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	return id, nil
}

func historicalDictionaryID(table, name string, existing map[string]dictionaryItem) (int64, error) {
	var names []string
	switch table {
	case "cuisines":
		names = []string{"云南菜", "台湾菜", "江西菜"}
	case "flavors":
		names = []string{"咸", "辣", "酸甜"}
	default:
		return 0, rowFailure("unknown_historical_dictionary", table, name, "id")
	}
	base := int64(0)
	for existingName, item := range existing {
		if !containsString(names, existingName) && item.ID > base {
			base = item.ID
		}
	}
	for index, candidate := range names {
		if candidate == name {
			return base + int64(index) + 1, nil
		}
	}
	return 0, rowFailure("unknown_historical_dictionary", table, name, "id")
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func legacyFlavorSortOrder(name string) int32 {
	switch name {
	case "咸":
		return 10_000
	case "辣":
		return 10_010
	case "酸甜":
		return 10_020
	default:
		return 0
	}
}

func transformPostImages(sourceRow sourcePost, postID int64, imageByURL map[string]int64, result *dataset) error {
	position := int16(0)
	for sourcePosition, publicURL := range sourceRow.Images {
		if isPlaceholderImage(publicURL) {
			addEvent(result, "OMIT", "post_images", sourceRow.ID+"#"+intString(sourcePosition), "image_url", "placeholder_url")
			continue
		}
		imageID, exists := imageByURL[publicURL]
		if !exists {
			return rowFailure("image_asset_not_found", "posts", sourceRow.ID, "images")
		}
		if result.Images[imageID].Purpose != "post" {
			return rowFailure("invalid_post_image_asset", "posts", sourceRow.ID, "images")
		}
		if position >= 9 {
			return rowFailure("too_many_images", "posts", sourceRow.ID, "images")
		}
		key := relationKey(int64String(postID), intString(int(position)))
		result.PostImages[key] = postImageRow{
			SourceID: sourceRow.ID, PostID: postID,
			Position: position, ImageAssetID: imageID, CreatedAt: sourceRow.CreatedAt,
		}
		position++
	}
	return nil
}

func transformComments(source sourceData, userIDs, postIDs, commentIDs map[string]int64, result *dataset) error {
	byID := make(map[string]sourceComment, len(source.Comments))
	for _, row := range source.Comments {
		byID[row.ID] = row
	}
	originalRoots := map[string]string{}
	for _, row := range source.Comments {
		root, err := findOriginalRoot(row.ID, byID)
		if err != nil {
			return err
		}
		originalRoots[row.ID] = root
	}
	ordered := append([]sourceComment(nil), source.Comments...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].CreatedAt.Equal(ordered[j].CreatedAt) {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].CreatedAt.Before(ordered[j].CreatedAt)
	})
	for _, sourceRow := range ordered {
		if err := transformComment(sourceRow, source.Comments, byID, originalRoots, userIDs, postIDs, commentIDs, result); err != nil {
			return err
		}
	}
	result.Stats["comments"] = tableStat{SourceRows: len(source.Comments), TargetRows: len(result.Comments)}
	return nil
}

func transformComment(sourceRow sourceComment, all []sourceComment, byID map[string]sourceComment, roots map[string]string,
	userIDs, postIDs, commentIDs map[string]int64, result *dataset,
) error {
	id, postID, authorID := commentIDs[sourceRow.ID], postIDs[sourceRow.PostID], userIDs[sourceRow.AuthorID]
	post, exists := result.Posts[postID]
	if !exists || authorID == 0 {
		return rowFailure("missing_endpoint", "comments", sourceRow.ID, "post_id")
	}
	row := commentRow{
		SourceID: sourceRow.ID, ID: id, PostID: postID, AuthorID: authorID,
		Content: sourceRow.Content, Moderation: "pass", CreatedAt: sourceRow.CreatedAt, UpdatedAt: sourceRow.UpdatedAt,
	}
	if sourceRow.ParentID == nil {
		row.ReplyToUserID = post.AuthorID
		addEvent(result, "TRANSFORM", "comments", sourceRow.ID, "reply_to_user_id", "root_reply_to_post_author")
		result.Comments[id] = row
		return nil
	}
	parentSource, exists := byID[*sourceRow.ParentID]
	if !exists {
		return rowFailure("parent_not_found", "comments", sourceRow.ID, "parent_id")
	}
	chosenParentID, err := chooseReplyParent(sourceRow, parentSource, all, roots, result)
	if err != nil {
		return err
	}
	if sourceRow.ReplyToUserID == nil {
		return rowFailure("missing_reply_target", "comments", sourceRow.ID, "reply_to_user_id")
	}
	parent, exists := result.Comments[commentIDs[chosenParentID]]
	if !exists {
		return rowFailure("parent_not_earlier", "comments", sourceRow.ID, "parent_id")
	}
	row.ParentID = int64Pointer(parent.ID)
	if parent.RootID == nil {
		row.RootID = int64Pointer(parent.ID)
	} else {
		row.RootID = int64Pointer(*parent.RootID)
	}
	row.ReplyToUserID = parent.AuthorID
	result.Comments[id] = row
	return nil
}

func chooseReplyParent(row, parent sourceComment, all []sourceComment, roots map[string]string, result *dataset) (string, error) {
	if row.ReplyToUserID == nil || *row.ReplyToUserID == parent.AuthorID {
		return parent.ID, nil
	}
	if row.ID == specialReplyFallbackID {
		addEvent(result, "TRANSFORM", "comments", row.ID, "reply_to_user_id", "reply_to_parent_fallback")
		return parent.ID, nil
	}
	candidate, err := inferReplyParent(row, all, roots)
	if err != nil {
		return "", err
	}
	addEvent(result, "TRANSFORM", "comments", row.ID, "parent_id", "reply_parent_inferred")
	return candidate.ID, nil
}

func findOriginalRoot(id string, byID map[string]sourceComment) (string, error) {
	seen := map[string]bool{}
	current := id
	for {
		if seen[current] {
			return "", rowFailure("comment_cycle", "comments", id, "parent_id")
		}
		seen[current] = true
		row, exists := byID[current]
		if !exists {
			return "", rowFailure("parent_not_found", "comments", id, "parent_id")
		}
		if row.ParentID == nil {
			return row.ID, nil
		}
		current = *row.ParentID
	}
}

func inferReplyParent(row sourceComment, all []sourceComment, roots map[string]string) (sourceComment, error) {
	candidates := make([]sourceComment, 0, 2)
	for _, candidate := range all {
		if candidate.PostID == row.PostID && candidate.AuthorID == *row.ReplyToUserID &&
			candidate.CreatedAt.Before(row.CreatedAt) && roots[candidate.ID] == roots[row.ID] {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) != 1 {
		return sourceComment{}, rowFailure("reply_parent_not_unique", "comments", row.ID, "parent_id")
	}
	return candidates[0], nil
}

func transformRelations(source sourceData, userIDs, postIDs, commentIDs map[string]int64, result *dataset) error {
	for _, row := range source.Follows {
		followerID, followingID := userIDs[row.FollowerID], userIDs[row.FollowingID]
		if followerID == 0 || followingID == 0 || followerID == followingID {
			return rowFailure("invalid_follow", "follows", row.ID, "endpoint")
		}
		key := relationKey(int64String(followerID), int64String(followingID))
		if _, exists := result.Follows[key]; exists {
			return rowFailure("duplicate_relation", "follows", row.ID, "endpoint")
		}
		result.Follows[key] = followRow{SourceID: row.ID, FollowerID: followerID, FollowingID: followingID, CreatedAt: row.CreatedAt}
	}
	for _, row := range source.Favorites {
		userID, postID := userIDs[row.UserID], postIDs[row.PostID]
		if userID == 0 || postID == 0 {
			return rowFailure("invalid_favorite", "favorites", row.ID, "endpoint")
		}
		key := relationKey(int64String(userID), int64String(postID))
		if _, exists := result.Favorites[key]; exists {
			return rowFailure("duplicate_relation", "favorites", row.ID, "endpoint")
		}
		result.Favorites[key] = favoriteRow{SourceID: row.ID, UserID: userID, PostID: postID, CreatedAt: row.CreatedAt}
	}
	for _, row := range source.Likes {
		userID := userIDs[row.UserID]
		if userID == 0 {
			return rowFailure("missing_user", "likes", row.ID, "user_id")
		}
		switch row.Type {
		case "post":
			postID, exists := postIDs[row.TargetID]
			if !exists {
				addEvent(result, "OMIT", "likes", row.ID, "likeable_id", "orphan_target")
				continue
			}
			key := relationKey(int64String(userID), int64String(postID))
			result.PostLikes[key] = postLikeRow{SourceID: row.ID, UserID: userID, PostID: postID, CreatedAt: row.CreatedAt}
		case "comment":
			commentID, exists := commentIDs[row.TargetID]
			if !exists {
				addEvent(result, "OMIT", "likes", row.ID, "likeable_id", "orphan_target")
				continue
			}
			key := relationKey(int64String(userID), int64String(commentID))
			result.CommentLikes[key] = commentLikeRow{SourceID: row.ID, UserID: userID, CommentID: commentID, CreatedAt: row.CreatedAt}
		default:
			return rowFailure("unknown_like_type", "likes", row.ID, "likeable_type")
		}
	}
	result.Stats["follows"] = tableStat{SourceRows: len(source.Follows), TargetRows: len(result.Follows)}
	result.Stats["favorites"] = tableStat{SourceRows: len(source.Favorites), TargetRows: len(result.Favorites)}
	result.Stats["likes"] = tableStat{
		SourceRows: len(source.Likes), TargetRows: len(result.PostLikes) + len(result.CommentLikes),
		OmittedRows: len(source.Likes) - len(result.PostLikes) - len(result.CommentLikes),
	}
	return nil
}

func transformNotifications(source sourceData, userIDs, postIDs, commentIDs map[string]int64, result *dataset) error {
	for _, sourceRow := range source.Notifications {
		if !isUUID(sourceRow.ID) {
			return rowFailure("invalid_uuid", "notifications", sourceRow.ID, "id")
		}
		id := sourceRow.TargetID
		if id <= 0 {
			return rowFailure("invalid_sequential_id", "notifications", sourceRow.ID, "id")
		}
		row := notificationRow{
			SourceID: sourceRow.ID, ID: id, RecipientID: userIDs[sourceRow.RecipientID],
			SenderID: userIDs[sourceRow.SenderID], Type: sourceRow.Type, Content: sourceRow.Content,
			IsRead: sourceRow.IsRead, CreatedAt: sourceRow.CreatedAt, UpdatedAt: sourceRow.UpdatedAt,
		}
		if row.RecipientID == 0 || row.SenderID == 0 {
			return rowFailure("missing_user", "notifications", sourceRow.ID, "endpoint")
		}
		omit, err := resolveNotificationTarget(sourceRow, postIDs, commentIDs, &row)
		if err != nil {
			return err
		}
		if omit {
			addEvent(result, "OMIT", "notifications", sourceRow.ID, "related_id", "orphan_target")
			continue
		}
		if _, duplicate := result.Notifications[id]; duplicate {
			return rowFailure("deterministic_id_collision", "notifications", sourceRow.ID, "id")
		}
		result.Notifications[id] = row
	}
	result.Stats["notifications"] = tableStat{
		SourceRows: len(source.Notifications), TargetRows: len(result.Notifications),
		OmittedRows: len(source.Notifications) - len(result.Notifications),
	}
	return nil
}

func resolveNotificationTarget(sourceRow sourceNotification, postIDs, commentIDs map[string]int64, row *notificationRow) (bool, error) {
	switch sourceRow.Type {
	case "like_post", "comment":
		if sourceRow.RelatedID == nil || sourceRow.RelatedType == nil || *sourceRow.RelatedType != "post" {
			return false, rowFailure("invalid_notification_shape", "notifications", sourceRow.ID, "related_type")
		}
		id, exists := postIDs[*sourceRow.RelatedID]
		if !exists {
			return true, nil
		}
		row.RelatedPostID = int64Pointer(id)
	case "like_comment", "reply":
		if sourceRow.RelatedID == nil || sourceRow.RelatedType == nil || *sourceRow.RelatedType != "comment" {
			return false, rowFailure("invalid_notification_shape", "notifications", sourceRow.ID, "related_type")
		}
		id, exists := commentIDs[*sourceRow.RelatedID]
		if !exists {
			return true, nil
		}
		row.RelatedCommentID = int64Pointer(id)
	case "follow":
		if sourceRow.RelatedID != nil || sourceRow.RelatedType != nil {
			return false, rowFailure("invalid_notification_shape", "notifications", sourceRow.ID, "related_id")
		}
	default:
		return false, rowFailure("unknown_notification_type", "notifications", sourceRow.ID, "type")
	}
	needsContent := sourceRow.Type == "comment" || sourceRow.Type == "reply"
	if needsContent != (sourceRow.Content != nil) {
		return false, rowFailure("invalid_notification_shape", "notifications", sourceRow.ID, "content")
	}
	return false, nil
}

func validateImageReferences(imageIDs map[string]int64, result *dataset) error {
	for _, row := range result.PostImages {
		if _, exists := result.Images[row.ImageAssetID]; !exists {
			return rowFailure("missing_image", "post_images", row.SourceID, "image_asset_id")
		}
	}
	for sourceID, id := range imageIDs {
		if _, exists := result.Images[id]; !exists {
			return rowFailure("image_mapping_incomplete", "image_assets", sourceID, "id")
		}
	}
	return nil
}

func deriveCounters(result *dataset) {
	for _, like := range result.PostLikes {
		post := result.Posts[like.PostID]
		post.LikeCount++
		result.Posts[like.PostID] = post
	}
	for _, favorite := range result.Favorites {
		post := result.Posts[favorite.PostID]
		post.FavoriteCount++
		result.Posts[favorite.PostID] = post
	}
	for _, comment := range result.Comments {
		post := result.Posts[comment.PostID]
		post.CommentCount++
		result.Posts[comment.PostID] = post
		if comment.RootID != nil {
			root := result.Comments[*comment.RootID]
			root.ReplyCount++
			result.Comments[*comment.RootID] = root
		}
	}
	for _, like := range result.CommentLikes {
		comment := result.Comments[like.CommentID]
		comment.LikeCount++
		result.Comments[like.CommentID] = comment
	}
}

func populateDetailedStats(source sourceData, result *dataset) {
	managedRoles, inactiveUsers, postImageRefs, tagRefs, flavorRefs := 0, 0, 0, 0, 0
	for _, user := range source.Users {
		if user.Role != "user" {
			managedRoles++
		}
		if !user.IsActive {
			inactiveUsers++
		}
	}
	for _, post := range source.Posts {
		postImageRefs += len(post.Images)
		tagRefs += len(post.Tags)
		flavorRefs += len(post.Flavors)
		if post.Preferences != nil {
			flavorRefs += len(post.Preferences.PreferFlavors) + len(post.Preferences.AvoidFlavors)
		}
	}
	result.Stats["user_roles"] = tableStat{SourceRows: managedRoles, TargetRows: len(result.Roles)}
	result.Stats["user_role_records"] = tableStat{SourceRows: managedRoles, TargetRows: len(result.RoleRecords)}
	result.Stats["user_ban_records"] = tableStat{SourceRows: inactiveUsers, TargetRows: len(result.BanRecords)}
	result.Stats["tags"] = tableStat{SourceRows: len(result.Tags), TargetRows: len(result.Tags)}
	result.Stats["post_tags"] = tableStat{SourceRows: tagRefs, TargetRows: len(result.PostTags)}
	result.Stats["post_flavors"] = tableStat{SourceRows: flavorRefs, TargetRows: len(result.PostFlavors)}
	result.Stats["post_images"] = tableStat{
		SourceRows: postImageRefs, TargetRows: len(result.PostImages),
		OmittedRows: postImageRefs - len(result.PostImages),
	}
	result.Stats["post_likes"] = tableStat{SourceRows: len(result.PostLikes), TargetRows: len(result.PostLikes)}
	result.Stats["comment_likes"] = tableStat{SourceRows: len(result.CommentLikes), TargetRows: len(result.CommentLikes)}
	result.Stats["cuisines"] = tableStat{SourceRows: len(result.Cuisines), TargetRows: len(result.Cuisines)}
	result.Stats["flavors"] = tableStat{
		SourceRows: len(source.Flavors) + len(result.Flavors), TargetRows: len(source.Flavors) + len(result.Flavors),
	}
}

func validateDecisionCounts(source sourceData, result dataset) error {
	expectedTables := map[string]int{
		"users": 47, "posts": 33, "comments": 109, "likes": 109,
		"follows": 44, "favorites": 2, "notifications": 386, "image_assets": 40, "flavors": 16,
	}
	actualTables := map[string]int{
		"users": len(source.Users), "posts": len(source.Posts), "comments": len(source.Comments),
		"likes": len(source.Likes), "follows": len(source.Follows), "favorites": len(source.Favorites),
		"notifications": len(source.Notifications), "image_assets": len(source.Images), "flavors": len(source.Flavors),
	}
	for table, expected := range expectedTables {
		if actualTables[table] != expected {
			return rowFailure("source_count_unexpected", table, "count", "rows")
		}
	}
	expectedEvents := map[string]int{
		"unfinished_upload": 2, "placeholder_url": 8, "fake_avatar_to_null": 3,
		"orphan_target": 15, "target_field_absent": 11, "companion_to_seeking": 1,
		"root_reply_to_post_author": 37, "reply_parent_inferred": 2, "reply_to_parent_fallback": 1,
		"target_seed_order_authoritative": 16, "cuisine_seed_alias": 1, "cuisine_to_other": 1,
		"cuisine_historical_inactive": 3, "flavor_historical_inactive": 3,
		"flavor_seed_match": 3, "flavor_seed_alias": 1,
	}
	actualEvents := map[string]int{}
	for _, event := range result.Events {
		actualEvents[event.Code]++
	}
	for code, expected := range expectedEvents {
		if actualEvents[code] != expected {
			return rowFailure("decision_count_unexpected", "decision_events", code, "count")
		}
	}
	if len(result.Images) != 38 || len(result.PostImages) != 27 || len(result.PostLikes) != 51 ||
		len(result.CommentLikes) != 45 || len(result.Notifications) != 384 || len(result.Follows) != 44 ||
		len(result.Favorites) != 2 || len(result.Tags) != 9 || len(result.Cuisines) != 3 ||
		len(result.Flavors) != 3 || len(result.PostFlavors) != 7 {
		return rowFailure("target_count_unexpected", "decision_events", "count", "rows")
	}
	return nil
}

func addEvent(result *dataset, kind, table, sourceID, field, code string) {
	result.Events = append(result.Events, decisionEvent{Kind: kind, Table: table, SourceID: sourceID, Field: field, Code: code})
}

func isFakeAvatar(value string) bool {
	return value == "irure et" || strings.HasPrefix(value, "aute incididunt") || strings.HasPrefix(value, "laborum laboris")
}

func isPlaceholderImage(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "example.com" || host == "www.example.com" || host == "images.unsplash.com"
}

func int64Pointer(value int64) *int64    { return &value }
func stringPointer(value string) *string { return &value }

func int64String(value int64) string { return strconv.FormatInt(value, 10) }

func intString(value int) string {
	return strconv.Itoa(value)
}
