package service

import (
	"encoding/json"
	"time"

	"github.com/shopspring/decimal"

	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/pagination"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/ptime"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

const adminRestoreProvider model.ModerationProvider = "admin_restore"

// AdminAuthorView 是管理端可见的作者信息。
type AdminAuthorView struct {
	ID    uint64 `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// AdminPostView 是管理端帖子列表项，保留软删除审计字段。
type AdminPostView struct {
	ID            uint64              `json:"id"`
	Title         string              `json:"title"`
	Content       string              `json:"content"`
	Category      model.PostCategory  `json:"category"`
	PostType      model.PostType      `json:"post_type"`
	Images        []string            `json:"images"`
	Author        AdminAuthorView     `json:"author"`
	Status        model.PostStatus    `json:"status"`
	LikeCount     int32               `json:"like_count"`
	ViewCount     int32               `json:"view_count"`
	CommentCount  int32               `json:"comment_count"`
	DeletedAt     *ptime.Time         `json:"deleted_at"`
	DeletedReason *model.DeleteReason `json:"deleted_reason"`
	DeletedBy     *uint64             `json:"deleted_by"`
	CreatedAt     ptime.Time          `json:"created_at"`
}

// AdminPostList 是管理端帖子页。
type AdminPostList struct {
	Posts      []AdminPostView `json:"posts"`
	Pagination pagination.Meta `json:"pagination"`
}

// AdminCommentView 是管理端评论列表项，原文在软删除后仍可见。
type AdminCommentView struct {
	ID            uint64              `json:"id"`
	Content       string              `json:"content"`
	PostID        uint64              `json:"post_id"`
	Author        AdminAuthorView     `json:"author"`
	ParentID      *uint64             `json:"parent_id"`
	LikeCount     int32               `json:"like_count"`
	ReplyCount    int32               `json:"reply_count"`
	DeletedAt     *ptime.Time         `json:"deleted_at"`
	DeletedReason *model.DeleteReason `json:"deleted_reason"`
	DeletedBy     *uint64             `json:"deleted_by"`
	CreatedAt     ptime.Time          `json:"created_at"`
}

// AdminCommentList 是管理端评论页。
type AdminCommentList struct {
	Comments   []AdminCommentView `json:"comments"`
	Pagination pagination.Meta    `json:"pagination"`
}

// AdminUserView 是管理端用户项，兼容 is_active 并暴露完整封禁形态。
type AdminUserView struct {
	ID             uint64           `json:"id"`
	Name           string           `json:"name"`
	Email          string           `json:"email"`
	Roles          []model.UserRole `json:"roles"`
	IsActive       bool             `json:"is_active"`
	IsBanned       bool             `json:"is_banned"`
	BanIsPermanent bool             `json:"ban_is_permanent"`
	BannedUntil    *ptime.Time      `json:"banned_until"`
	BanReason      *string          `json:"ban_reason"`
	BannedBy       *uint64          `json:"banned_by"`
	AvatarURL      *string          `json:"avatar_url"`
	Bio            *string          `json:"bio"`
	Stats          UserStats        `json:"stats"`
	DeletedAt      *ptime.Time      `json:"deleted_at"`
	CreatedAt      ptime.Time       `json:"created_at"`
}

// AdminUserList 是管理端用户页。
type AdminUserList struct {
	Users      []AdminUserView `json:"users"`
	Pagination pagination.Meta `json:"pagination"`
}

// AdminUserBanRecordView 是管理端单用户取证可见的一次封禁状态变更。
type AdminUserBanRecordView struct {
	ID             uint64              `json:"id"`
	Action         model.UserBanAction `json:"action"`
	BanIsPermanent bool                `json:"ban_is_permanent"`
	BannedUntil    *ptime.Time         `json:"banned_until"`
	Reason         *string             `json:"reason"`
	ActorID        *uint64             `json:"actor_id"`
	CreatedAt      ptime.Time          `json:"created_at"`
}

// AdminUserDetail 是单用户取证详情及其完整封禁历史。
type AdminUserDetail struct {
	AdminUserView
	BanRecords []AdminUserBanRecordView `json:"ban_records"`
}

// AdminImageView 是具备内容审核能力的角色可见的单张图片详情。
type AdminImageView struct {
	ID         uint64                 `json:"id"`
	UploaderID *uint64                `json:"uploader_id"`
	Purpose    model.ImagePurpose     `json:"purpose"`
	Status     model.ImageStatus      `json:"status"`
	Moderation model.ModerationStatus `json:"moderation"`
	ImageURL   *string                `json:"image_url"`
	CreatedAt  ptime.Time             `json:"created_at"`
}

// AdminModerationView 是通用待人工复核队列的一项。
type AdminModerationView struct {
	ID               uint64                   `json:"id"`
	TargetType       string                   `json:"target_type"`
	TargetID         uint64                   `json:"target_id"`
	Field            *model.ModerationField   `json:"field"`
	PostHistoryID    *uint64                  `json:"post_history_id"`
	CommentHistoryID *uint64                  `json:"comment_history_id"`
	Scene            model.ModerationScene    `json:"scene"`
	Provider         model.ModerationProvider `json:"provider"`
	ProviderJobID    *string                  `json:"provider_job_id"`
	Verdict          model.ModerationVerdict  `json:"verdict"`
	Labels           []string                 `json:"labels"`
	Score            *decimal.Decimal         `json:"score"`
	Content          *string                  `json:"content"`
	CreatedAt        ptime.Time               `json:"created_at"`
}

// AdminModerationList 是待人工复核页。
type AdminModerationList struct {
	Records    []AdminModerationView `json:"records"`
	Pagination pagination.Meta       `json:"pagination"`
}

// UpdateAdminUserStatusInput 同时支持 v2 封禁字段组与旧 is_active 入口。
type UpdateAdminUserStatusInput struct {
	IsActive       *bool
	BanIsPermanent *bool
	BannedUntil    *time.Time
	BanReason      *string
	LegacyReason   *string
}

// AdminUserStatusResult 是封禁或解封后的完整当前状态。
type AdminUserStatusResult struct {
	UserID         uint64      `json:"user_id"`
	IsActive       bool        `json:"is_active"`
	IsBanned       bool        `json:"is_banned"`
	BanIsPermanent bool        `json:"ban_is_permanent"`
	BannedUntil    *ptime.Time `json:"banned_until"`
	BanReason      *string     `json:"ban_reason"`
	BannedBy       *uint64     `json:"banned_by"`
}

// AdminUserRoleResult 是角色调整结果。
type AdminUserRoleResult struct {
	UserID  uint64               `json:"user_id"`
	Role    model.UserRole       `json:"role"`
	Action  model.UserRoleAction `json:"action"`
	Changed bool                 `json:"changed"`
	Roles   []model.UserRole     `json:"roles"`
}

// AdminPostReviewInput 是基线帖子审核端点的输入。
type AdminPostReviewInput struct {
	Status   string
	Feedback *string
}

// AdminManualReviewInput 是通用人工复核端点的输入。
type AdminManualReviewInput struct {
	Verdict     model.ModerationVerdict
	Labels      []string
	Score       *decimal.Decimal
	RawResponse json.RawMessage
}

// AdminReviewResult 是人工裁决追加成功后的结果。
type AdminReviewResult struct {
	ModerationRecordID uint64                  `json:"moderation_record_id"`
	TargetType         string                  `json:"target_type"`
	TargetID           uint64                  `json:"target_id"`
	Verdict            model.ModerationVerdict `json:"verdict"`
	ReviewedAt         ptime.Time              `json:"reviewed_at"`
}

// AdminPostReviewResult 保留基线帖子审核响应字段并附带审计行 id。
type AdminPostReviewResult struct {
	PostID             uint64           `json:"post_id"`
	Status             model.PostStatus `json:"status"`
	ReviewedAt         ptime.Time       `json:"reviewed_at"`
	ModerationRecordID uint64           `json:"moderation_record_id"`
}

// AdminPostDeleteResult 是管理员删帖响应。
type AdminPostDeleteResult struct {
	PostID uint64 `json:"post_id"`
}

// AdminCommentDeleteResult 是管理员删除评论响应。
type AdminCommentDeleteResult struct {
	CommentID uint64 `json:"comment_id"`
}

// AdminPostRestoreResult 是恢复帖子及其不可变审计行。
type AdminPostRestoreResult struct {
	PostID             uint64 `json:"post_id"`
	ModerationRecordID uint64 `json:"moderation_record_id"`
}

// AdminCommentRestoreResult 是恢复评论及其不可变审计行。
type AdminCommentRestoreResult struct {
	CommentID          uint64 `json:"comment_id"`
	ModerationRecordID uint64 `json:"moderation_record_id"`
}

// AdminService 实现管理端列表、审核、封禁、角色与软删除恢复。
type AdminService struct {
	admin          repository.AdminRepository
	posts          repository.PostRepository
	comments       repository.CommentRepository
	sessions       repository.SessionRepository
	users          repository.UserRepository
	assets         repository.UploadRepository
	moderation     *ModerationService
	imageStorage   ImageStorage
	signedImageTTL time.Duration
	tagCursor      *pagination.CursorCodec
}

// NewAdminService 创建管理端服务。
func NewAdminService(
	alerter ModerationAlerter,
	imageAccess ImageAccessController,
	imageStorage ImageStorage,
	signedImageTTL time.Duration,
) *AdminService {
	if imageStorage == nil {
		imageStorage = UnavailableImageStorage{}
	}
	return &AdminService{
		moderation:   NewModerationService(alerter, imageAccess),
		imageStorage: imageStorage, signedImageTTL: signedImageTTL,
		tagCursor: pagination.NewEphemeralCursorCodec("admin-tags"),
	}
}

// WithTagCursorSecret 将标签管理游标切换为跨实例可互认的运行时密钥。
func (s *AdminService) WithTagCursorSecret(cursorSecret string) *AdminService {
	s.tagCursor = pagination.NewCursorCodec(cursorSecret, "admin-tags")
	return s
}
