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

// DeletePost 以管理员来源软删除帖子，不解除任何内容关联。
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
	if err := s.admin.SoftDeletePost(ctx, postID, actorID, time.Now().UTC()); err != nil {
		return nil, repository.ToAPIError(err, apierr.BizPostNotFound, "帖子")
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
	record, err := restorationRecord(actorID, &postID, nil)
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
	record, err := restorationRecord(actorID, nil, &commentID)
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

func restorationRecord(
	actorID uint64,
	postID *uint64,
	commentID *uint64,
) (*model.ModerationRecord, error) {
	raw, err := json.Marshal(struct {
		Action string `json:"action"`
	}{Action: "restore"})
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	record := &model.ModerationRecord{
		PostID: postID, CommentID: commentID, Scene: model.ModerationSceneText,
		Provider: adminRestoreProvider, Verdict: model.ModerationVerdictPass,
		Labels: pq.StringArray{}, RawResponse: raw, ReviewerID: &actorID, ReviewedAt: &now,
		CreatedAt: now,
	}
	return record, nil
}
