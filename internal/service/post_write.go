package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/money"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

const historyRestoreProvider model.ModerationProvider = "history_restore"

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
	status, err := s.finishModeration(ctx, post.ID, 1, resolved)
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
	contentRevision, err := s.replacePostContentVersion(
		ctx, post, authorID, resolved.EditReason, resolved, initialStatus, now,
	)
	if err != nil {
		return nil, err
	}
	status, err := s.finishModeration(ctx, postID, contentRevision, resolved)
	if err != nil {
		return nil, err
	}
	if err := s.posts.UpdateStatus(ctx, postID, status); err != nil {
		return nil, apierr.Internal(err)
	}
	return &PostCreateResult{ID: postID, PostType: post.PostType, Status: status}, nil
}

// Delete 以作者来源软删除帖子，保留关联并处置失去全部未删除帖子引用的图片。
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
	return deletePostWithHistoryAndImages(
		ctx, s.posts, s.moderation, post, authorID, model.DeleteReasonAuthor, time.Now().UTC(),
	)
}

// RestoreHistory 把指定旧快照写成新的当前版本；有审核结论时继承，否则重新送审。
func (s *PostService) RestoreHistory(
	ctx context.Context,
	postID uint64,
	revision int32,
	input RestorePostHistoryInput,
	authorID uint64,
) (*PostCreateResult, error) {
	post, err := s.posts.LockByID(ctx, postID, repository.QueryOptions{IncludeDeleted: true})
	if err != nil {
		return nil, postRepositoryError(err)
	}
	if post.AuthorID != authorID {
		return nil, apierr.Forbidden(apierr.BizNotOwner, "只能恢复自己的帖子历史版本")
	}
	history, err := s.posts.FindHistory(ctx, postID, revision)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apierr.NotFound(apierr.BizNotFound, "帖子历史版本")
		}
		return nil, apierr.Internal(err)
	}
	sourceModeration, err := s.posts.LatestPostModerationForRevision(ctx, postID, revision)
	if err != nil {
		if !errors.Is(err, repository.ErrNotFound) {
			return nil, apierr.Internal(err)
		}
		sourceModeration = nil
	}
	updateInput, snapshot, err := s.updateInputFromSnapshot(ctx, history.Snapshot, input.EditReason)
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeUpdatePost(updateInput, post)
	if err != nil {
		return nil, err
	}
	oldImageIDs, err := s.posts.PostImageIDs(ctx, postID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if post.DeletedAt != nil {
		if err := s.restoreDeletedSnapshotImages(
			ctx, postID, authorID, snapshot.Images, oldImageIDs,
		); err != nil {
			return nil, err
		}
	}
	resolved, err := s.resolvePostPayload(ctx, normalized, authorID, oldImageIDs)
	if err != nil {
		return nil, err
	}
	status := model.PostStatusPending
	if !resolved.Publish {
		status = model.PostStatusDraft
	}
	if sourceModeration != nil {
		status, err = aggregatePostModeration(sourceModeration.Verdict, resolved.ImageAssets)
		if err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	contentRevision, err := s.replacePostContentVersion(
		ctx, post, authorID, resolved.EditReason, resolved, status, now,
	)
	if err != nil {
		return nil, err
	}
	if sourceModeration == nil {
		status, err = s.reviewRestoredPost(ctx, postID, contentRevision, resolved)
	} else {
		err = s.inheritRestoredPostModeration(
			ctx, postID, contentRevision, revision, sourceModeration, now,
		)
	}
	if err != nil {
		return nil, err
	}
	if post.DeletedAt != nil {
		if err := s.posts.Restore(ctx, postID); err != nil {
			return nil, postRepositoryError(err)
		}
	}
	return &PostCreateResult{ID: postID, PostType: post.PostType, Status: status}, nil
}

func (s *PostService) reviewRestoredPost(
	ctx context.Context,
	postID uint64,
	contentRevision int32,
	resolved resolvedPostPayload,
) (model.PostStatus, error) {
	status, err := s.finishModeration(ctx, postID, contentRevision, resolved)
	if err != nil {
		return "", err
	}
	if err := s.posts.UpdateStatus(ctx, postID, status); err != nil {
		return "", apierr.Internal(err)
	}
	return status, nil
}

func (s *PostService) inheritRestoredPostModeration(
	ctx context.Context,
	postID uint64,
	contentRevision int32,
	sourceRevision int32,
	source *model.ModerationRecord,
	now time.Time,
) error {
	record, err := inheritedPostModerationRecord(
		postID, contentRevision, sourceRevision, source, now,
	)
	if err != nil {
		return apierr.Internal(err)
	}
	if err := s.posts.CreateModerationRecord(ctx, record); err != nil {
		return apierr.Internal(err)
	}
	return nil
}

// replacePostContentVersion 是普通编辑与历史回退共用的内容写入路径。
// 调用方必须已经锁定帖子主体；本函数先保存被替换版本，再重建主体与全部关联。
func (s *PostService) replacePostContentVersion(
	ctx context.Context,
	post *model.Post,
	editorID uint64,
	editReason *string,
	resolved resolvedPostPayload,
	status model.PostStatus,
	now time.Time,
) (int32, error) {
	previousRevision, err := appendCurrentPostHistory(
		ctx, s.posts, post, editorID, editReason, now,
	)
	if err != nil {
		return 0, historyWriteError(err)
	}
	if err := s.posts.UpdateContent(ctx, post.ID, contentFields(resolved, status, now)); err != nil {
		return 0, postRepositoryError(err)
	}
	if err := s.applyPostRelations(ctx, post.ID, &resolved); err != nil {
		return 0, apierr.Internal(err)
	}
	return previousRevision + 1, nil
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

func appendCurrentPostHistory(
	ctx context.Context,
	posts repository.PostRepository,
	post *model.Post,
	editorID uint64,
	editReason *string,
	editedAt time.Time,
) (int32, error) {
	relations, err := posts.LoadSnapshotRelations(ctx, post.ID)
	if err != nil {
		return 0, err
	}
	snapshot, err := json.Marshal(snapshotFromCurrent(post, relations))
	if err != nil {
		return 0, err
	}
	revision, err := posts.NextHistoryRevision(ctx, post.ID)
	if err != nil {
		return 0, err
	}
	history := &model.PostHistory{
		PostID: post.ID, Revision: revision, EditedBy: editorID, EditedAt: editedAt,
		Snapshot: snapshot, EditReason: editReason,
	}
	if err := posts.CreateHistory(ctx, history); err != nil {
		return 0, err
	}
	return revision, nil
}

func deletePostWithHistoryAndImages(
	ctx context.Context,
	posts repository.PostRepository,
	moderation *ModerationService,
	post *model.Post,
	actorID uint64,
	reason model.DeleteReason,
	now time.Time,
) error {
	imageIDs, err := posts.PostImageIDs(ctx, post.ID)
	if err != nil {
		return apierr.Internal(err)
	}
	assets, err := posts.LockImagesByIDs(ctx, imageIDs)
	if err != nil {
		return apierr.Internal(err)
	}
	if _, err := appendCurrentPostHistory(ctx, posts, post, actorID, nil, now); err != nil {
		return historyWriteError(err)
	}
	if err := posts.SoftDelete(ctx, post.ID, actorID, reason, now); err != nil {
		return postRepositoryError(err)
	}
	imageIDsToBlock, err := posts.ImageIDsWithoutUndeletedPostReferences(ctx, imageIDs)
	if err != nil {
		return apierr.Internal(err)
	}
	assetsByID := make(map[uint64]*model.ImageAsset, len(assets))
	for index := range assets {
		assetsByID[assets[index].ID] = &assets[index]
	}
	for _, imageID := range imageIDsToBlock {
		if assetsByID[imageID] != nil &&
			assetsByID[imageID].Moderation == model.ModerationStatusBlock {
			continue
		}
		if err := moderation.applyAdminPostDeleteImage(
			ctx, post.ID, actorID, assetsByID[imageID], now,
		); err != nil {
			return apierr.Internal(err)
		}
	}
	return nil
}

func (s *PostService) updateInputFromSnapshot(
	ctx context.Context,
	raw json.RawMessage,
	editReason *string,
) (UpdatePostInput, postSnapshot, error) {
	var snapshot postSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return UpdatePostInput{}, snapshot, apierr.Internal(err)
	}
	payload := PostPayload{
		Title: snapshot.Title, Content: snapshot.Content, Category: string(snapshot.Category),
		CanteenWindowID: snapshot.CanteenWindowID, Tags: append([]string{}, snapshot.Tags...),
		Images: append([]string{}, snapshot.Images...),
	}
	if snapshot.ShareType != nil {
		value := string(*snapshot.ShareType)
		payload.ShareType = &value
	}
	if snapshot.CanteenID != nil {
		canteen, err := s.posts.FindActiveCanteenByID(ctx, *snapshot.CanteenID)
		if err != nil {
			return UpdatePostInput{}, snapshot, dictionaryError(err, "餐厅")
		}
		payload.CanteenCode = &canteen.Code
	}
	if snapshot.CuisineID != nil {
		cuisine, err := s.posts.FindActiveCuisineByID(ctx, *snapshot.CuisineID)
		if err != nil {
			return UpdatePostInput{}, snapshot, dictionaryError(err, "菜系")
		}
		payload.Cuisine = &cuisine.Name
	}
	if snapshot.Price != nil {
		price, err := money.Parse(*snapshot.Price)
		if err != nil {
			return UpdatePostInput{}, snapshot, apierr.Internal(err)
		}
		payload.Price = &price
	}
	if snapshot.BudgetMin != nil && snapshot.BudgetMax != nil {
		payload.BudgetRange = &BudgetRangeInput{Min: *snapshot.BudgetMin, Max: *snapshot.BudgetMax}
	}
	if snapshot.PostType == model.PostTypeShare {
		for _, flavor := range snapshot.Flavors {
			payload.Flavors = append(payload.Flavors, flavor.Name)
		}
	} else {
		preferences := &PreferencesInput{}
		for _, flavor := range snapshot.Flavors {
			switch flavor.Stance {
			case model.FlavorStancePrefer:
				preferences.PreferFlavors = append(preferences.PreferFlavors, flavor.Name)
			case model.FlavorStanceAvoid:
				preferences.AvoidFlavors = append(preferences.AvoidFlavors, flavor.Name)
			}
		}
		if len(preferences.PreferFlavors) > 0 || len(preferences.AvoidFlavors) > 0 {
			payload.Preferences = preferences
		}
	}
	postType := string(snapshot.PostType)
	publishStatus := string(model.PostStatusPending)
	return UpdatePostInput{
		PostPayload: payload, PostType: &postType, Status: &publishStatus, EditReason: editReason,
	}, snapshot, nil
}

func (s *PostService) restoreDeletedSnapshotImages(
	ctx context.Context,
	postID uint64,
	actorID uint64,
	urls []string,
	oldImageIDs []uint64,
) error {
	assets, err := s.posts.FindImagesByURLs(ctx, urls)
	if err != nil {
		return apierr.Internal(err)
	}
	targetIDs := make(map[uint64]struct{}, len(assets))
	allIDs := append([]uint64{}, oldImageIDs...)
	for index := range assets {
		targetIDs[assets[index].ID] = struct{}{}
		allIDs = append(allIDs, assets[index].ID)
	}
	locked, err := s.posts.LockImagesByIDs(ctx, allIDs)
	if err != nil {
		return apierr.Internal(err)
	}
	targets := make([]model.ImageAsset, 0, len(targetIDs))
	for index := range locked {
		if _, exists := targetIDs[locked[index].ID]; exists {
			targets = append(targets, locked[index])
		}
	}
	if err := restoreAdminPostDeleteImages(
		ctx, s.posts, s.moderation, postID, actorID, targets, time.Now().UTC(),
	); err != nil {
		return apierr.Internal(err)
	}
	return nil
}

func inheritedPostModerationRecord(
	postID uint64,
	contentRevision int32,
	sourceRevision int32,
	source *model.ModerationRecord,
	now time.Time,
) (*model.ModerationRecord, error) {
	raw, err := json.Marshal(struct {
		Action                   string `json:"action"`
		SourceRevision           int32  `json:"source_revision"`
		SourceModerationRecordID uint64 `json:"source_moderation_record_id"`
	}{
		Action: "restore_history", SourceRevision: sourceRevision,
		SourceModerationRecordID: source.ID,
	})
	if err != nil {
		return nil, err
	}
	labels := append(pq.StringArray{}, source.Labels...)
	return &model.ModerationRecord{
		PostID: &postID, ContentRevision: &contentRevision,
		Scene: model.ModerationSceneText, Provider: historyRestoreProvider,
		Verdict: source.Verdict, Labels: labels, Score: source.Score,
		RawResponse: raw, CreatedAt: now,
	}, nil
}

func (s *PostService) finishModeration(
	ctx context.Context,
	postID uint64,
	contentRevision int32,
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
	record := moderationRecordForPost(postID, contentRevision, result)
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
	return aggregatePostModeration(result.Verdict, resolved.ImageAssets)
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
		if asset.Status != model.ImageStatusPending && asset.Status != model.ImageStatusReady {
			return nil, nil, apierr.Conflict(apierr.BizImageNotApproved, "图片当前不可引用")
		}
		if asset.UploaderID == nil || *asset.UploaderID != authorID {
			return nil, nil, apierr.Forbidden(apierr.BizImageNotOwned, "只能引用自己上传的图片")
		}
		if asset.Purpose != model.ImagePurposePost {
			return nil, nil, apierr.Conflict(apierr.BizImagePurposeWrong, "图片用途不是帖子配图")
		}
		if asset.Moderation == model.ModerationStatusBlock {
			return nil, nil, apierr.Conflict(
				apierr.BizImageNotApproved,
				fmt.Sprintf("第 %d 张图片（upload_id=%d）未通过审核", len(ordered)+1, asset.ID),
			)
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
	contentRevision int32,
	result ModerationResult,
) *model.ModerationRecord {
	record := moderationRecordForTarget(&postID, nil, result)
	record.ContentRevision = &contentRevision
	return record
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

func aggregatePostModeration(
	textVerdict model.ModerationVerdict,
	images []model.ImageAsset,
) (model.PostStatus, error) {
	if textVerdict == model.ModerationVerdictBlock || imagesContainModeration(
		images, model.ModerationStatusBlock,
	) {
		return model.PostStatusRejected, nil
	}
	if textVerdict == model.ModerationVerdictReview ||
		imagesContainModeration(images, model.ModerationStatusReview) ||
		imagesContainModeration(images, model.ModerationStatusPending) {
		return model.PostStatusPending, nil
	}
	if textVerdict != model.ModerationVerdictPass {
		return "", apierr.Internal(errors.New("未知的审核结论"))
	}
	return model.PostStatusApproved, nil
}

func imagesContainModeration(images []model.ImageAsset, status model.ModerationStatus) bool {
	for _, image := range images {
		if image.Moderation == status {
			return true
		}
	}
	return false
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
