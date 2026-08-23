package service

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/pagination"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/ptime"
	"github.com/jingyijun/danshi_backend_go/internal/repository"
)

// PendingPosts 返回未删除的待审帖子。
func (s *AdminService) PendingPosts(
	ctx context.Context,
	params pagination.Params,
) (*AdminPostList, error) {
	status := model.PostStatusPending
	rows, meta, err := s.admin.FindPostPage(
		ctx, repository.AdminPostFilter{Status: &status}, params, repository.QueryOptions{},
	)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return adminPostList(rows, meta, false), nil
}

// Posts 返回包含软删除行的全部帖子列表。
func (s *AdminService) Posts(
	ctx context.Context,
	rawStatus string,
	rawPostType string,
	params pagination.Params,
) (*AdminPostList, error) {
	filter, err := adminPostFilter(rawStatus, rawPostType)
	if err != nil {
		return nil, err
	}
	rows, meta, err := s.admin.FindPostPage(
		ctx, filter, params, repository.QueryOptions{IncludeDeleted: true},
	)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return adminPostList(rows, meta, true), nil
}

// Comments 返回包含软删除行的全部评论列表。
func (s *AdminService) Comments(
	ctx context.Context,
	postID *uint64,
	params pagination.Params,
) (*AdminCommentList, error) {
	rows, meta, err := s.admin.FindCommentPage(
		ctx, postID, params, repository.QueryOptions{IncludeDeleted: true},
	)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	comments := make([]AdminCommentView, 0, len(rows))
	for _, row := range rows {
		comments = append(comments, AdminCommentView{
			ID: row.ID, Content: row.Content, PostID: row.PostID,
			Author:   AdminAuthorView{ID: row.AuthorID, Name: row.AuthorName, Email: row.AuthorEmail},
			ParentID: row.ParentID, LikeCount: row.LikeCount, ReplyCount: row.ReplyCount,
			DeletedAt: ptime.Ptr(row.DeletedAt), DeletedReason: row.DeletedReason,
			DeletedBy: row.DeletedBy, CreatedAt: ptime.Time(row.CreatedAt),
		})
	}
	return &AdminCommentList{Comments: comments, Pagination: meta}, nil
}

// Users 返回未注销的用户列表；注销用户仍可通过详情与内容取证端点访问。
func (s *AdminService) Users(
	ctx context.Context,
	rawRole string,
	isActive *bool,
	params pagination.Params,
) (*AdminUserList, error) {
	role, err := adminRole(rawRole, true)
	if err != nil {
		return nil, err
	}
	rows, meta, err := s.admin.FindUserPage(
		ctx, repository.AdminUserFilter{Role: role, IsActive: isActive}, params,
		repository.QueryOptions{},
	)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	users := make([]AdminUserView, 0, len(rows))
	now := time.Now().UTC()
	for index := range rows {
		users = append(users, adminUserView(&rows[index], now))
	}
	return &AdminUserList{Users: users, Pagination: meta}, nil
}

// User 返回单个用户详情及其完整封禁历史。
func (s *AdminService) User(ctx context.Context, userID uint64) (*AdminUserDetail, error) {
	row, err := s.admin.FindUserByID(ctx, userID, repository.QueryOptions{IncludeDeleted: true})
	if err != nil {
		return nil, repository.ToAPIError(err, apierr.BizNotFound, "用户")
	}
	records, err := s.admin.FindUserBanRecords(ctx, userID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	views := make([]AdminUserBanRecordView, 0, len(records))
	for _, record := range records {
		views = append(views, AdminUserBanRecordView{
			ID: record.ID, Action: record.Action, BanIsPermanent: record.BanIsPermanent,
			BannedUntil: ptime.Ptr(record.BannedUntil), Reason: record.Reason,
			ActorID: record.ActorID, CreatedAt: ptime.Time(record.CreatedAt),
		})
	}
	return &AdminUserDetail{
		AdminUserView: adminUserView(row, time.Now().UTC()), BanRecords: views,
	}, nil
}

// UserPosts 返回目标用户全部帖子，包括未通过与软删除内容。
func (s *AdminService) UserPosts(
	ctx context.Context,
	userID uint64,
	rawStatus string,
	rawPostType string,
	params pagination.Params,
) (*AdminPostList, error) {
	if _, err := s.admin.FindUserByID(
		ctx, userID, repository.QueryOptions{IncludeDeleted: true},
	); err != nil {
		return nil, repository.ToAPIError(err, apierr.BizNotFound, "用户")
	}
	filter, err := adminPostFilter(rawStatus, rawPostType)
	if err != nil {
		return nil, err
	}
	filter.AuthorID = &userID
	rows, meta, err := s.admin.FindPostPage(
		ctx, filter, params, repository.QueryOptions{IncludeDeleted: true},
	)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return adminPostList(rows, meta, true), nil
}

// Admins 返回 moderator 角色用户。
func (s *AdminService) Admins(ctx context.Context, params pagination.Params) (*AdminUserList, error) {
	return s.Users(ctx, string(model.UserRoleModerator), nil, params)
}

// SuperAdmins 返回 super_admin 角色用户。
func (s *AdminService) SuperAdmins(
	ctx context.Context,
	params pagination.Params,
) (*AdminUserList, error) {
	return s.Users(ctx, string(model.UserRoleSuperAdmin), nil, params)
}

// PendingModeration 返回所有对象类型的待人工复核记录。
func (s *AdminService) PendingModeration(
	ctx context.Context,
	params pagination.Params,
) (*AdminModerationList, error) {
	rows, meta, err := s.admin.FindPendingModerationPage(ctx, params)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	records := make([]AdminModerationView, 0, len(rows))
	for _, row := range rows {
		records = append(records, AdminModerationView{
			ID: row.ID, TargetType: row.TargetType, TargetID: row.TargetID,
			Field: row.Field, PostHistoryID: row.PostHistoryID,
			CommentHistoryID: row.CommentHistoryID, Scene: row.Scene,
			Provider: row.Provider, ProviderJobID: row.ProviderJobID, Verdict: row.Verdict,
			Labels: append([]string{}, row.Labels...), Score: row.Score,
			Content: row.Content, CreatedAt: ptime.Time(row.CreatedAt),
		})
	}
	return &AdminModerationList{Records: records, Pagination: meta}, nil
}

func adminPostList(
	rows []repository.AdminPostRecord,
	meta pagination.Meta,
	preview bool,
) *AdminPostList {
	posts := make([]AdminPostView, 0, len(rows))
	for _, row := range rows {
		content := row.Content
		if preview {
			content = adminContentPreview(content)
		}
		posts = append(posts, AdminPostView{
			ID: row.ID, Title: row.Title, Content: content, Category: row.Category,
			PostType: row.PostType, Images: row.Images,
			Author: AdminAuthorView{ID: row.AuthorID, Name: row.AuthorName, Email: row.AuthorEmail},
			Status: row.Status, LikeCount: row.LikeCount, ViewCount: row.ViewCount,
			CommentCount: row.CommentCount, DeletedAt: ptime.Ptr(row.DeletedAt),
			DeletedReason: row.DeletedReason, DeletedBy: row.DeletedBy,
			CreatedAt: ptime.Time(row.CreatedAt),
		})
	}
	return &AdminPostList{Posts: posts, Pagination: meta}
}

func adminPostFilter(rawStatus, rawPostType string) (repository.AdminPostFilter, error) {
	filter := repository.AdminPostFilter{}
	if rawStatus != "" {
		status := model.PostStatus(strings.TrimSpace(rawStatus))
		if status != model.PostStatusDraft && status != model.PostStatusPending &&
			status != model.PostStatusApproved && status != model.PostStatusRejected {
			return filter, apierr.InvalidField(
				"status", apierr.FieldInvalidEnum, "status 必须是 draft、pending、approved 或 rejected",
			)
		}
		filter.Status = &status
	}
	if rawPostType != "" {
		postType := model.PostType(strings.TrimSpace(rawPostType))
		if postType != model.PostTypeShare && postType != model.PostTypeSeeking {
			return filter, apierr.InvalidField(
				"post_type", apierr.FieldInvalidEnum, "post_type 必须是 share 或 seeking",
			)
		}
		filter.PostType = &postType
	}
	return filter, nil
}

func adminRole(raw string, optional bool) (*model.UserRole, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" && optional {
		return nil, nil
	}
	role := model.UserRole(raw)
	if role != model.UserRoleUser && role != model.UserRoleDictReviewer &&
		role != model.UserRoleModerator && role != model.UserRoleSuperAdmin {
		return nil, apierr.InvalidField(
			"role", apierr.FieldInvalidEnum,
			"role 必须是 user、dict_reviewer、moderator 或 super_admin",
		)
	}
	return &role, nil
}

func adminUserView(row *repository.AdminUserRecord, now time.Time) AdminUserView {
	banned := isCurrentlyBanned(&row.User, now)
	return AdminUserView{
		ID: row.ID, Name: row.Name, Email: row.Email, Roles: roleStrings(row.Roles),
		IsActive: row.DeletedAt == nil && !banned, IsBanned: banned,
		BanIsPermanent: row.BanIsPermanent, BannedUntil: ptime.Ptr(row.BannedUntil),
		BanReason: row.BanReason, BannedBy: row.BannedBy, AvatarURL: row.AvatarURL, Bio: row.Bio,
		Stats: UserStats{
			PostCount: row.PostCount, LikeCount: row.LikeCount, FavoriteCount: row.FavoriteCount,
			FollowerCount: row.FollowerCount, FollowingCount: row.FollowingCount,
		},
		DeletedAt: ptime.Ptr(row.DeletedAt), CreatedAt: ptime.Time(row.CreatedAt),
	}
}

func roleStrings(values []string) []model.UserRole {
	roles := make([]model.UserRole, 0, len(values))
	for _, value := range values {
		roles = append(roles, model.UserRole(value))
	}
	return roles
}

func adminContentPreview(content string) string {
	if utf8.RuneCountInString(content) <= 200 {
		return content
	}
	return truncateRunes(content, 200) + "..."
}
