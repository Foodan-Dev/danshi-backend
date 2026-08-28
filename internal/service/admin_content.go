package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/lib/pq"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

// DeletePost 按最低身份判定删除来源；作者删除自己的帖子始终记 author。
func (s *AdminService) DeletePost(
	ctx context.Context,
	postID uint64,
	actorID uint64,
) (*AdminPostDeleteResult, error) {
	post, err := s.posts.LockByID(ctx, postID, repository.QueryOptions{IncludeDeleted: true})
	if err != nil {
		return nil, repository.ToAPIError(err, apierr.BizPostNotFound, "帖子")
	}
	if post.DeletedAt != nil {
		return nil, apierr.NotFound(apierr.BizPostDeleted, "帖子")
	}
	reason := model.DeleteReasonAdmin
	if post.AuthorID == actorID {
		reason = model.DeleteReasonAuthor
	}
	now := time.Now().UTC()
	if err := deletePostWithHistoryAndImages(
		ctx, s.posts, s.moderation, post, actorID, reason, now,
	); err != nil {
		return nil, err
	}
	return &AdminPostDeleteResult{PostID: postID}, nil
}

// DeleteComment 以管理员来源软删除评论，回复链和原文保持不变。
func (s *AdminService) DeleteComment(
	ctx context.Context,
	commentID uint64,
	actorID uint64,
) (*AdminCommentDeleteResult, error) {
	comment, err := s.comments.LockByID(ctx, commentID)
	if err != nil {
		return nil, repository.ToAPIError(err, apierr.BizCommentNotFound, "评论")
	}
	if comment.DeletedAt != nil {
		return nil, apierr.NotFound(apierr.BizCommentDeleted, "评论")
	}
	if err := s.comments.SoftDelete(
		ctx, commentID, model.DeleteReasonAdmin, &actorID, time.Now().UTC(),
	); err != nil {
		return nil, repository.ToAPIError(err, apierr.BizCommentNotFound, "评论")
	}
	return &AdminCommentDeleteResult{CommentID: commentID}, nil
}

// RestorePost 恢复三种来源的软删除，并先逆转确由下架产生的图片封禁。
func (s *AdminService) RestorePost(
	ctx context.Context,
	postID uint64,
	actorID uint64,
) (*AdminPostRestoreResult, error) {
	post, err := s.posts.LockByID(ctx, postID, repository.QueryOptions{IncludeDeleted: true})
	if err != nil {
		return nil, repository.ToAPIError(err, apierr.BizPostNotFound, "帖子")
	}
	if !isRestorablePostDeletion(post.DeletedAt, post.DeletedReason) {
		return nil, apierr.Conflict(apierr.BizContentNotRestorable, "帖子当前不处于可恢复的软删除状态")
	}
	if post.AuthorID == actorID {
		return nil, apierr.Conflict(
			apierr.BizContentNotRestorable, "作者请通过历史版本回退恢复自己的帖子",
		)
	}
	imageIDs, err := s.posts.PostImageIDs(ctx, postID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	assets, err := s.posts.LockImagesByIDs(ctx, imageIDs)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if err := restoreAdminPostDeleteImages(
		ctx, s.posts, s.moderation, postID, actorID, assets, time.Now().UTC(),
	); err != nil {
		return nil, apierr.Internal(err)
	}
	if post.Status == model.PostStatusApproved {
		approved, checkErr := s.lockApprovedPostImages(ctx, postID)
		if checkErr != nil {
			return nil, apierr.Internal(checkErr)
		}
		if !approved {
			return nil, apierr.Conflict(apierr.BizImageNotApproved, "帖子仍引用未通过审核的图片")
		}
	}
	revision, err := s.posts.CurrentContentRevision(ctx, postID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	record, err := restorationRecord(actorID, &postID, nil, revision)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if err := s.admin.CreateRestorationRecord(ctx, record); err != nil {
		return nil, apierr.Internal(err)
	}
	if err := s.posts.Restore(ctx, postID); err != nil {
		return nil, repository.ToAPIError(err, apierr.BizPostNotFound, "帖子")
	}
	return &AdminPostRestoreResult{PostID: postID, ModerationRecordID: record.ID}, nil
}

func (s *AdminService) lockApprovedPostImages(ctx context.Context, postID uint64) (bool, error) {
	imageIDs, err := s.posts.PostImageIDs(ctx, postID)
	if err != nil {
		return false, err
	}
	assets, err := s.posts.LockImagesByIDs(ctx, imageIDs)
	if err != nil {
		return false, err
	}
	for _, asset := range assets {
		if asset.Moderation != model.ModerationStatusPass {
			return false, nil
		}
	}
	return true, nil
}

// RestoreComment 只恢复机审软删除；审核状态与计数回补由同一数据库更新完成。
func (s *AdminService) RestoreComment(
	ctx context.Context,
	commentID uint64,
	actorID uint64,
) (*AdminCommentRestoreResult, error) {
	comment, err := s.comments.LockByID(ctx, commentID)
	if err != nil {
		return nil, repository.ToAPIError(err, apierr.BizCommentNotFound, "评论")
	}
	if !isModerationDeletion(comment.DeletedAt, comment.DeletedReason) {
		return nil, apierr.Conflict(apierr.BizContentNotRestorable, "只有机审软删除的评论可以恢复")
	}
	revision, err := s.comments.CurrentContentRevision(ctx, commentID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	record, err := restorationRecord(actorID, nil, &commentID, revision)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if err := s.admin.CreateRestorationRecord(ctx, record); err != nil {
		return nil, apierr.Internal(err)
	}
	if err := s.admin.RestoreComment(ctx, commentID); err != nil {
		return nil, repository.ToAPIError(err, apierr.BizCommentNotFound, "评论")
	}
	return &AdminCommentRestoreResult{
		CommentID: commentID, ModerationRecordID: record.ID,
	}, nil
}

func isModerationDeletion(deletedAt *time.Time, reason *model.DeleteReason) bool {
	return deletedAt != nil && reason != nil && *reason == model.DeleteReasonModeration
}

func isRestorablePostDeletion(deletedAt *time.Time, reason *model.DeleteReason) bool {
	if deletedAt == nil || reason == nil {
		return false
	}
	return *reason == model.DeleteReasonAuthor || *reason == model.DeleteReasonAdmin ||
		*reason == model.DeleteReasonModeration
}

func restoreAdminPostDeleteImages(
	ctx context.Context,
	posts repository.PostRepository,
	moderation *ModerationService,
	postID uint64,
	actorID uint64,
	assets []model.ImageAsset,
	now time.Time,
) error {
	blockedIDs := make([]uint64, 0, len(assets))
	for index := range assets {
		if assets[index].Moderation == model.ModerationStatusBlock {
			blockedIDs = append(blockedIDs, assets[index].ID)
		}
	}
	latest, err := posts.LatestImageModerationByAssetIDs(ctx, blockedIDs)
	if err != nil {
		return err
	}
	for index := range assets {
		asset := &assets[index]
		if asset.Moderation != model.ModerationStatusBlock {
			continue
		}
		record, exists := latest[asset.ID]
		if !exists || record.Provider != adminPostDeleteProvider {
			continue
		}
		if err := moderation.applyAdminPostRestoreImage(
			ctx, postID, actorID, asset, record.ID, now,
		); err != nil {
			return err
		}
		asset.Moderation = model.ModerationStatusPass
	}
	return nil
}

func (s *ModerationService) applyAdminPostDeleteImage(
	ctx context.Context,
	postID uint64,
	actorID uint64,
	asset *model.ImageAsset,
	now time.Time,
) error {
	if asset == nil {
		return nil
	}
	raw, err := json.Marshal(struct {
		Action string `json:"action"`
		PostID uint64 `json:"post_id"`
	}{Action: "admin_delete_post", PostID: postID})
	if err != nil {
		return err
	}
	record := model.ModerationRecord{
		ImageAssetID: &asset.ID, Scene: model.ModerationSceneImage,
		Provider: adminPostDeleteProvider, Verdict: model.ModerationVerdictBlock,
		Labels: pq.StringArray{}, RawResponse: raw,
		ReviewerID: &actorID, ReviewedAt: &now, CreatedAt: now,
	}
	if err := s.moderation.CreateAdministrativeRecord(ctx, &record); err != nil {
		return err
	}
	if err := s.moderation.UpdateImageModeration(
		ctx, asset.ID, model.ModerationStatusBlock,
	); err != nil {
		return err
	}
	return s.applyImageAccess(ctx, asset, record.ID, model.ModerationVerdictBlock)
}

func (s *ModerationService) applyAdminPostRestoreImage(
	ctx context.Context,
	postID uint64,
	actorID uint64,
	asset *model.ImageAsset,
	sourceRecordID uint64,
	now time.Time,
) error {
	if asset == nil {
		return nil
	}
	raw, err := json.Marshal(struct {
		Action         string `json:"action"`
		PostID         uint64 `json:"post_id"`
		SourceRecordID uint64 `json:"source_record_id"`
	}{Action: "restore_post_image", PostID: postID, SourceRecordID: sourceRecordID})
	if err != nil {
		return err
	}
	record := model.ModerationRecord{
		ImageAssetID: &asset.ID, Scene: model.ModerationSceneImage,
		Provider: adminRestoreProvider, Verdict: model.ModerationVerdictPass,
		Labels: pq.StringArray{}, RawResponse: raw,
		ReviewerID: &actorID, ReviewedAt: &now, CreatedAt: now,
	}
	if err := s.moderation.CreateAdministrativeRecord(ctx, &record); err != nil {
		return err
	}
	if err := s.moderation.UpdateImageModeration(
		ctx, asset.ID, model.ModerationStatusPass,
	); err != nil {
		return err
	}
	return s.applyImageAccess(ctx, asset, record.ID, model.ModerationVerdictPass)
}

func restorationRecord(
	actorID uint64,
	postID *uint64,
	commentID *uint64,
	contentRevision int32,
) (*model.ModerationRecord, error) {
	raw, err := json.Marshal(struct {
		Action string `json:"action"`
	}{Action: "restore"})
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	record := &model.ModerationRecord{
		PostID: postID, CommentID: commentID, ContentRevision: &contentRevision,
		Scene:    model.ModerationSceneText,
		Provider: adminRestoreProvider, Verdict: model.ModerationVerdictPass,
		Labels: pq.StringArray{}, RawResponse: raw, ReviewerID: &actorID, ReviewedAt: &now,
		CreatedAt: now,
	}
	return record, nil
}
