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

// DeletePost 以管理员来源软删除帖子，并收紧已无其他未软删帖子引用的附图访问状态。
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
	imageIDs, err := s.posts.PostImageIDs(ctx, postID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	assets, err := s.posts.LockImagesByIDs(ctx, imageIDs)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	now := time.Now().UTC()
	if err := s.admin.SoftDeletePost(ctx, postID, actorID, now); err != nil {
		return nil, repository.ToAPIError(err, apierr.BizPostNotFound, "帖子")
	}
	imageIDsToBlock, err := s.posts.ImageIDsWithoutUndeletedPostReferences(ctx, imageIDs)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	assetsByID := make(map[uint64]*model.ImageAsset, len(assets))
	for index := range assets {
		assetsByID[assets[index].ID] = &assets[index]
	}
	for _, imageID := range imageIDsToBlock {
		if err := s.moderation.applyAdminPostDeleteImage(
			ctx, postID, actorID, assetsByID[imageID], now,
		); err != nil {
			return nil, apierr.Internal(err)
		}
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

// RestorePost 只恢复机审软删除，并拒绝恢复出已发布但图片未通过的帖子。
func (s *AdminService) RestorePost(
	ctx context.Context,
	postID uint64,
	actorID uint64,
) (*AdminPostRestoreResult, error) {
	post, err := s.posts.LockByID(ctx, postID, repository.QueryOptions{IncludeDeleted: true})
	if err != nil {
		return nil, repository.ToAPIError(err, apierr.BizPostNotFound, "帖子")
	}
	if !isModerationDeletion(post.DeletedAt, post.DeletedReason) {
		return nil, apierr.Conflict(apierr.BizContentNotRestorable, "只有机审软删除的帖子可以恢复")
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
	if err := s.admin.RestorePost(ctx, postID); err != nil {
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
