package service

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lib/pq"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/pagination"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/ptime"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

const maxUserBioRunes = 500

// UserStats 是用户主页与关注列表共享的统计数据。
type UserStats struct {
	PostCount      int64 `json:"post_count"`
	LikeCount      int64 `json:"like_count"`
	FavoriteCount  int64 `json:"favorite_count"`
	FollowerCount  int64 `json:"follower_count"`
	FollowingCount int64 `json:"following_count"`
}

// UserProfile 是用户主页。email 与 roles 仅在本人视角非空并参与序列化。
type UserProfile struct {
	ID          uint64            `json:"id"`
	Email       *string           `json:"email,omitempty"`
	Name        string            `json:"name"`
	AvatarURL   *string           `json:"avatar_url"`
	Bio         *string           `json:"bio"`
	Gender      *model.Gender     `json:"gender"`
	Roles       *[]model.UserRole `json:"roles,omitempty"`
	Stats       UserStats         `json:"stats"`
	IsFollowing bool              `json:"is_following"`
	CreatedAt   ptime.Time        `json:"created_at"`
}

// UpdateUserInput 是带字段存在性的局部资料更新输入。
type UpdateUserInput struct {
	Name         *string
	NameSet      bool
	Bio          *string
	BioSet       bool
	Gender       *string
	GenderSet    bool
	AvatarURL    *string
	AvatarURLSet bool
}

// UserUpdateResult 包装资料更新后的本人主页。
type UserUpdateResult struct {
	User UserProfile `json:"user"`
}

// UserDeleteResult 是本人账号软注销成功后的稳定响应。
type UserDeleteResult struct {
	UserID uint64 `json:"user_id"`
}

// FollowActionResult 是关注/取关后的稳定状态。
type FollowActionResult struct {
	IsFollowing   bool  `json:"is_following"`
	FollowerCount int64 `json:"follower_count"`
}

// UserListItem 是关注/粉丝列表的公开用户项。
type UserListItem struct {
	ID          uint64    `json:"id"`
	Name        string    `json:"name"`
	AvatarURL   *string   `json:"avatar_url"`
	Bio         *string   `json:"bio"`
	Stats       UserStats `json:"stats"`
	IsFollowing bool      `json:"is_following"`
}

// UserFollowList 是关注/粉丝列表及分页信息。
type UserFollowList struct {
	Users      []UserListItem  `json:"users"`
	Pagination pagination.Meta `json:"pagination"`
}

// UserService 实现用户资料、个人列表与关注关系。
type UserService struct {
	moderator     ContentModerator
	alerter       UserModerationAlerter
	users         repository.UserRepository
	posts         repository.PostRepository
	notifications repository.NotificationRepository
	sessions      repository.SessionRepository
}

// NewUserService 创建用户服务。
func NewUserService(moderator ContentModerator, alerter UserModerationAlerter) *UserService {
	if moderator == nil {
		moderator = UnavailableContentModerator{}
	}
	if alerter == nil {
		alerter = DiscardUserModerationAlerter{}
	}
	return &UserService{moderator: moderator, alerter: alerter}
}

// Delete 只允许本人软注销账号，并在同一事务撤销全部会话。
func (s *UserService) Delete(
	ctx context.Context,
	userID uint64,
	currentUserID uint64,
) (*UserDeleteResult, error) {
	if userID != currentUserID {
		return nil, apierr.Forbidden(apierr.BizNotOwner, "只能注销自己的账号")
	}
	if _, err := s.users.LockByID(ctx, userID); err != nil {
		return nil, userNotFoundError(err)
	}
	now := time.Now().UTC()
	if err := s.users.SoftDelete(ctx, userID, now); err != nil {
		return nil, userNotFoundError(err)
	}
	if err := s.sessions.RevokeAll(ctx, userID, now); err != nil {
		return nil, apierr.Internal(err)
	}
	return &UserDeleteResult{UserID: userID}, nil
}

// Profile 返回用户主页，并只在本人视角附带 email 与 roles。
func (s *UserService) Profile(ctx context.Context, userID, currentUserID uint64) (*UserProfile, error) {
	record, err := s.users.FindProfile(ctx, userID, currentUserID)
	if err != nil {
		return nil, userNotFoundError(err)
	}
	profile := buildUserProfile(record)
	if userID == currentUserID {
		roles, rolesErr := s.users.FindRoles(ctx, userID)
		if rolesErr != nil {
			return nil, apierr.Internal(rolesErr)
		}
		email := record.Email
		profile.Email, profile.Roles = &email, &roles
	}
	return &profile, nil
}

// Update 局部更新本人资料，昵称与简介发生变化时分别送审并追加流水。
func (s *UserService) Update(
	ctx context.Context,
	userID uint64,
	currentUserID uint64,
	input UpdateUserInput,
) (*UserUpdateResult, error) {
	if userID != currentUserID {
		return nil, apierr.Forbidden(apierr.BizNotOwner, "只能修改自己的资料")
	}
	input, err := normalizeUserUpdate(input)
	if err != nil {
		return nil, err
	}
	user, err := s.users.LockByID(ctx, userID)
	if err != nil {
		return nil, userNotFoundError(err)
	}
	fields, moderated := changedUserFields(user, input)
	if input.AvatarURLSet {
		avatarID, avatarErr := s.resolveAvatar(ctx, user, input.AvatarURL)
		if avatarErr != nil {
			return nil, avatarErr
		}
		if !equalIDs(user.AvatarImageAssetID, avatarID) {
			fields["avatar_image_asset_id"] = avatarID
		}
	}
	if err := s.users.UpdateProfile(ctx, userID, fields); err != nil {
		return nil, userNotFoundError(err)
	}
	for _, field := range moderated {
		if err := s.moderateUserField(ctx, userID, field.Field, field.Text); err != nil {
			return nil, err
		}
	}
	profile, err := s.Profile(ctx, userID, currentUserID)
	if err != nil {
		return nil, err
	}
	return &UserUpdateResult{User: *profile}, nil
}

// Posts 返回用户帖子；他人视角强制只看 approved。
func (s *UserService) Posts(
	ctx context.Context,
	userID uint64,
	status string,
	params pagination.Params,
	currentUserID uint64,
) (*PostList, error) {
	if err := s.ensureUser(ctx, userID); err != nil {
		return nil, err
	}
	parsed, err := userPostStatus(status)
	if err != nil {
		return nil, err
	}
	if userID != currentUserID {
		if parsed != nil && *parsed != model.PostStatusApproved {
			return nil, apierr.Forbidden(apierr.BizPermissionDenied, "无法查看他人的非公开帖子")
		}
		approved := model.PostStatusApproved
		parsed = &approved
	}
	records, meta, err := s.posts.FindAuthorPage(ctx, userID, parsed, params)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return s.postList(ctx, records, meta, currentUserID)
}

// Favorites 返回本人收藏的已发布帖子。
func (s *UserService) Favorites(
	ctx context.Context,
	userID uint64,
	params pagination.Params,
	currentUserID uint64,
) (*PostList, error) {
	if userID != currentUserID {
		return nil, apierr.Forbidden(apierr.BizPermissionDenied, "无法查看他人的收藏列表")
	}
	if err := s.ensureUser(ctx, userID); err != nil {
		return nil, err
	}
	records, meta, err := s.posts.FindFavoritePage(ctx, userID, params)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return s.postList(ctx, records, meta, currentUserID)
}

// Follow 幂等关注目标用户；只有真实插入时才创建一条 follow 通知。
func (s *UserService) Follow(ctx context.Context, targetID, currentUserID uint64) (*FollowActionResult, error) {
	if targetID == currentUserID {
		return nil, apierr.BadRequest(apierr.BizCannotFollowSelf, "不能关注自己")
	}
	if err := s.ensureUser(ctx, targetID); err != nil {
		return nil, err
	}
	created, err := s.users.Follow(ctx, currentUserID, targetID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if created {
		notification := &model.Notification{
			RecipientID: targetID, SenderID: currentUserID, Type: model.NotificationTypeFollow,
		}
		if err := s.notifications.Create(ctx, notification); err != nil {
			return nil, apierr.Internal(err)
		}
	}
	return s.followResult(ctx, targetID, true)
}

// Unfollow 幂等取消关注。
func (s *UserService) Unfollow(
	ctx context.Context,
	targetID uint64,
	currentUserID uint64,
) (*FollowActionResult, error) {
	if targetID == currentUserID {
		return nil, apierr.BadRequest(apierr.BizCannotFollowSelf, "不能取消关注自己")
	}
	if err := s.ensureUser(ctx, targetID); err != nil {
		return nil, err
	}
	if err := s.users.Unfollow(ctx, currentUserID, targetID); err != nil {
		return nil, apierr.Internal(err)
	}
	return s.followResult(ctx, targetID, false)
}

// Following 返回指定用户关注的人。
func (s *UserService) Following(
	ctx context.Context,
	userID uint64,
	params pagination.Params,
	currentUserID uint64,
) (*UserFollowList, error) {
	if err := s.ensureUser(ctx, userID); err != nil {
		return nil, err
	}
	rows, meta, err := s.users.FindFollowingPage(ctx, userID, currentUserID, params)
	return userFollowList(rows, meta, err)
}

// Followers 返回指定用户的粉丝。
func (s *UserService) Followers(
	ctx context.Context,
	userID uint64,
	params pagination.Params,
	currentUserID uint64,
) (*UserFollowList, error) {
	if err := s.ensureUser(ctx, userID); err != nil {
		return nil, err
	}
	rows, meta, err := s.users.FindFollowersPage(ctx, userID, currentUserID, params)
	return userFollowList(rows, meta, err)
}

type moderatedUserField struct {
	Field model.ModerationField
	Text  string
}

func changedUserFields(user *model.User, input UpdateUserInput) (map[string]any, []moderatedUserField) {
	fields := make(map[string]any)
	moderated := make([]moderatedUserField, 0, 2)
	if input.NameSet && input.Name != nil && user.Name != *input.Name {
		fields["name"] = *input.Name
		if *input.Name != "" {
			moderated = append(moderated, moderatedUserField{Field: model.ModerationFieldName, Text: *input.Name})
		}
	}
	if input.BioSet && !equalStrings(user.Bio, input.Bio) {
		fields["bio"] = input.Bio
		if input.Bio != nil {
			moderated = append(moderated, moderatedUserField{Field: model.ModerationFieldBio, Text: *input.Bio})
		}
	}
	if input.GenderSet {
		gender := genderPointer(input.Gender)
		if !equalGenders(user.Gender, gender) {
			fields["gender"] = gender
		}
	}
	return fields, moderated
}

func normalizeUserUpdate(input UpdateUserInput) (UpdateUserInput, error) {
	if input.NameSet {
		if input.Name == nil {
			return input, apierr.InvalidField("name", apierr.FieldInvalidFormat, "name 不能是 null")
		}
		value := strings.TrimSpace(*input.Name)
		if utf8.RuneCountInString(value) > 100 {
			return input, apierr.InvalidField("name", apierr.FieldTooLong, "昵称不能超过 100 个字符")
		}
		input.Name = &value
	}
	if input.BioSet && input.Bio != nil {
		value := strings.TrimSpace(*input.Bio)
		if utf8.RuneCountInString(value) > maxUserBioRunes {
			return input, apierr.InvalidField("bio", apierr.FieldTooLong, "简介不能超过 500 个字符")
		}
		input.Bio = optionalNonBlank(value)
	}
	if input.GenderSet && input.Gender != nil {
		value := model.Gender(strings.TrimSpace(*input.Gender))
		if value != model.GenderMale && value != model.GenderFemale && value != model.GenderOther {
			return input, apierr.InvalidField("gender", apierr.FieldInvalidEnum, "gender 取值不合法")
		}
		raw := string(value)
		input.Gender = &raw
	}
	if input.AvatarURLSet && input.AvatarURL != nil {
		value := strings.TrimSpace(*input.AvatarURL)
		input.AvatarURL = optionalNonBlank(value)
	}
	return input, nil
}

func (s *UserService) resolveAvatar(
	ctx context.Context,
	user *model.User,
	avatarURL *string,
) (*uint64, error) {
	var newID *uint64
	if avatarURL != nil {
		assets, err := s.posts.FindImagesByURLs(ctx, []string{*avatarURL})
		if err != nil {
			return nil, apierr.Internal(err)
		}
		if len(assets) != 1 {
			return nil, apierr.NotFound(apierr.BizImageNotFound, "头像图片")
		}
		id := assets[0].ID
		newID = &id
	}
	ids := make([]uint64, 0, 2)
	if user.AvatarImageAssetID != nil {
		ids = append(ids, *user.AvatarImageAssetID)
	}
	if newID != nil {
		ids = append(ids, *newID)
	}
	locked, err := s.posts.LockImagesByIDs(ctx, ids)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if newID == nil {
		return nil, nil
	}
	var selected *model.ImageAsset
	for index := range locked {
		if locked[index].ID == *newID {
			selected = &locked[index]
			break
		}
	}
	if selected == nil {
		return nil, apierr.NotFound(apierr.BizImageNotFound, "头像图片")
	}
	if selected.UploaderID == nil || *selected.UploaderID != user.ID {
		return nil, apierr.Forbidden(apierr.BizImageNotOwned, "只能使用自己上传的头像")
	}
	if selected.Purpose != model.ImagePurposeAvatar {
		return nil, apierr.BadRequest(apierr.BizImagePurposeWrong, "图片用途不是头像")
	}
	if selected.PublicURL != *avatarURL || model.IsPurgedImageURL(selected.PublicURL) ||
		(selected.Status != model.ImageStatusPending && selected.Status != model.ImageStatusReady) ||
		selected.Moderation != model.ModerationStatusPass {
		return nil, apierr.Conflict(apierr.BizImageNotApproved, "头像图片尚不可引用")
	}
	return newID, nil
}

func (s *UserService) moderateUserField(
	ctx context.Context,
	userID uint64,
	field model.ModerationField,
	content string,
) error {
	result, err := s.moderator.Review(ctx, ModerationRequest{
		Target: ModerationTargetUser, Field: &field, Text: content,
	})
	if err != nil {
		return err
	}
	if err := validateModerationResult(result); err != nil {
		return err
	}
	labels := pq.StringArray(result.Labels)
	if labels == nil {
		labels = pq.StringArray{}
	}
	record := &model.ModerationRecord{
		UserID: &userID, Field: &field, Scene: model.ModerationSceneText,
		Provider: result.Provider, ProviderJobID: result.ProviderJobID,
		Verdict: result.Verdict, Labels: labels, Score: result.Score,
		RawResponse: result.RawResponse, CreatedAt: time.Now().UTC(),
	}
	if err := s.users.CreateModerationRecord(ctx, record); err != nil {
		return apierr.Internal(err)
	}
	if result.Verdict == model.ModerationVerdictBlock {
		s.alerter.AlertUserContent(ctx, UserModerationAlert{
			UserID: userID, Field: field, Verdict: result.Verdict, Labels: append([]string{}, result.Labels...),
		})
	}
	return nil
}

func (s *UserService) postList(
	ctx context.Context,
	records []repository.PostRecord,
	meta pagination.Meta,
	currentUserID uint64,
) (*PostList, error) {
	relations, err := (&PostService{posts: s.posts}).loadRelations(ctx, records, currentUserID)
	if err != nil {
		return nil, err
	}
	items := make([]PostListItem, 0, len(records))
	for index := range records {
		items = append(items, buildPostListItem(&records[index], relations, currentUserID))
	}
	return &PostList{Posts: items, Pagination: meta}, nil
}

func (s *UserService) ensureUser(ctx context.Context, userID uint64) error {
	_, err := s.users.FindByID(ctx, userID)
	return userNotFoundError(err)
}

func (s *UserService) followResult(
	ctx context.Context,
	targetID uint64,
	isFollowing bool,
) (*FollowActionResult, error) {
	count, err := s.users.FollowerCount(ctx, targetID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return &FollowActionResult{IsFollowing: isFollowing, FollowerCount: count}, nil
}

func userFollowList(
	rows []repository.UserListRecord,
	meta pagination.Meta,
	err error,
) (*UserFollowList, error) {
	if err != nil {
		return nil, apierr.Internal(err)
	}
	items := make([]UserListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, UserListItem{
			ID: row.ID, Name: row.Name, AvatarURL: row.AvatarURL, Bio: row.Bio,
			Stats: userStats(row.PostCount, row.LikeCount, row.FavoriteCount,
				row.FollowerCount, row.FollowingCount),
			IsFollowing: row.IsFollowing,
		})
	}
	return &UserFollowList{Users: items, Pagination: meta}, nil
}

func buildUserProfile(record *repository.UserProfileRecord) UserProfile {
	return UserProfile{
		ID: record.ID, Name: record.Name, AvatarURL: record.AvatarURL,
		Bio: record.Bio, Gender: record.Gender,
		Stats: userStats(record.PostCount, record.LikeCount, record.FavoriteCount,
			record.FollowerCount, record.FollowingCount),
		IsFollowing: record.IsFollowing, CreatedAt: ptime.Time(record.CreatedAt),
	}
}

func userStats(post, like, favorite, follower, following int64) UserStats {
	return UserStats{
		PostCount: post, LikeCount: like, FavoriteCount: favorite,
		FollowerCount: follower, FollowingCount: following,
	}
}

func userPostStatus(raw string) (*model.PostStatus, error) {
	if raw == "" {
		return nil, nil
	}
	status := model.PostStatus(raw)
	if status != model.PostStatusDraft && status != model.PostStatusPending &&
		status != model.PostStatusApproved && status != model.PostStatusRejected {
		return nil, apierr.InvalidField("status", apierr.FieldInvalidEnum, "status 取值不合法")
	}
	return &status, nil
}

func userNotFoundError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, repository.ErrNotFound) {
		return apierr.NotFound(apierr.BizNotFound, "用户")
	}
	return apierr.Internal(err)
}

func optionalNonBlank(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func genderPointer(value *string) *model.Gender {
	if value == nil {
		return nil
	}
	gender := model.Gender(*value)
	return &gender
}

func equalStrings(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func equalGenders(left, right *model.Gender) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func equalIDs(left, right *uint64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
