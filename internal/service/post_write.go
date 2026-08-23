package service

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

type resolvedPostPayload struct {
	normalizedPostPayload
	CanteenID   *uint64
	CuisineID   *uint64
	Flavors     []model.Flavor
	ImageAssets []model.ImageAsset
	ImageIDs    []uint64
}

type postSnapshot struct {
	PostType        model.PostType     `json:"post_type"`
	ShareType       *model.ShareType   `json:"share_type"`
	Title           string             `json:"title"`
	Content         string             `json:"content"`
	Category        model.PostCategory `json:"category"`
	CanteenID       *uint64            `json:"canteen_id"`
	CanteenWindowID *uint64            `json:"canteen_window_id"`
	CuisineID       *uint64            `json:"cuisine_id"`
	Price           *string            `json:"price"`
	BudgetMin       *int32             `json:"budget_min"`
	BudgetMax       *int32             `json:"budget_max"`
	Tags            []string           `json:"tags"`
	Flavors         []snapshotFlavor   `json:"flavors"`
	Images          []string           `json:"images"`
}

type snapshotFlavor struct {
	Name   string             `json:"name"`
	Stance model.FlavorStance `json:"stance"`
}

// Create 在一个 UoW 内创建主体、全部关联与审核流水；首次创建不写编辑历史。
func (s *PostService) Create(
	ctx context.Context,
	input CreatePostInput,
	authorID uint64,
) (*PostCreateResult, error) {
	normalized, err := normalizeCreatePost(input)
	if err != nil {
		return nil, err
	}
	resolved, err := s.resolvePostPayload(ctx, normalized, authorID, nil)
	if err != nil {
		return nil, err
	}
	initialStatus := model.PostStatusPending
	if !resolved.Publish {
		initialStatus = model.PostStatusDraft
	}
	post := newPostModel(resolved, authorID, initialStatus)
	if err := s.posts.Create(ctx, post); err != nil {
		return nil, apierr.Internal(err)
	}
	if err := s.applyPostRelations(ctx, post.ID, &resolved); err != nil {
		return nil, apierr.Internal(err)
	}
	status, err := s.finishModeration(ctx, post.ID, resolved)
	if err != nil {
		return nil, err
	}
	if err := s.posts.UpdateStatus(ctx, post.ID, status); err != nil {
		return nil, apierr.Internal(err)
	}
	return &PostCreateResult{ID: post.ID, PostType: post.PostType, Status: status}, nil
}

// Update 串行化同帖编辑，先追加被替换的当前版本，再更新主体与关联。
func (s *PostService) Update(
	ctx context.Context,
	postID uint64,
	input UpdatePostInput,
	authorID uint64,
) (*PostCreateResult, error) {
	post, err := s.posts.LockByID(ctx, postID, repository.QueryOptions{IncludeDeleted: true})
	if err != nil {
		return nil, postRepositoryError(err)
	}
	if post.DeletedAt != nil {
		return nil, apierr.NotFound(apierr.BizPostDeleted, "帖子")
	}
	if post.AuthorID != authorID {
		return nil, apierr.Forbidden(apierr.BizNotOwner, "只能修改自己的帖子")
	}
	normalized, err := normalizeUpdatePost(input, post)
	if err != nil {
		return nil, err
	}
	oldImageIDs, err := s.posts.PostImageIDs(ctx, postID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	resolved, err := s.resolvePostPayload(ctx, normalized, authorID, oldImageIDs)
	if err != nil {
		return nil, err
	}
	initialStatus := model.PostStatusPending
	if !resolved.Publish {
		initialStatus = model.PostStatusDraft
	}
	now := time.Now().UTC()
	if err := s.appendCurrentPostHistory(ctx, post, authorID, resolved.EditReason, now); err != nil {
		return nil, historyWriteError(err)
	}
	if err := s.posts.UpdateContent(ctx, postID, contentFields(resolved, initialStatus, now)); err != nil {
		return nil, postRepositoryError(err)
	}
	if err := s.applyPostRelations(ctx, post.ID, &resolved); err != nil {
		return nil, apierr.Internal(err)
	}
	status, err := s.finishModeration(ctx, postID, resolved)
	if err != nil {
		return nil, err
	}
	if err := s.posts.UpdateStatus(ctx, postID, status); err != nil {
		return nil, apierr.Internal(err)
	}
	return &PostCreateResult{ID: postID, PostType: post.PostType, Status: status}, nil
}

// Delete 软删除帖子，并在解除图片引用前锁定全部目标资产。
func (s *PostService) Delete(ctx context.Context, postID, authorID uint64) error {
	post, err := s.posts.LockByID(ctx, postID, repository.QueryOptions{IncludeDeleted: true})
	if err != nil {
		return postRepositoryError(err)
	}
	if post.DeletedAt != nil {
		return apierr.NotFound(apierr.BizPostDeleted, "帖子")
	}
	if post.AuthorID != authorID {
		return apierr.Forbidden(apierr.BizNotOwner, "只能删除自己的帖子")
	}
	imageIDs, err := s.posts.PostImageIDs(ctx, postID)
	if err != nil {
		return apierr.Internal(err)
	}
	if _, err := s.posts.LockImagesByIDs(ctx, imageIDs); err != nil {
		return apierr.Internal(err)
	}
	if err := s.posts.ReplaceImages(ctx, postID, nil); err != nil {
		return apierr.Internal(err)
	}
	if err := s.posts.SoftDelete(ctx, postID, authorID, time.Now().UTC()); err != nil {
		return postRepositoryError(err)
	}
	return nil
}

func (s *PostService) applyPostRelations(
	ctx context.Context,
	postID uint64,
	resolved *resolvedPostPayload,
) error {
	tagIDs, canonicalTags, err := s.replaceTags(ctx, resolved.Tags)
	if err != nil {
		return err
	}
	resolved.Tags = canonicalTags
	if err := s.posts.ReplaceTags(ctx, postID, tagIDs); err != nil {
		return err
	}
	if err := s.posts.ReplaceFlavors(
		ctx, postID, resolved.PostType, resolved.Flavors, resolved.FlavorStances,
	); err != nil {
		return err
	}
	if err := s.posts.ReplaceImages(ctx, postID, resolved.ImageIDs); err != nil {
		return err
	}
	return nil
}

func (s *PostService) appendCurrentPostHistory(
	ctx context.Context,
	post *model.Post,
	editorID uint64,
	editReason *string,
	editedAt time.Time,
) error {
	relations, err := s.posts.LoadSnapshotRelations(ctx, post.ID)
	if err != nil {
		return err
	}
	snapshot, err := json.Marshal(snapshotFromCurrent(post, relations))
	if err != nil {
		return err
	}
	revision, err := s.posts.NextHistoryRevision(ctx, post.ID)
	if err != nil {
		return err
	}
	history := &model.PostHistory{
		PostID: post.ID, Revision: revision, EditedBy: editorID, EditedAt: editedAt,
		Snapshot: snapshot, EditReason: editReason,
	}
	return s.posts.CreateHistory(ctx, history)
}

func (s *PostService) finishModeration(
	ctx context.Context,
	postID uint64,
	resolved resolvedPostPayload,
) (model.PostStatus, error) {
	if !resolved.Publish {
		return model.PostStatusDraft, nil
	}
	result, err := s.moderator.Review(ctx, ModerationRequest{
		Target: ModerationTargetPost, Text: resolved.Title + "\n" + resolved.Content,
	})
	if err != nil {
		return "", err
	}
	if err := validateModerationResult(result); err != nil {
		return "", err
	}
	record := moderationRecordForPost(postID, result)
	if err := s.posts.CreateModerationRecord(ctx, record); err != nil {
		return "", apierr.Internal(err)
	}
	if result.Verdict != model.ModerationVerdictPass {
		s.alerter.Alert(ctx, ModerationAlert{
			Target: ModerationTargetPost, TargetID: postID, Provider: result.Provider,
			ProviderJobID: result.ProviderJobID, Verdict: result.Verdict,
			Labels: append([]string{}, result.Labels...),
		})
	}
	switch result.Verdict {
	case model.ModerationVerdictBlock:
		return model.PostStatusRejected, nil
	case model.ModerationVerdictReview:
		return model.PostStatusPending, nil
	case model.ModerationVerdictPass:
		if imagesApproved(resolved.ImageAssets) {
			return model.PostStatusApproved, nil
		}
		return model.PostStatusPending, nil
	default:
		return "", apierr.Internal(errors.New("未知的审核结论"))
	}
}

func (s *PostService) replaceTags(ctx context.Context, names []string) ([]uint64, []string, error) {
	tagIDs := make([]uint64, 0, len(names))
	canonicalNames := make([]string, 0, len(names))
	for _, name := range names {
		tag, created, err := s.posts.FindOrCreateTag(ctx, name)
		if err != nil {
			return nil, nil, err
		}
		if tag.DeletedAt != nil {
			return nil, nil, apierr.Conflict(apierr.BizContentRejected, "包含已下架标签")
		}
		if created {
			if err := s.moderateTag(ctx, tag); err != nil {
				return nil, nil, err
			}
		}
		tagIDs = append(tagIDs, tag.ID)
		canonicalNames = append(canonicalNames, tag.Name)
	}
	return tagIDs, canonicalNames, nil
}

func (s *PostService) moderateTag(ctx context.Context, tag *model.Tag) error {
	result, err := s.moderator.Review(ctx, ModerationRequest{Target: ModerationTargetTag, Text: tag.Name})
	if err != nil {
		return err
	}
	if err := validateModerationResult(result); err != nil {
		return err
	}
	status := model.ModerationStatus(result.Verdict)
	var deletedAt *time.Time
	if result.Verdict == model.ModerationVerdictBlock {
		now := time.Now().UTC()
		deletedAt = &now
	}
	if err := s.posts.SetTagModeration(ctx, tag.ID, status, deletedAt); err != nil {
		return apierr.Internal(err)
	}
	tagID := tag.ID
	record := moderationRecordForTarget(nil, &tagID, result)
	if err := s.posts.CreateModerationRecord(ctx, record); err != nil {
		return apierr.Internal(err)
	}
	if result.Verdict != model.ModerationVerdictPass {
		s.alerter.Alert(ctx, ModerationAlert{
			Target: ModerationTargetTag, TargetID: tag.ID, Provider: result.Provider,
			ProviderJobID: result.ProviderJobID, Verdict: result.Verdict,
			Labels: append([]string{}, result.Labels...),
		})
	}
	return nil
}

func (s *PostService) resolvePostPayload(
	ctx context.Context,
	normalized normalizedPostPayload,
	authorID uint64,
	oldImageIDs []uint64,
) (resolvedPostPayload, error) {
	resolved := resolvedPostPayload{normalizedPostPayload: normalized}
	if err := s.resolveDictionaries(ctx, &resolved); err != nil {
		return resolvedPostPayload{}, err
	}
	assets, err := s.posts.FindImagesByURLs(ctx, resolved.Images)
	if err != nil {
		return resolvedPostPayload{}, apierr.Internal(err)
	}
	ids := append([]uint64{}, oldImageIDs...)
	for _, asset := range assets {
		ids = append(ids, asset.ID)
	}
	locked, err := s.posts.LockImagesByIDs(ctx, ids)
	if err != nil {
		return resolvedPostPayload{}, apierr.Internal(err)
	}
	ordered, imageIDs, err := validateAndOrderImages(resolved.Images, locked, authorID)
	if err != nil {
		return resolvedPostPayload{}, err
	}
	resolved.ImageAssets = ordered
	resolved.ImageIDs = imageIDs
	return resolved, nil
}

func (s *PostService) resolveDictionaries(ctx context.Context, resolved *resolvedPostPayload) error {
	if resolved.CanteenCode != nil {
		canteen, err := s.posts.FindActiveCanteenByCode(ctx, *resolved.CanteenCode)
		if err != nil {
			return dictionaryError(err, "餐厅")
		}
		resolved.CanteenID = &canteen.ID
	}
	if resolved.CanteenWindowID != nil {
		window, err := s.posts.FindActiveWindow(ctx, *resolved.CanteenWindowID)
		if err != nil {
			return dictionaryError(err, "窗口")
		}
		if resolved.CanteenID == nil || window.CanteenID != *resolved.CanteenID {
			return apierr.Conflict(apierr.BizWindowNotInCanteen, "窗口不属于所选餐厅")
		}
	}
	if resolved.Cuisine != nil {
		cuisine, err := s.posts.FindActiveCuisineByName(ctx, *resolved.Cuisine)
		if err != nil {
			return dictionaryError(err, "菜系")
		}
		resolved.CuisineID = &cuisine.ID
	}
	flavors, err := s.posts.FindActiveFlavorsByNames(ctx, resolved.FlavorNames)
	if err != nil {
		return apierr.Internal(err)
	}
	byName := make(map[string]model.Flavor, len(flavors))
	for _, flavor := range flavors {
		byName[flavor.Name] = flavor
	}
	for _, name := range resolved.FlavorNames {
		flavor, exists := byName[name]
		if !exists {
			return apierr.NotFound(apierr.BizDictItemNotFound, "口味")
		}
		resolved.Flavors = append(resolved.Flavors, flavor)
	}
	return nil
}

func validateAndOrderImages(
	urls []string,
	assets []model.ImageAsset,
	authorID uint64,
) ([]model.ImageAsset, []uint64, error) {
	byURL := make(map[string]model.ImageAsset, len(assets))
	for _, asset := range assets {
		byURL[asset.PublicURL] = asset
	}
	ordered := make([]model.ImageAsset, 0, len(urls))
	ids := make([]uint64, 0, len(urls))
	for _, url := range urls {
		asset, exists := byURL[url]
		if !exists || model.IsPurgedImageURL(asset.PublicURL) {
			return nil, nil, apierr.NotFound(apierr.BizImageNotFound, "图片")
		}
		if asset.UploaderID == nil || *asset.UploaderID != authorID {
			return nil, nil, apierr.Forbidden(apierr.BizImageNotOwned, "只能引用自己上传的图片")
		}
		if asset.Purpose != model.ImagePurposePost {
			return nil, nil, apierr.Conflict(apierr.BizImagePurposeWrong, "图片用途不是帖子配图")
		}
		if asset.Moderation == model.ModerationStatusBlock {
			return nil, nil, apierr.Conflict(apierr.BizImageNotApproved, "图片未通过审核")
		}
		ordered = append(ordered, asset)
		ids = append(ids, asset.ID)
	}
	return ordered, ids, nil
}

func newPostModel(
	resolved resolvedPostPayload,
	authorID uint64,
	status model.PostStatus,
) *model.Post {
	post := &model.Post{
		AuthorID: authorID, PostType: resolved.PostType, ShareType: resolved.ShareType,
		Status: status, Category: resolved.Category, Title: resolved.Title, Content: resolved.Content,
		CanteenID: resolved.CanteenID, CanteenWindowID: resolved.CanteenWindowID,
		CuisineID: resolved.CuisineID,
	}
	if resolved.Price != nil {
		price := resolved.Price.Decimal()
		post.Price = &price
	}
	post.BudgetMin, post.BudgetMax = budgetColumns(resolved.BudgetRange)
	return post
}

func contentFields(
	resolved resolvedPostPayload,
	status model.PostStatus,
	now time.Time,
) map[string]any {
	budgetMin, budgetMax := budgetColumns(resolved.BudgetRange)
	fields := map[string]any{
		"share_type": resolved.ShareType, "status": status, "category": resolved.Category,
		"title": resolved.Title, "content": resolved.Content, "canteen_id": resolved.CanteenID,
		"canteen_window_id": resolved.CanteenWindowID, "cuisine_id": resolved.CuisineID,
		"budget_min": budgetMin, "budget_max": budgetMax, "updated_at": now,
	}
	if resolved.Price == nil {
		fields["price"] = nil
	} else {
		fields["price"] = resolved.Price.Decimal()
	}
	return fields
}

func snapshotFromCurrent(post *model.Post, relations repository.PostRelations) postSnapshot {
	snapshot := postSnapshot{
		PostType: post.PostType, ShareType: post.ShareType, Title: post.Title, Content: post.Content,
		Category: post.Category, CanteenID: post.CanteenID, CanteenWindowID: post.CanteenWindowID,
		CuisineID: post.CuisineID, Tags: snapshotTags(relations.Tags[post.ID]),
		Images: nonNilStrings(relations.Images[post.ID]), Flavors: []snapshotFlavor{},
	}
	if post.Price != nil {
		price := post.Price.StringFixed(2)
		snapshot.Price = &price
	}
	snapshot.BudgetMin, snapshot.BudgetMax = post.BudgetMin, post.BudgetMax
	for _, flavor := range relations.Flavors[post.ID] {
		snapshot.Flavors = append(snapshot.Flavors, snapshotFlavor{Name: flavor.Name, Stance: flavor.Stance})
	}
	return snapshot
}

func snapshotTags(values []string) []string {
	tags := append([]string{}, values...)
	sort.Slice(tags, func(i, j int) bool {
		left, right := strings.ToLower(tags[i]), strings.ToLower(tags[j])
		if left == right {
			return tags[i] < tags[j]
		}
		return left < right
	})
	return nonNilStrings(tags)
}

func budgetColumns(value *BudgetRangeInput) (*int32, *int32) {
	if value == nil {
		return nil, nil
	}
	minimum, maximum := value.Min, value.Max
	return &minimum, &maximum
}

func moderationRecordForPost(
	postID uint64,
	result ModerationResult,
) *model.ModerationRecord {
	return moderationRecordForTarget(&postID, nil, result)
}

func moderationRecordForTarget(
	postID *uint64,
	tagID *uint64,
	result ModerationResult,
) *model.ModerationRecord {
	labels := pq.StringArray(result.Labels)
	if labels == nil {
		labels = pq.StringArray{}
	}
	return &model.ModerationRecord{
		PostID: postID, TagID: tagID,
		Scene: model.ModerationSceneText, Provider: result.Provider,
		ProviderJobID: result.ProviderJobID, Verdict: result.Verdict,
		Labels: labels, Score: result.Score, RawResponse: result.RawResponse,
		CreatedAt: time.Now().UTC(),
	}
}

func validateModerationResult(result ModerationResult) error {
	if strings.TrimSpace(string(result.Provider)) == "" {
		return apierr.Internal(errors.New("审核供应商返回了空 provider"))
	}
	if result.Verdict != model.ModerationVerdictPass &&
		result.Verdict != model.ModerationVerdictReview &&
		result.Verdict != model.ModerationVerdictBlock {
		return apierr.Internal(errors.New("审核供应商返回了未知 verdict"))
	}
	return nil
}

func imagesApproved(images []model.ImageAsset) bool {
	for _, image := range images {
		if image.Moderation != model.ModerationStatusPass {
			return false
		}
	}
	return true
}

func dictionaryError(err error, resource string) error {
	if errors.Is(err, repository.ErrNotFound) {
		return apierr.NotFound(apierr.BizDictItemNotFound, resource)
	}
	return apierr.Internal(err)
}

func historyWriteError(err error) error {
	if repository.IsUniqueViolation(err, "uq_post_histories_revision") {
		return apierr.Conflict(apierr.BizConflict, "帖子版本冲突，请重试")
	}
	return apierr.Internal(err)
}
