package service

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/ptime"
	"github.com/jingyijun/danshi_backend_go/internal/repository"
)

// UpdateUserStatus 执行永久封禁、限时封禁或解封，并在封禁时同事务撤销全部会话。
func (s *AdminService) UpdateUserStatus(
	ctx context.Context,
	userID uint64,
	actorID uint64,
	input UpdateAdminUserStatusInput,
) (*AdminUserStatusResult, error) {
	now := time.Now().UTC()
	state, banned, err := normalizeAdminBan(input, actorID, now)
	if err != nil {
		return nil, err
	}
	if _, err := s.admin.LockUserByID(ctx, userID, repository.QueryOptions{}); err != nil {
		return nil, repository.ToAPIError(err, apierr.BizNotFound, "用户")
	}
	if err := s.admin.UpdateUserBan(ctx, userID, state, now); err != nil {
		return nil, repository.ToAPIError(err, apierr.BizNotFound, "用户")
	}
	if banned {
		if err := s.sessions.RevokeAll(ctx, userID, now); err != nil {
			return nil, apierr.Internal(err)
		}
	}
	return &AdminUserStatusResult{
		UserID: userID, IsActive: !banned, IsBanned: banned,
		BanIsPermanent: state.IsPermanent, BannedUntil: ptime.Ptr(state.Until),
		BanReason: state.Reason, BannedBy: state.ActorID,
	}, nil
}

// UpdateUserRole 调整用户角色；super_admin 权限由路由中间件单独把关。
func (s *AdminService) UpdateUserRole(
	ctx context.Context,
	userID uint64,
	rawRole string,
) (*AdminUserRoleResult, error) {
	role, err := adminRole(rawRole, false)
	if err != nil {
		return nil, err
	}
	if _, err := s.admin.LockUserByID(ctx, userID, repository.QueryOptions{}); err != nil {
		return nil, repository.ToAPIError(err, apierr.BizNotFound, "用户")
	}
	if err := s.admin.UpdateUserRole(ctx, userID, *role, time.Now().UTC()); err != nil {
		return nil, repository.ToAPIError(err, apierr.BizNotFound, "用户")
	}
	return &AdminUserRoleResult{UserID: userID, Role: *role}, nil
}

func normalizeAdminBan(
	input UpdateAdminUserStatusInput,
	actorID uint64,
	now time.Time,
) (repository.UserBanState, bool, error) {
	if input.IsActive != nil {
		return normalizeLegacyAdminBan(input, actorID)
	}
	if input.BanIsPermanent == nil {
		return repository.UserBanState{}, false, apierr.InvalidField(
			"ban_is_permanent", apierr.FieldRequired, "ban_is_permanent 必填",
		)
	}
	if *input.BanIsPermanent && input.BannedUntil != nil {
		return repository.UserBanState{}, false, apierr.InvalidField(
			"banned_until", apierr.FieldConflict, "永久封禁不能同时设置 banned_until",
		)
	}
	if *input.BanIsPermanent {
		return permanentBan(input.BanReason, actorID)
	}
	if input.BannedUntil != nil {
		if !input.BannedUntil.After(now) {
			return repository.UserBanState{}, false, apierr.InvalidField(
				"banned_until", apierr.FieldOutOfRange, "banned_until 必须晚于当前时间",
			)
		}
		return timedBan(input.BannedUntil, input.BanReason, actorID)
	}
	if input.BanReason != nil || input.LegacyReason != nil {
		return repository.UserBanState{}, false, apierr.InvalidField(
			"ban_reason", apierr.FieldConflict, "解封时不能保留 ban_reason",
		)
	}
	return repository.UserBanState{}, false, nil
}

func normalizeLegacyAdminBan(
	input UpdateAdminUserStatusInput,
	actorID uint64,
) (repository.UserBanState, bool, error) {
	if input.BanIsPermanent != nil || input.BannedUntil != nil || input.BanReason != nil {
		return repository.UserBanState{}, false, apierr.InvalidField(
			"is_active", apierr.FieldConflict, "is_active 不能与 v2 封禁字段混用",
		)
	}
	if *input.IsActive {
		if input.LegacyReason != nil {
			return repository.UserBanState{}, false, apierr.InvalidField(
				"reason", apierr.FieldConflict, "解封时不能保留 reason",
			)
		}
		return repository.UserBanState{}, false, nil
	}
	return permanentBan(input.LegacyReason, actorID)
}

func permanentBan(reason *string, actorID uint64) (repository.UserBanState, bool, error) {
	reason, err := normalizeBanReason(reason)
	if err != nil {
		return repository.UserBanState{}, false, err
	}
	return repository.UserBanState{
		IsPermanent: true, Reason: reason, ActorID: &actorID,
	}, true, nil
}

func timedBan(
	until *time.Time,
	reason *string,
	actorID uint64,
) (repository.UserBanState, bool, error) {
	reason, err := normalizeBanReason(reason)
	if err != nil {
		return repository.UserBanState{}, false, err
	}
	return repository.UserBanState{Until: until, Reason: reason, ActorID: &actorID}, true, nil
}

func normalizeBanReason(reason *string) (*string, error) {
	if reason == nil || strings.TrimSpace(*reason) == "" {
		return nil, apierr.InvalidField("ban_reason", apierr.FieldRequired, "ban_reason 必填且不能为空")
	}
	value := strings.TrimSpace(*reason)
	if utf8.RuneCountInString(value) > 200 {
		return nil, apierr.InvalidField("ban_reason", apierr.FieldTooLong, "ban_reason 不能超过 200 个字符")
	}
	return &value, nil
}

func isCurrentlyBanned(user *model.User, now time.Time) bool {
	return user.BanIsPermanent || (user.BannedUntil != nil && user.BannedUntil.After(now))
}
