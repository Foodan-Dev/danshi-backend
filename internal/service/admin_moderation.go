package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/ptime"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

// ReviewPost 通过帖子 id 找到未处理的机器 review 记录，并复用统一人工复核动作。
func (s *AdminService) ReviewPost(
	ctx context.Context,
	postID uint64,
	reviewerID uint64,
	input AdminPostReviewInput,
) (*AdminPostReviewResult, error) {
	verdict, err := postReviewVerdict(input.Status)
	if err != nil {
		return nil, err
	}
	raw, err := postReviewFeedback(input.Feedback)
	if err != nil {
		return nil, err
	}
	post, err := s.posts.FindByID(ctx, postID, repository.QueryOptions{IncludeDeleted: true})
	if err != nil {
		return nil, repository.ToAPIError(err, apierr.BizPostNotFound, "帖子")
	}
	if post.DeletedAt != nil {
		return nil, apierr.NotFound(apierr.BizPostDeleted, "帖子")
	}
	if post.Status != model.PostStatusPending {
		return nil, apierr.Conflict(apierr.BizModerationNotPending, "帖子当前不在待审核状态")
	}
	machine, err := s.admin.FindPendingPostReview(ctx, postID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apierr.Conflict(apierr.BizModerationNotPending, "帖子没有待人工复核的机审记录")
		}
		return nil, apierr.Internal(err)
	}
	manual, err := s.moderation.ManualReview(ctx, ManualReviewInput{
		MachineRecordID: machine.ID, ReviewerID: reviewerID, Verdict: verdict,
		Labels: []string{}, RawResponse: raw,
	})
	if err != nil {
		return nil, err
	}
	current, err := s.posts.FindByID(ctx, postID, repository.QueryOptions{IncludeDeleted: true})
	if err != nil {
		return nil, repository.ToAPIError(err, apierr.BizPostNotFound, "帖子")
	}
	return &AdminPostReviewResult{
		PostID: postID, Status: current.Status, ReviewedAt: ptime.Time(*manual.ReviewedAt),
		ModerationRecordID: manual.ID,
	}, nil
}

// ManualReview 按审核记录 id 对任意支持的对象追加人工裁决行。
func (s *AdminService) ManualReview(
	ctx context.Context,
	machineRecordID uint64,
	reviewerID uint64,
	input AdminManualReviewInput,
) (*AdminReviewResult, error) {
	manual, err := s.moderation.ManualReview(ctx, ManualReviewInput{
		MachineRecordID: machineRecordID, ReviewerID: reviewerID,
		Verdict: input.Verdict, Labels: input.Labels, Score: input.Score,
		RawResponse: input.RawResponse,
	})
	if err != nil {
		return nil, err
	}
	target, targetID := moderationRecordTarget(manual)
	return &AdminReviewResult{
		ModerationRecordID: manual.ID, TargetType: string(target), TargetID: targetID,
		Verdict: manual.Verdict, ReviewedAt: ptime.Time(*manual.ReviewedAt),
	}, nil
}

func postReviewVerdict(raw string) (model.ModerationVerdict, error) {
	switch strings.TrimSpace(raw) {
	case string(model.PostStatusApproved):
		return model.ModerationVerdictPass, nil
	case string(model.PostStatusRejected):
		return model.ModerationVerdictBlock, nil
	default:
		return "", apierr.InvalidField(
			"status", apierr.FieldInvalidEnum, "status 必须是 approved 或 rejected",
		)
	}
}

func postReviewFeedback(feedback *string) (json.RawMessage, error) {
	if feedback == nil {
		return nil, nil
	}
	value := strings.TrimSpace(*feedback)
	if utf8.RuneCountInString(value) > 1000 {
		return nil, apierr.InvalidField("feedback", apierr.FieldTooLong, "feedback 不能超过 1000 个字符")
	}
	raw, err := json.Marshal(struct {
		Feedback string `json:"feedback"`
	}{Feedback: value})
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return raw, nil
}
