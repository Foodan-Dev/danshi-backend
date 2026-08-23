package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/shopspring/decimal"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

// ImageModerationApplyResult 描述回调是否为重复投递及补发布数量。
type ImageModerationApplyResult struct {
	Duplicate     bool  `json:"duplicate"`
	ApprovedPosts int64 `json:"approved_posts"`
}

// ManualReviewInput 是管理员对一条机器 review 记录作最终裁决的输入。
type ManualReviewInput struct {
	MachineRecordID uint64
	ReviewerID      uint64
	Verdict         model.ModerationVerdict
	Labels          []string
	Score           *decimal.Decimal
	RawResponse     json.RawMessage
}

// ModerationService 处理异步结论的幂等落库与目标状态写回。
type ModerationService struct {
	alerter     ModerationAlerter
	imageAccess ImageAccessController
	moderation  repository.ModerationRepository
}

// NewModerationService 创建审核服务。
func NewModerationService(
	alerter ModerationAlerter,
	imageAccess ImageAccessController,
) *ModerationService {
	if alerter == nil {
		alerter = DiscardModerationAlerter{}
	}
	if imageAccess == nil {
		imageAccess = DiscardImageAccessController{}
	}
	return &ModerationService{alerter: alerter, imageAccess: imageAccess}
}

// ManualReview 追加 manual 行并用 supersedes_id 指回原机器记录；原行永不更新。
func (s *ModerationService) ManualReview(
	ctx context.Context,
	input ManualReviewInput,
) (*model.ModerationRecord, error) {
	if input.MachineRecordID == 0 || input.ReviewerID == 0 {
		return nil, apierr.InvalidField(
			"moderation_record_id", apierr.FieldInvalidFormat, "复核记录与复核人必须是正整数",
		)
	}
	if input.Verdict != model.ModerationVerdictPass &&
		input.Verdict != model.ModerationVerdictBlock {
		return nil, apierr.InvalidField(
			"verdict", apierr.FieldInvalidEnum, "人工复核结论必须是 pass 或 block",
		)
	}
	if input.Score != nil && (input.Score.IsNegative() || input.Score.GreaterThan(decimal.NewFromInt(100))) {
		return nil, apierr.InvalidField(
			"score", apierr.FieldOutOfRange, "人工复核分数必须在 0 到 100 之间",
		)
	}
	if len(input.RawResponse) > 0 && !json.Valid(input.RawResponse) {
		return nil, apierr.InvalidField(
			"raw_response", apierr.FieldInvalidFormat, "人工复核原始信息必须是合法 JSON",
		)
	}
	snapshot, err := s.moderation.FindRecordForReview(ctx, input.MachineRecordID)
	if err != nil {
		return nil, repository.ToAPIError(err, apierr.BizNotFound, "审核记录")
	}
	if err := validateMachineReviewRecord(snapshot); err != nil {
		return nil, err
	}
	var imagePostIDs []uint64
	var imageAssets []model.ImageAsset
	if snapshot.ImageAssetID != nil {
		imagePostIDs, err = s.moderation.LockPendingPostsForImage(ctx, *snapshot.ImageAssetID)
		if err == nil {
			imageAssets, err = s.moderation.LockImagesForPosts(ctx, *snapshot.ImageAssetID, imagePostIDs)
		}
	} else {
		err = s.moderation.LockManualTarget(ctx, snapshot)
	}
	if err != nil {
		return nil, apierr.Internal(err)
	}
	original, err := s.moderation.LockRecordForReview(ctx, input.MachineRecordID)
	if err != nil {
		return nil, repository.ToAPIError(err, apierr.BizNotFound, "审核记录")
	}
	if err := validateMachineReviewRecord(original); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	labels := pq.StringArray(input.Labels)
	if labels == nil {
		labels = pq.StringArray{}
	}
	record := &model.ModerationRecord{
		PostID: original.PostID, CommentID: original.CommentID,
		ImageAssetID: original.ImageAssetID, TagID: original.TagID, UserID: original.UserID,
		Field: original.Field, PostHistoryID: original.PostHistoryID,
		CommentHistoryID: original.CommentHistoryID, Scene: original.Scene,
		Provider: model.ModerationProviderManual, Verdict: input.Verdict,
		Labels: labels, Score: input.Score, RawResponse: input.RawResponse,
		ReviewerID: &input.ReviewerID, ReviewedAt: &now,
		SupersedesID: &original.ID, CreatedAt: now,
	}
	if err := s.moderation.CreateManualRecord(ctx, record); err != nil {
		if repository.IsUniqueViolation(err, "uq_mr_supersedes") {
			return nil, apierr.Conflict(apierr.BizConflict, "该机器审核记录已经复核")
		}
		return nil, apierr.Internal(err)
	}
	if original.ImageAssetID != nil {
		status := model.ModerationStatus(input.Verdict)
		if err := s.moderation.UpdateImageModeration(ctx, *original.ImageAssetID, status); err != nil {
			return nil, apierr.Internal(err)
		}
		if _, err := s.moderation.ApproveEligiblePendingPosts(ctx, imagePostIDs); err != nil {
			return nil, apierr.Internal(err)
		}
		asset := imageAssetByID(imageAssets, *original.ImageAssetID)
		s.applyImageAccess(ctx, asset, input.Verdict)
	} else if err := s.moderation.ApplyManualTextVerdict(ctx, original, input.Verdict); err != nil {
		return nil, apierr.Internal(err)
	}
	if input.Verdict != model.ModerationVerdictPass {
		s.alerter.Alert(ctx, manualReviewAlert(original, input, record))
	}
	return record, nil
}

func validateMachineReviewRecord(record *model.ModerationRecord) error {
	if record.Provider == model.ModerationProviderManual ||
		record.Verdict != model.ModerationVerdictReview {
		return apierr.Conflict(apierr.BizConflict, "只能复核机器判定为 review 的记录")
	}
	return nil
}

func manualReviewAlert(
	original *model.ModerationRecord,
	input ManualReviewInput,
	record *model.ModerationRecord,
) ModerationAlert {
	target, targetID := moderationRecordTarget(original)
	return ModerationAlert{
		Target: target, TargetID: targetID, Field: original.Field,
		Provider: model.ModerationProviderManual, Verdict: input.Verdict,
		Labels: append([]string{}, record.Labels...),
	}
}

func moderationRecordTarget(record *model.ModerationRecord) (ModerationTarget, uint64) {
	switch {
	case record.PostID != nil:
		return ModerationTargetPost, *record.PostID
	case record.CommentID != nil:
		return ModerationTargetComment, *record.CommentID
	case record.ImageAssetID != nil:
		return ModerationTargetImage, *record.ImageAssetID
	case record.TagID != nil:
		return ModerationTargetTag, *record.TagID
	case record.UserID != nil:
		return ModerationTargetUser, *record.UserID
	default:
		return "", 0
	}
}

// ApplyImageCallback 幂等追加图片审核流水、写回图片并补发布待审帖子。
func (s *ModerationService) ApplyImageCallback(
	ctx context.Context,
	callback ImageModerationCallback,
) (*ImageModerationApplyResult, error) {
	return s.applyImageResult(ctx, callback, true)
}

// ApplyImageResult 写入开发或同步供应商直接返回的图片结论。
func (s *ModerationService) ApplyImageResult(
	ctx context.Context,
	callback ImageModerationCallback,
) (*ImageModerationApplyResult, error) {
	return s.applyImageResult(ctx, callback, false)
}

func (s *ModerationService) applyImageResult(
	ctx context.Context,
	callback ImageModerationCallback,
	requireJobID bool,
) (*ImageModerationApplyResult, error) {
	if err := validateImageCallback(callback, requireJobID); err != nil {
		return nil, err
	}
	postIDs, err := s.moderation.LockPendingPostsForImage(ctx, callback.ImageAssetID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	assets, err := s.moderation.LockImagesForPosts(ctx, callback.ImageAssetID, postIDs)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	asset, err := validateCallbackAsset(callback, assets)
	if err != nil {
		return nil, err
	}
	record := imageModerationRecord(callback)
	created, err := s.moderation.CreateMachineRecordIfNew(ctx, record)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if !created {
		existing, findErr := s.moderation.FindMachineRecordByProviderJobID(
			ctx, callback.Provider, callback.ProviderJobID,
		)
		if findErr != nil {
			return nil, apierr.Internal(findErr)
		}
		if existing.ImageAssetID == nil || *existing.ImageAssetID != callback.ImageAssetID {
			return nil, apierr.Conflict(
				apierr.BizModerationCallbackInvalid,
				"审核回调任务号与图片不一致",
			)
		}
		s.reconcileImageAccess(ctx, asset)
		return &ImageModerationApplyResult{Duplicate: true}, nil
	}
	status := model.ModerationStatus(callback.Verdict)
	if err := s.moderation.UpdateImageModeration(ctx, callback.ImageAssetID, status); err != nil {
		return nil, repository.ToAPIError(err, apierr.BizImageNotFound, "图片")
	}
	approved, err := s.moderation.ApproveEligiblePendingPosts(ctx, postIDs)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if callback.Verdict != model.ModerationVerdictPass {
		s.alerter.Alert(ctx, ModerationAlert{
			Target: ModerationTargetImage, TargetID: callback.ImageAssetID,
			Provider: callback.Provider, ProviderJobID: &callback.ProviderJobID,
			Verdict: callback.Verdict, Labels: append([]string{}, callback.Labels...),
		})
	}
	s.applyImageAccess(ctx, asset, callback.Verdict)
	return &ImageModerationApplyResult{ApprovedPosts: approved}, nil
}

func validateImageCallback(callback ImageModerationCallback, requireJobID bool) error {
	if callback.ImageAssetID == 0 || strings.TrimSpace(string(callback.Provider)) == "" {
		return apierr.BadRequest(apierr.BizModerationCallbackInvalid, "审核回调缺少必要标识")
	}
	if requireJobID && strings.TrimSpace(callback.ProviderJobID) == "" {
		return apierr.BadRequest(apierr.BizModerationCallbackInvalid, "审核回调缺少外部任务号")
	}
	if callback.Verdict != model.ModerationVerdictPass &&
		callback.Verdict != model.ModerationVerdictReview &&
		callback.Verdict != model.ModerationVerdictBlock {
		return apierr.BadRequest(apierr.BizModerationCallbackInvalid, "审核回调结论无效")
	}
	return nil
}

func validateCallbackAsset(
	callback ImageModerationCallback,
	assets []model.ImageAsset,
) (*model.ImageAsset, error) {
	for i := range assets {
		if assets[i].ID != callback.ImageAssetID {
			continue
		}
		if callback.ObjectKey != "" && callback.ObjectKey != assets[i].ObjectKey {
			return nil, apierr.Conflict(apierr.BizModerationCallbackInvalid, "审核回调对象与上传记录不一致")
		}
		if assets[i].PublicURL == "" || model.IsPurgedImageURL(assets[i].PublicURL) {
			return nil, apierr.Conflict(apierr.BizModerationCallbackInvalid, "审核回调对应的上传尚未完成")
		}
		return &assets[i], nil
	}
	return nil, apierr.NotFound(apierr.BizImageNotFound, "图片")
}

func imageAssetByID(assets []model.ImageAsset, imageAssetID uint64) *model.ImageAsset {
	for index := range assets {
		if assets[index].ID == imageAssetID {
			return &assets[index]
		}
	}
	return nil
}

func (s *ModerationService) applyImageAccess(
	ctx context.Context,
	asset *model.ImageAsset,
	verdict model.ModerationVerdict,
) {
	if asset == nil {
		return
	}
	public := verdict == model.ModerationVerdictPass
	if public && asset.Moderation != model.ModerationStatusBlock &&
		asset.Moderation != model.ModerationStatusReview {
		return
	}
	s.imageAccess.Apply(ctx, ImageAccessChange{
		ImageAssetID: asset.ID,
		ObjectKey:    asset.ObjectKey,
		PublicURL:    asset.PublicURL,
		Public:       public,
	})
}

func (s *ModerationService) reconcileImageAccess(ctx context.Context, asset *model.ImageAsset) {
	if asset == nil {
		return
	}
	s.imageAccess.Apply(ctx, ImageAccessChange{
		ImageAssetID: asset.ID,
		ObjectKey:    asset.ObjectKey,
		PublicURL:    asset.PublicURL,
		Public:       asset.Moderation == model.ModerationStatusPass,
	})
}

func imageModerationRecord(callback ImageModerationCallback) *model.ModerationRecord {
	var jobID *string
	if callback.ProviderJobID != "" {
		value := callback.ProviderJobID
		jobID = &value
	}
	labels := pq.StringArray(callback.Labels)
	if labels == nil {
		labels = pq.StringArray{}
	}
	return &model.ModerationRecord{
		ImageAssetID: &callback.ImageAssetID, Scene: model.ModerationSceneImage,
		Provider: callback.Provider, ProviderJobID: jobID, Verdict: callback.Verdict,
		Labels: labels, Score: callback.Score, RawResponse: callback.RawResponse,
		CreatedAt: time.Now().UTC(),
	}
}

var errImmediateImageResultMissingJobID = errors.New("异步图片审核未返回任务号")
