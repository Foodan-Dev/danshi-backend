package repository

import (
	"context"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm/clause"

	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
)

// PostFlavorRow 是批量预加载的帖子口味展示行。
type PostFlavorRow struct {
	PostID uint64
	Name   string
	Stance model.FlavorStance
}

// PostRelations 是一批帖子全部多值关系与当前用户互动状态。
type PostRelations struct {
	Tags      map[uint64][]string
	Flavors   map[uint64][]PostFlavorRow
	Images    map[uint64][]string
	Liked     map[uint64]bool
	Favorited map[uint64]bool
	Following map[uint64]bool
}

// LoadRelations 使用固定数量的批量查询预加载关联，查询数不随 page_size 增长。
func (PostRepository) LoadRelations(
	ctx context.Context,
	postIDs []uint64,
	authorIDs []uint64,
	currentUserID uint64,
) (PostRelations, error) {
	relations := PostRelations{
		Tags: make(map[uint64][]string), Flavors: make(map[uint64][]PostFlavorRow),
		Images: make(map[uint64][]string), Liked: make(map[uint64]bool),
		Favorited: make(map[uint64]bool), Following: make(map[uint64]bool),
	}
	if len(postIDs) == 0 {
		return relations, nil
	}
	if err := loadPostTags(ctx, postIDs, &relations); err != nil {
		return PostRelations{}, err
	}
	if err := loadPostFlavors(ctx, postIDs, &relations); err != nil {
		return PostRelations{}, err
	}
	if err := loadPostImages(ctx, postIDs, &relations); err != nil {
		return PostRelations{}, err
	}
	if currentUserID == 0 {
		return relations, nil
	}
	if err := loadPostInteractions(ctx, postIDs, currentUserID, &relations); err != nil {
		return PostRelations{}, err
	}
	if err := loadAuthorFollows(ctx, authorIDs, currentUserID, &relations); err != nil {
		return PostRelations{}, err
	}
	return relations, nil
}

// LoadSnapshotRelations 加载旧版本快照所需的完整当前关联。
// 与展示查询不同，这里包含已下架标签，因为关联仍然属于被替换的当前版本。
func (PostRepository) LoadSnapshotRelations(
	ctx context.Context,
	postID uint64,
) (PostRelations, error) {
	relations := PostRelations{
		Tags: make(map[uint64][]string), Flavors: make(map[uint64][]PostFlavorRow),
		Images: make(map[uint64][]string), Liked: make(map[uint64]bool),
		Favorited: make(map[uint64]bool), Following: make(map[uint64]bool),
	}
	postIDs := []uint64{postID}
	if err := loadPostTagsIncludingDeleted(ctx, postIDs, &relations); err != nil {
		return PostRelations{}, err
	}
	if err := loadPostFlavors(ctx, postIDs, &relations); err != nil {
		return PostRelations{}, err
	}
	if err := loadPostImages(ctx, postIDs, &relations); err != nil {
		return PostRelations{}, err
	}
	return relations, nil
}

// FindActiveCanteenByCode 解析可用于新写入的餐厅稳定 code。
func (PostRepository) FindActiveCanteenByCode(ctx context.Context, code string) (*model.Canteen, error) {
	var canteen model.Canteen
	err := db.FromContext(ctx).Where("code = ? AND is_active", code).First(&canteen).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &canteen, nil
}

// FindActiveWindow 返回可用于新写入的餐厅窗口。
func (PostRepository) FindActiveWindow(ctx context.Context, windowID uint64) (*model.CanteenWindow, error) {
	var window model.CanteenWindow
	err := db.FromContext(ctx).Where("id = ? AND is_active", windowID).First(&window).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &window, nil
}

// FindActiveCuisineByName 解析可用于新写入的菜系名称。
func (PostRepository) FindActiveCuisineByName(ctx context.Context, name string) (*model.Cuisine, error) {
	var cuisine model.Cuisine
	err := db.FromContext(ctx).Where("name = ? AND is_active", name).First(&cuisine).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &cuisine, nil
}

// FindActiveFlavorsByNames 批量解析可用于新写入的口味名称。
func (PostRepository) FindActiveFlavorsByNames(ctx context.Context, names []string) ([]model.Flavor, error) {
	if len(names) == 0 {
		return []model.Flavor{}, nil
	}
	var flavors []model.Flavor
	err := db.FromContext(ctx).Where("name IN ? AND is_active", names).Find(&flavors).Error
	return flavors, err
}

// FindImagesByURLs 把 API 的 URL 数组解析为资产行。
func (PostRepository) FindImagesByURLs(ctx context.Context, urls []string) ([]model.ImageAsset, error) {
	if len(urls) == 0 {
		return []model.ImageAsset{}, nil
	}
	var assets []model.ImageAsset
	err := db.FromContext(ctx).Where("public_url IN ?", urls).Find(&assets).Error
	return assets, err
}

// LockImagesByIDs 在增删引用前按 id 升序锁资产，所有写路径共用同一锁序。
func (PostRepository) LockImagesByIDs(ctx context.Context, imageIDs []uint64) ([]model.ImageAsset, error) {
	imageIDs = uniqueSortedIDs(imageIDs)
	if len(imageIDs) == 0 {
		return []model.ImageAsset{}, nil
	}
	var assets []model.ImageAsset
	err := db.FromContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ?", imageIDs).Order("id").Find(&assets).Error
	return assets, err
}

// PostImageIDs 返回帖子当前全部图片资产 id。
func (PostRepository) PostImageIDs(ctx context.Context, postID uint64) ([]uint64, error) {
	var ids []uint64
	err := db.FromContext(ctx).Model(&model.PostImage{}).Where("post_id = ?", postID).
		Order("position").Pluck("image_asset_id", &ids).Error
	return ids, err
}

// ImageIDsWithoutUndeletedPostReferences 返回已不再被任何未软删帖子引用的目标图片。
// 帖子状态不影响引用有效性；pending 等未软删帖子同样会阻止图片访问状态被收紧。
// 调用方必须先按 id 升序锁定全部目标资产，避免并发引用、编辑或下架漏掉状态收敛。
func (PostRepository) ImageIDsWithoutUndeletedPostReferences(
	ctx context.Context,
	imageIDs []uint64,
) ([]uint64, error) {
	imageIDs = uniqueSortedIDs(imageIDs)
	if len(imageIDs) == 0 {
		return []uint64{}, nil
	}
	var ids []uint64
	err := db.FromContext(ctx).Table("image_assets AS image").
		Where("image.id IN ?", imageIDs).
		Where(`NOT EXISTS (
			SELECT 1
			FROM post_images AS pi
			JOIN posts AS p ON p.id = pi.post_id
			WHERE pi.image_asset_id = image.id
			  AND p.deleted_at IS NULL
		)`).
		Order("image.id").Pluck("image.id", &ids).Error
	return ids, err
}

// ReplaceImages 物理替换图片关联；调用前必须锁定新旧资产全集。
func (PostRepository) ReplaceImages(ctx context.Context, postID uint64, imageIDs []uint64) error {
	if err := db.FromContext(ctx).Where("post_id = ?", postID).Delete(&model.PostImage{}).Error; err != nil {
		return err
	}
	if len(imageIDs) == 0 {
		return nil
	}
	rows := make([]model.PostImage, 0, len(imageIDs))
	for position, imageID := range imageIDs {
		rows = append(rows, model.PostImage{PostID: postID, Position: int16(position), ImageAssetID: imageID})
	}
	return db.FromContext(ctx).Create(&rows).Error
}

// ReplaceFlavors 物理替换帖子口味关联。
func (PostRepository) ReplaceFlavors(
	ctx context.Context,
	postID uint64,
	postType model.PostType,
	flavors []model.Flavor,
	stances map[string]model.FlavorStance,
) error {
	if err := db.FromContext(ctx).Where("post_id = ?", postID).Delete(&model.PostFlavor{}).Error; err != nil {
		return err
	}
	if len(flavors) == 0 {
		return nil
	}
	rows := make([]model.PostFlavor, 0, len(flavors))
	for _, flavor := range flavors {
		rows = append(rows, model.PostFlavor{
			PostID: postID, FlavorID: flavor.ID, Stance: stances[flavor.Name], PostType: postType,
		})
	}
	return db.FromContext(ctx).Create(&rows).Error
}

// FindOrCreateTag 按大小写不敏感唯一规则复用或创建标签。
func (PostRepository) FindOrCreateTag(ctx context.Context, name string) (*model.Tag, bool, error) {
	var tag model.Tag
	result := db.FromContext(ctx).Raw(`
		INSERT INTO tags (name) VALUES (?)
		ON CONFLICT DO NOTHING
		RETURNING id, name, created_at, updated_at, moderation, deleted_at
	`, name).Scan(&tag)
	if result.Error != nil {
		return nil, false, result.Error
	}
	if result.RowsAffected == 1 {
		return &tag, true, nil
	}
	err := db.FromContext(ctx).Where("lower(name) = lower(?)", name).First(&tag).Error
	if err != nil {
		return nil, false, NormalizeError(err)
	}
	return &tag, false, nil
}

// ReplaceTags 物理替换帖子与标签关联。
func (PostRepository) ReplaceTags(ctx context.Context, postID uint64, tagIDs []uint64) error {
	if err := db.FromContext(ctx).Where("post_id = ?", postID).Delete(&model.PostTag{}).Error; err != nil {
		return err
	}
	if len(tagIDs) == 0 {
		return nil
	}
	rows := make([]model.PostTag, 0, len(tagIDs))
	for _, tagID := range tagIDs {
		rows = append(rows, model.PostTag{PostID: postID, TagID: tagID})
	}
	return db.FromContext(ctx).Create(&rows).Error
}

// SetTagModeration 写回新标签的先发后审结论。
func (PostRepository) SetTagModeration(
	ctx context.Context,
	tagID uint64,
	status model.ModerationStatus,
	deletedAt *time.Time,
) error {
	return db.FromContext(ctx).Model(&model.Tag{}).Where("id = ?", tagID).Updates(map[string]any{
		"moderation": status, "deleted_at": deletedAt,
	}).Error
}

// CreateModerationRecord 追加不可篡改的审核流水。
func (PostRepository) CreateModerationRecord(ctx context.Context, record *model.ModerationRecord) error {
	return db.FromContext(ctx).Create(record).Error
}

// Like 幂等创建点赞动作，并返回本次是否真的插入了新行。
func (PostRepository) Like(ctx context.Context, userID, postID uint64) (bool, error) {
	result := db.FromContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
		Create(&model.PostLike{UserID: userID, PostID: postID})
	return result.RowsAffected == 1, result.Error
}

// Unlike 幂等物理删除点赞动作。
func (PostRepository) Unlike(ctx context.Context, userID, postID uint64) error {
	return DeleteAssociation(ctx, &model.PostLike{UserID: userID, PostID: postID})
}

// Favorite 幂等创建收藏动作，计数器由数据库触发器维护。
func (PostRepository) Favorite(ctx context.Context, userID, postID uint64) error {
	return UpsertAssociation(ctx, &model.Favorite{UserID: userID, PostID: postID})
}

// Unfavorite 幂等物理删除收藏动作。
func (PostRepository) Unfavorite(ctx context.Context, userID, postID uint64) error {
	return DeleteAssociation(ctx, &model.Favorite{UserID: userID, PostID: postID})
}

// Counters 读取触发器更新后的帖子计数。
func (PostRepository) Counters(ctx context.Context, postID uint64) (int32, int32, error) {
	var row struct {
		LikeCount     int32
		FavoriteCount int32
	}
	result := db.FromContext(ctx).Model(&model.Post{}).Select("like_count, favorite_count").
		Where("id = ?", postID).Scan(&row)
	if result.Error != nil {
		return 0, 0, result.Error
	}
	if result.RowsAffected == 0 {
		return 0, 0, ErrNotFound
	}
	return row.LikeCount, row.FavoriteCount, nil
}

func loadPostTags(ctx context.Context, postIDs []uint64, relations *PostRelations) error {
	return loadPostTagRows(ctx, postIDs, relations, true)
}

func loadPostTagsIncludingDeleted(
	ctx context.Context,
	postIDs []uint64,
	relations *PostRelations,
) error {
	return loadPostTagRows(ctx, postIDs, relations, false)
}

func loadPostTagRows(
	ctx context.Context,
	postIDs []uint64,
	relations *PostRelations,
	onlyVisible bool,
) error {
	var rows []struct {
		PostID uint64
		Name   string
	}
	query := db.FromContext(ctx).Table("post_tags AS pt").Select("pt.post_id, t.name").
		Joins("JOIN tags AS t ON t.id = pt.tag_id").Where("pt.post_id IN ?", postIDs)
	if onlyVisible {
		query = query.Where("t.deleted_at IS NULL")
	}
	err := query.Order("pt.post_id, lower(t.name), t.id").Scan(&rows).Error
	for _, row := range rows {
		relations.Tags[row.PostID] = append(relations.Tags[row.PostID], row.Name)
	}
	return err
}

func loadPostFlavors(ctx context.Context, postIDs []uint64, relations *PostRelations) error {
	var rows []PostFlavorRow
	err := db.FromContext(ctx).Table("post_flavors AS pf").
		Select("pf.post_id, f.name, pf.stance").Joins("JOIN flavors AS f ON f.id = pf.flavor_id").
		Where("pf.post_id IN ?", postIDs).Order("pf.post_id, f.sort_order, f.id").Scan(&rows).Error
	for _, row := range rows {
		relations.Flavors[row.PostID] = append(relations.Flavors[row.PostID], row)
	}
	return err
}

func loadPostImages(ctx context.Context, postIDs []uint64, relations *PostRelations) error {
	var rows []struct {
		PostID    uint64
		PublicURL string
	}
	err := db.FromContext(ctx).Table("post_images AS pi").
		Select("pi.post_id, a.public_url").Joins("JOIN image_assets AS a ON a.id = pi.image_asset_id").
		Where("pi.post_id IN ?", postIDs).Order("pi.post_id, pi.position").Scan(&rows).Error
	for _, row := range rows {
		relations.Images[row.PostID] = append(relations.Images[row.PostID], row.PublicURL)
	}
	return err
}

func loadPostInteractions(
	ctx context.Context,
	postIDs []uint64,
	userID uint64,
	relations *PostRelations,
) error {
	var liked []uint64
	if err := db.FromContext(ctx).Model(&model.PostLike{}).
		Where("user_id = ? AND post_id IN ?", userID, postIDs).Pluck("post_id", &liked).Error; err != nil {
		return err
	}
	for _, postID := range liked {
		relations.Liked[postID] = true
	}
	var favorites []uint64
	if err := db.FromContext(ctx).Model(&model.Favorite{}).
		Where("user_id = ? AND post_id IN ?", userID, postIDs).Pluck("post_id", &favorites).Error; err != nil {
		return err
	}
	for _, postID := range favorites {
		relations.Favorited[postID] = true
	}
	return nil
}

func loadAuthorFollows(
	ctx context.Context,
	authorIDs []uint64,
	userID uint64,
	relations *PostRelations,
) error {
	authorIDs = uniqueSortedIDs(authorIDs)
	if len(authorIDs) == 0 {
		return nil
	}
	var following []uint64
	err := db.FromContext(ctx).Model(&model.Follow{}).
		Where("follower_id = ? AND following_id IN ?", userID, authorIDs).
		Pluck("following_id", &following).Error
	for _, authorID := range following {
		relations.Following[authorID] = true
	}
	return err
}

func uniqueSortedIDs(ids []uint64) []uint64 {
	set := make(map[uint64]struct{}, len(ids))
	for _, id := range ids {
		if id != 0 {
			set[id] = struct{}{}
		}
	}
	result := make([]uint64, 0, len(set))
	for id := range set {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

// CanonicalNames 去重并返回稳定顺序，供 service 在快照与关联写入之间共享。
func CanonicalNames(names []string) []string {
	seen := make(map[string]struct{}, len(names))
	result := make([]string, 0, len(names))
	for _, name := range names {
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, name)
	}
	return result
}
