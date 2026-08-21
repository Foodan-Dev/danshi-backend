package model

// UserRole 是用户权限角色。
type UserRole string

// 用户角色枚举值。
const (
	UserRoleUser       UserRole = "user"
	UserRoleAdmin      UserRole = "admin"
	UserRoleSuperAdmin UserRole = "super_admin"
)

// Gender 是用户性别。
type Gender string

// 用户性别枚举值。
const (
	GenderMale   Gender = "male"
	GenderFemale Gender = "female"
	GenderOther  Gender = "other"
)

// ModerationStatus 是内容对象当前的机审状态。
type ModerationStatus string

// 内容对象机审状态枚举值。
const (
	ModerationStatusPending ModerationStatus = "pending"
	ModerationStatusPass    ModerationStatus = "pass"
	ModerationStatusReview  ModerationStatus = "review"
	ModerationStatusBlock   ModerationStatus = "block"
)

// ImagePurpose 是图片资产的用途。
type ImagePurpose string

// 图片资产用途枚举值。
const (
	ImagePurposePost   ImagePurpose = "post"
	ImagePurposeAvatar ImagePurpose = "avatar"
)

// ImageStatus 是图片资产的引用生命周期状态。
type ImageStatus string

// 图片资产生命周期状态枚举值。
const (
	ImageStatusPending ImageStatus = "pending"
	ImageStatusReady   ImageStatus = "ready"
	ImageStatusRetired ImageStatus = "retired"
)

// PostType 是帖子的产品类型。
type PostType string

// 帖子产品类型枚举值。
const (
	PostTypeShare   PostType = "share"
	PostTypeSeeking PostType = "seeking"
)

// ShareType 是分享帖的推荐倾向。
type ShareType string

// 分享帖推荐倾向枚举值。
const (
	ShareTypeRecommend ShareType = "recommend"
	ShareTypeWarning   ShareType = "warning"
)

// PostStatus 是帖子的发布与审核状态。
type PostStatus string

// 帖子发布与审核状态枚举值。
const (
	PostStatusDraft    PostStatus = "draft"
	PostStatusPending  PostStatus = "pending"
	PostStatusApproved PostStatus = "approved"
	PostStatusRejected PostStatus = "rejected"
)

// PostCategory 是帖子的内容分类。
type PostCategory string

// 帖子内容分类枚举值。
const (
	PostCategoryFood   PostCategory = "food"
	PostCategoryRecipe PostCategory = "recipe"
)

// DeleteReason 是内容软删除的来源。
type DeleteReason string

// 内容软删除来源枚举值。
const (
	DeleteReasonAuthor     DeleteReason = "author"
	DeleteReasonAdmin      DeleteReason = "admin"
	DeleteReasonModeration DeleteReason = "moderation"
)

// FlavorStance 是帖子与口味的关系。
type FlavorStance string

// 帖子口味立场枚举值。
const (
	FlavorStanceHas    FlavorStance = "has"
	FlavorStancePrefer FlavorStance = "prefer"
	FlavorStanceAvoid  FlavorStance = "avoid"
)

// NotificationType 是站内通知类型。
type NotificationType string

// 站内通知类型枚举值。
const (
	NotificationTypeLikePost    NotificationType = "like_post"
	NotificationTypeLikeComment NotificationType = "like_comment"
	NotificationTypeComment     NotificationType = "comment"
	NotificationTypeReply       NotificationType = "reply"
	NotificationTypeMention     NotificationType = "mention"
	NotificationTypeFollow      NotificationType = "follow"
)

// VerificationPurpose 是邮箱验证码的用途。
type VerificationPurpose string

// 邮箱验证码用途枚举值。
const (
	VerificationPurposeRegistration VerificationPurpose = "registration"
)

// ModerationScene 是审核内容的媒介类型。
type ModerationScene string

// 审核媒介类型枚举值。
const (
	ModerationSceneText  ModerationScene = "text"
	ModerationSceneImage ModerationScene = "image"
)

// ModerationVerdict 是单次审核的结论。
type ModerationVerdict string

// 单次审核结论枚举值。
const (
	ModerationVerdictPass   ModerationVerdict = "pass"
	ModerationVerdictReview ModerationVerdict = "review"
	ModerationVerdictBlock  ModerationVerdict = "block"
)

// ModerationField 是多字段对象被审核的具体字段。
type ModerationField string

// 可审核字段枚举值。
const (
	ModerationFieldName    ModerationField = "name"
	ModerationFieldBio     ModerationField = "bio"
	ModerationFieldTitle   ModerationField = "title"
	ModerationFieldContent ModerationField = "content"
)

// ModerationProvider 是审核记录的来源实现。
//
// 供应商标识是开放集合；这里只为当前已知实现提供常量，不能据此拒绝新供应商。
type ModerationProvider string

// 当前已知审核后端标识。
const (
	ModerationProviderTencentCI ModerationProvider = "tencent_ci"
	ModerationProviderManual    ModerationProvider = "manual"
)

// SuggestionKind 是词条提议的目标字典类型。
type SuggestionKind string

// 词条提议目标类型枚举值。
const (
	SuggestionKindFlavor        SuggestionKind = "flavor"
	SuggestionKindCuisine       SuggestionKind = "cuisine"
	SuggestionKindCanteen       SuggestionKind = "canteen"
	SuggestionKindCanteenWindow SuggestionKind = "canteen_window"
)

// SuggestionStatus 是词条提议的审核状态。
type SuggestionStatus string

// 词条提议状态枚举值。
const (
	SuggestionStatusPending  SuggestionStatus = "pending"
	SuggestionStatusApproved SuggestionStatus = "approved"
	SuggestionStatusRejected SuggestionStatus = "rejected"
)
