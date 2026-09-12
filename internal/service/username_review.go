package service

import (
	"context"
	"errors"
	"time"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

// applyManualUsernameVerdict 在已持有用户行锁的复核事务内应用候选值。
// 拒绝只追加裁决，不改变正式用户名；通过必须再次满足当前改名约束。
func applyManualUsernameVerdict(ctx context.Context, original *model.ModerationRecord, verdict model.ModerationVerdict) error {
	if verdict != model.ModerationVerdictPass {
		return nil
	}
	if original.UsernameCandidate == nil || original.UsernameRevision == nil {
		return apierr.Conflict(apierr.BizConflict, "该历史审核缺少候选用户名，请用户重新提交")
	}
	users := repository.UserRepository{}
	user, err := users.LockByID(ctx, *original.UserID)
	if err != nil {
		return userNotFoundError(err)
	}
	revision, err := users.UsernameRevision(ctx, user.ID)
	if err != nil {
		return apierr.Internal(err)
	}
	if revision != *original.UsernameRevision {
		return apierr.Conflict(apierr.BizConflict, "用户名已变更，该审核申请已过期")
	}
	candidate, err := normalizeUsername(*original.UsernameCandidate)
	if err != nil {
		return err
	}
	if candidate == user.Username {
		return nil
	}
	changed, err := users.HasRecentUsernameChange(ctx, user.ID)
	if err != nil {
		return apierr.Internal(err)
	}
	if changed {
		return usernameChangeLimitedError()
	}
	if err := claimUsername(ctx, users, user.ID, candidate); err != nil {
		return err
	}
	if err := users.UpdateProfile(ctx, user.ID, map[string]any{"name": candidate}); err != nil {
		if repository.IsCheckViolation(err, "user_name_change_records_cooldown_check") {
			return usernameChangeLimitedError()
		}
		return userNotFoundError(err)
	}
	return nil
}

func claimUsername(ctx context.Context, users repository.UserRepository, userID uint64, candidate string) error {
	if err := users.ClaimUsername(ctx, userID, candidate, time.Now().UTC()); err != nil {
		if repository.IsUniqueViolation(err, "uq_user_name_claims_name_lower") ||
			repository.IsUniqueViolation(err, "uq_users_name_lower") || errors.Is(err, repository.ErrAlreadyExists) {
			return apierr.Conflict(apierr.BizUsernameTaken, "用户名已被占用")
		}
		return apierr.Internal(err)
	}
	return nil
}
