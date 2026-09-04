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
	Users      []UserListItem        `json:"users"`
	Pagination pagination.CursorMeta `json:"pagination"`
}

// UserNameChangeView 是用户本人或用户管理端可见的 name 变更记录。
type UserNameChangeView struct {
	ID        uint64     `json:"id"`
	OldName   string     `json:"old_name"`
	NewName   string     `json:"new_name"`
	ChangedAt ptime.Time `json:"changed_at"`
}

// UserNameChangeHistory 是 name 变更记录列表。
type UserNameChangeHistory struct {
	Changes []UserNameChangeView `json:"changes"`
}

// UserService 实现用户资料、个人列表与关注关系。
type UserService struct {
	moderator     ContentModerator
	alerter       UserModerationAlerter
	users         repository.UserRepository
	posts         repository.PostRepository
	notifications repository.NotificationRepository
	sessions      repository.SessionRepository
	following     *pagination.CursorCodec
	followers     *pagination.CursorCodec
}

// NewUserService 创建用户服务。
func NewUserService(moderator ContentModerator, alerter UserModerationAlerter) *UserService {
	return newUserService(
		moderator,
		alerter,
		pagination.NewEphemeralCursorCodec("users.following"),
		pagination.NewEphemeralCursorCodec("users.followers"),
	)
}

// NewUserServiceWithCursorSecret 创建跨实例可互认关注列表游标的用户服务。
func NewUserServiceWithCursorSecret(
	moderator ContentModerator,
	alerter UserModerationAlerter,
	cursorSecret string,
) *UserService {
	return newUserService(
		moderator,
		alerter,
		pagination.NewCursorCodec(cursorSecret, "users.following"),
		pagination.NewCursorCodec(cursorSecret, "users.followers"),
	)
}

func newUserService(
	moderator ContentModerator,
	alerter UserModerationAlerter,
	following *pagination.CursorCodec,
	followers *pagination.CursorCodec,
) *UserService {
	if moderator == nil {
		moderator = UnavailableContentModerator{}
	}
	if alerter == nil {
		alerter = DiscardUserModerationAlerter{}
	}
	return &UserService{
		moderator: moderator, alerter: alerter,
		following: following, followers: followers,
	}
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

// NameHistory 只允许本人读取 name 变更历史；管理端通过 AdminService 的用户取证接口读取。
func (s *UserService) NameHistory(
	ctx context.Context, userID, currentUserID uint64,
) (*UserNameChangeHistory, error) {
	if userID != currentUserID {
		return nil, apierr.Forbidden(apierr.BizNotOwner, "只能查看自己的 name 修改记录")
	}
	if _, err := s.users.FindByID(ctx, userID); err != nil {
		return nil, userNotFoundError(err)
	}
	records, err := s.users.FindNameChangeRecords(ctx, userID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	changes := make([]UserNameChangeView, 0, len(records))
	for _, record := range records {
		changes = append(changes, UserNameChangeView{
			ID: record.ID, OldName: record.OldName, NewName: record.NewName,
			ChangedAt: ptime.Time(record.ChangedAt),
		})
	}
	return &UserNameChangeHistory{Changes: changes}, nil
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
	nameChanged := input.NameSet && input.Name != nil && user.Name != *input.Name
	if nameChanged {
		result, reviewErr := s.reviewUserField(ctx, user.ID, model.ModerationFieldName, *input.Name)
		if reviewErr != nil {
			return nil, reviewErr
		}
		if result.Verdict != model.ModerationVerdictPass {
			return nil, &persistedError{
				err: moderationVerdictError(result.Verdict, "name"),
			}
		}
	}
	if nameChanged {
		// ClaimName 保留稳定的业务错误；users 上的触发器仍是直写/导入场景的数据库兜底。
		if err := s.users.ClaimName(ctx, userID, *input.Name, time.Now().UTC()); err != nil {
			if repository.IsUniqueViolation(err, "uq_user_name_claims_name_lower") ||
				repository.IsUniqueViolation(err, "uq_users_name_lower") ||
				errors.Is(err, repository.ErrAlreadyExists) {
				return nil, apierr.Conflict(apierr.BizNameTaken, "name 已被占用")
			}
			return nil, apierr.Internal(err)
		}
	}
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
		if field.Field == model.ModerationFieldName {
			continue
		}
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
	records, meta, err := s.posts.FindAuthorPage(ctx, userID, parsed, userID == currentUserID, params)
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
	request pagination.CursorRequest,
	currentUserID uint64,
) (*UserFollowList, error) {
	if err := s.ensureUser(ctx, userID); err != nil {
		return nil, err
	}
	params, err := s.following.DecodeRequest(request)
	if err != nil {
		return nil, err
	}
	rows, hasMore, err := s.users.FindFollowingPage(ctx, userID, currentUserID, params)
	meta, err := userFollowCursorMeta(s.following, rows, params.Limit, hasMore, err)
	return userFollowList(rows, meta, err)
}

// Followers 返回指定用户的粉丝。
func (s *UserService) Followers(
	ctx context.Context,
	userID uint64,
	request pagination.CursorRequest,
	currentUserID uint64,
) (*UserFollowList, error) {
	if err := s.ensureUser(ctx, userID); err != nil {
		return nil, err
	}
	params, err := s.followers.DecodeRequest(request)
	if err != nil {
		return nil, err
	}
	rows, hasMore, err := s.users.FindFollowersPage(ctx, userID, currentUserID, params)
	meta, err := userFollowCursorMeta(s.followers, rows, params.Limit, hasMore, err)
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
		value, err := normalizeName(*input.Name)
		if err != nil {
			return input, err
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
		if !validGender(value) {
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
	_, err := s.reviewUserField(ctx, userID, field, content)
	return err
}

func (s *UserService) reviewUserField(
	ctx context.Context, userID uint64, field model.ModerationField, content string,
) (ModerationResult, error) {
	result, err := s.moderator.Review(ctx, ModerationRequest{
		Target: ModerationTargetUser, Field: &field, Text: content,
	})
	if err != nil {
		return ModerationResult{}, err
	}
	if err := validateModerationResult(result); err != nil {
		return ModerationResult{}, err
	}
	record := moderationRecordForUser(userID, field, result)
	if err := s.users.CreateModerationRecord(ctx, record); err != nil {
		return ModerationResult{}, apierr.Internal(err)
	}
	if result.Verdict == model.ModerationVerdictBlock {
		s.alerter.AlertUserContent(ctx, UserModerationAlert{
			UserID: userID, Field: field, Verdict: result.Verdict, Labels: append([]string{}, result.Labels...),
		})
	}
	return result, nil
}

func moderationRecordForUser(
	userID uint64, field model.ModerationField, result ModerationResult,
) *model.ModerationRecord {
	labels := pq.StringArray(result.Labels)
	if labels == nil {
		labels = pq.StringArray{}
	}
	return &model.ModerationRecord{
		UserID: &userID, Field: &field, Scene: model.ModerationSceneText,
		Provider: result.Provider, ProviderJobID: result.ProviderJobID,
		Verdict: result.Verdict, Labels: labels, Score: result.Score,
		RawResponse: result.RawResponse, CreatedAt: time.Now().UTC(),
	}
}

func moderationFieldPtr(field model.ModerationField) *model.ModerationField { return &field }

func moderationVerdictError(verdict model.ModerationVerdict, field string) error {
	if verdict == model.ModerationVerdictReview {
		return apierr.Conflict(apierr.BizContentUnderAudit, field+" 正在审核")
	}
	return apierr.Conflict(apierr.BizContentRejected, field+" 未通过内容审核")
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
	meta pagination.CursorMeta,
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

func userFollowCursorMeta(
	codec *pagination.CursorCodec,
	rows []repository.UserListRecord,
	limit int,
	hasMore bool,
	err error,
) (pagination.CursorMeta, error) {
	if err != nil {
		return pagination.CursorMeta{}, err
	}
	meta := pagination.CursorMeta{Limit: limit, HasMore: hasMore}
	if !hasMore || len(rows) == 0 {
		return meta, nil
	}
	last := rows[len(rows)-1]
	token, err := codec.Encode(pagination.Cursor{CreatedAt: last.FollowCreatedAt, ID: last.ID})
	if err != nil {
		return pagination.CursorMeta{}, apierr.Internal(err)
	}
	meta.NextCursor = &token
	return meta, nil
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
