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
	RejectedPosts int64 `json:"rejected_posts"`
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

// ManualPostReviewInput 是管理员对一条帖子全部待处理审核对象的一次统一裁决。
type ManualPostReviewInput struct {
	PostID      uint64
	ReviewerID  uint64
	Verdict     model.ModerationVerdict
	RawResponse json.RawMessage
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
	if err := s.validateSingleReviewScope(ctx, snapshot); err != nil {
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
		Field: original.Field, Scene: original.Scene,
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
		if err := s.applyManualImageVerdict(
			ctx, *original.ImageAssetID, imagePostIDs, imageAssets, record.ID, input.Verdict,
		); err != nil {
			return nil, apierr.Internal(err)
		}
	} else if err := s.moderation.ApplyManualTextVerdict(ctx, original, input.Verdict); err != nil {
		return nil, apierr.Internal(err)
	}
	if input.Verdict != model.ModerationVerdictPass {
		s.alerter.Alert(ctx, manualReviewAlert(original, input, record))
	}
	return record, nil
}

func (s *ModerationService) applyManualImageVerdict(
	ctx context.Context,
	imageAssetID uint64,
	imagePostIDs []uint64,
	imageAssets []model.ImageAsset,
	sourceModerationRecordID uint64,
	verdict model.ModerationVerdict,
) error {
	if err := s.moderation.UpdateImageModeration(
		ctx, imageAssetID, model.ModerationStatus(verdict),
	); err != nil {
		return err
	}
	if _, err := s.moderation.ReconcilePendingPosts(ctx, imagePostIDs); err != nil {
		return err
	}
	return s.applyImageAccess(
		ctx, imageAssetByID(imageAssets, imageAssetID), sourceModerationRecordID, verdict,
	)
}

func (s *ModerationService) validateSingleReviewScope(
	ctx context.Context,
	record *model.ModerationRecord,
) error {
	if record.PostID != nil {
		return apierr.Conflict(
			apierr.BizConflict,
			"帖子审核记录必须通过整帖复核端点一次性处理",
		)
	}
	if record.ImageAssetID == nil {
		return nil
	}
	postIDs, err := s.moderation.PendingPostIDsForImages(ctx, []uint64{*record.ImageAssetID})
	if err != nil {
		return apierr.Internal(err)
	}
	if len(postIDs) > 0 {
		return apierr.Conflict(
			apierr.BizConflict,
			"帖子附图审核记录必须随所属帖子一次性处理",
		)
	}
	return nil
}

// ManualReviewPost 对正文与全部附图的当前 review 记录逐条追加 manual 行并重算整帖状态。
func (s *ModerationService) ManualReviewPost(
	ctx context.Context,
	input ManualPostReviewInput,
) ([]model.ModerationRecord, error) {
	if err := validateManualPostReviewInput(input); err != nil {
		return nil, err
	}
	snapshot, err := s.lockPostReviewSnapshot(ctx, input.PostID)
	if err != nil {
		return nil, err
	}
	records, originals, imageIDs, err := s.createPostManualRecords(ctx, input, snapshot)
	if err != nil {
		return nil, err
	}
	if err := s.applyPostManualVerdict(ctx, input, snapshot, imageIDs, records); err != nil {
		return nil, err
	}
	if input.Verdict == model.ModerationVerdictBlock {
		s.alertPostManualBlock(ctx, input, originals, records)
	}
	return records, nil
}

type postReviewSnapshot struct {
	assets     []model.ImageAsset
	assetByID  map[uint64]*model.ImageAsset
	latestText *model.ModerationRecord
	pending    []model.ModerationRecord
}

func validateManualPostReviewInput(input ManualPostReviewInput) error {
	if input.PostID == 0 || input.ReviewerID == 0 {
		return apierr.InvalidField(
			"post_id", apierr.FieldInvalidFormat, "帖子与复核人必须是正整数",
		)
	}
	if input.Verdict != model.ModerationVerdictPass &&
		input.Verdict != model.ModerationVerdictBlock {
		return apierr.InvalidField(
			"status", apierr.FieldInvalidEnum, "人工复核结论必须是 pass 或 block",
		)
	}
	if len(input.RawResponse) > 0 && !json.Valid(input.RawResponse) {
		return apierr.InvalidField(
			"feedback", apierr.FieldInvalidFormat, "人工复核反馈必须是合法 JSON",
		)
	}
	return nil
}

func (s *ModerationService) lockPostReviewSnapshot(
	ctx context.Context,
	postID uint64,
) (*postReviewSnapshot, error) {
	post, err := s.moderation.LockPostForReview(ctx, postID)
	if err != nil {
		return nil, repository.ToAPIError(err, apierr.BizPostNotFound, "帖子")
	}
	if post.DeletedAt != nil {
		return nil, apierr.NotFound(apierr.BizPostDeleted, "帖子")
	}
	if post.Status != model.PostStatusPending {
		return nil, apierr.Conflict(apierr.BizModerationNotPending, "帖子当前不在待审核状态")
	}
	assets, err := s.moderation.LockImagesForPost(ctx, postID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if err := validatePostReviewAssets(assets); err != nil {
		return nil, err
	}
	latestText, err := s.moderation.LatestPostModeration(ctx, postID)
	if err != nil {
		return nil, repository.ToAPIError(err, apierr.BizModerationNotPending, "帖子审核记录")
	}
	if latestText.Verdict == model.ModerationVerdictBlock {
		return nil, apierr.Conflict(
			apierr.BizModerationNotPending,
			"帖子正文已被机器禁止，不应进入人工复核",
		)
	}
	pending, err := s.moderation.LockPendingReviewRecordsForPost(ctx, postID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	assetByID := make(map[uint64]*model.ImageAsset, len(assets))
	for index := range assets {
		assetByID[assets[index].ID] = &assets[index]
	}
	return &postReviewSnapshot{
		assets: assets, assetByID: assetByID, latestText: latestText, pending: pending,
	}, nil
}

func validatePostReviewAssets(assets []model.ImageAsset) error {
	for index := range assets {
		switch assets[index].Moderation {
		case model.ModerationStatusPass, model.ModerationStatusReview:
		case model.ModerationStatusPending:
			return apierr.Conflict(
				apierr.BizModerationNotPending,
				"帖子仍有图片正在机审，暂不能人工复核",
			)
		case model.ModerationStatusBlock:
			return apierr.Conflict(
				apierr.BizModerationNotPending,
				"帖子包含已被机器禁止的图片，不应进入人工复核",
			)
		default:
			return apierr.Internal(errors.New("图片存在未知审核状态"))
		}
	}
	return nil
}

func (s *ModerationService) createPostManualRecords(
	ctx context.Context,
	input ManualPostReviewInput,
	snapshot *postReviewSnapshot,
) ([]model.ModerationRecord, []*model.ModerationRecord, []uint64, error) {
	records := make([]model.ModerationRecord, 0, len(snapshot.pending))
	originals := make([]*model.ModerationRecord, 0, len(snapshot.pending))
	imageIDs := make([]uint64, 0, len(snapshot.pending))
	now := time.Now().UTC()
	for index := range snapshot.pending {
		original := &snapshot.pending[index]
		if err := validateMachineReviewRecord(original); err != nil {
			return nil, nil, nil, err
		}
		if original.PostID != nil && original.ID != snapshot.latestText.ID {
			continue
		}
		if original.ImageAssetID != nil {
			asset := snapshot.assetByID[*original.ImageAssetID]
			if asset == nil || asset.Moderation != model.ModerationStatusReview {
				continue
			}
			imageIDs = append(imageIDs, asset.ID)
		}
		record := postManualRecord(original, input, now)
		if err := s.moderation.CreateManualRecord(ctx, &record); err != nil {
			if repository.IsUniqueViolation(err, "uq_mr_supersedes") {
				return nil, nil, nil, apierr.Conflict(
					apierr.BizConflict, "帖子中有机器审核记录已经复核",
				)
			}
			return nil, nil, nil, apierr.Internal(err)
		}
		records = append(records, record)
		originals = append(originals, original)
	}
	if len(records) == 0 {
		return nil, nil, nil, apierr.Conflict(
			apierr.BizModerationNotPending,
			"帖子没有待人工复核的当前机审记录",
		)
	}
	return records, originals, uniqueIDs(imageIDs), nil
}

func postManualRecord(
	original *model.ModerationRecord,
	input ManualPostReviewInput,
	now time.Time,
) model.ModerationRecord {
	return model.ModerationRecord{
		PostID: original.PostID, CommentID: original.CommentID,
		ImageAssetID: original.ImageAssetID, TagID: original.TagID, UserID: original.UserID,
		Field: original.Field, Scene: original.Scene,
		Provider: model.ModerationProviderManual, Verdict: input.Verdict,
		Labels: pq.StringArray{}, RawResponse: input.RawResponse,
		ReviewerID: &input.ReviewerID, ReviewedAt: &now,
		SupersedesID: &original.ID, CreatedAt: now,
	}
}

func (s *ModerationService) applyPostManualVerdict(
	ctx context.Context,
	input ManualPostReviewInput,
	snapshot *postReviewSnapshot,
	imageIDs []uint64,
	records []model.ModerationRecord,
) error {
	for _, imageID := range imageIDs {
		if err := s.moderation.UpdateImageModeration(
			ctx, imageID, model.ModerationStatus(input.Verdict),
		); err != nil {
			return apierr.Internal(err)
		}
	}
	postIDs, err := s.moderation.PendingPostIDsForImages(ctx, imageIDs)
	if err != nil {
		return apierr.Internal(err)
	}
	postIDs = append(postIDs, input.PostID)
	if _, err := s.moderation.ReconcilePendingPosts(ctx, postIDs); err != nil {
		return apierr.Internal(err)
	}
	for index := range records {
		if records[index].ImageAssetID == nil {
			continue
		}
		if err := s.applyImageAccess(
			ctx, snapshot.assetByID[*records[index].ImageAssetID], records[index].ID, input.Verdict,
		); err != nil {
			return apierr.Internal(err)
		}
	}
	return nil
}

func (s *ModerationService) alertPostManualBlock(
	ctx context.Context,
	input ManualPostReviewInput,
	originals []*model.ModerationRecord,
	records []model.ModerationRecord,
) {
	for index := range records {
		s.alerter.Alert(ctx, manualReviewAlert(originals[index], ManualReviewInput{
			ReviewerID: input.ReviewerID, Verdict: input.Verdict,
		}, &records[index]))
	}
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
		return s.reconcileDuplicateImageResult(ctx, callback, asset)
	}
	status := model.ModerationStatus(callback.Verdict)
	if err := s.moderation.UpdateImageModeration(ctx, callback.ImageAssetID, status); err != nil {
		return nil, repository.ToAPIError(err, apierr.BizImageNotFound, "图片")
	}
	transitions, err := s.moderation.ReconcilePendingPosts(ctx, postIDs)
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
	if err := s.applyImageAccess(ctx, asset, record.ID, callback.Verdict); err != nil {
		return nil, apierr.Internal(err)
	}
	if err := (repository.ImageModerationRetryRepository{}).DeleteForAsset(
		ctx, callback.ImageAssetID,
	); err != nil {
		return nil, apierr.Internal(err)
	}
	return &ImageModerationApplyResult{
		ApprovedPosts: transitions.Approved,
		RejectedPosts: transitions.Rejected,
	}, nil
}

func (s *ModerationService) reconcileDuplicateImageResult(
	ctx context.Context,
	callback ImageModerationCallback,
	asset *model.ImageAsset,
) (*ImageModerationApplyResult, error) {
	existing, err := s.moderation.FindMachineRecordByProviderJobID(
		ctx, callback.Provider, callback.ProviderJobID,
	)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if existing.ImageAssetID == nil || *existing.ImageAssetID != callback.ImageAssetID {
		return nil, apierr.Conflict(
			apierr.BizModerationCallbackInvalid,
			"审核回调任务号与图片不一致",
		)
	}
	if err := s.reconcileImageAccess(ctx, asset, existing.ID); err != nil {
		return nil, apierr.Internal(err)
	}
	if err := (repository.ImageModerationRetryRepository{}).DeleteForAsset(
		ctx, callback.ImageAssetID,
	); err != nil {
		return nil, apierr.Internal(err)
	}
	return &ImageModerationApplyResult{Duplicate: true}, nil
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
	sourceModerationRecordID uint64,
	verdict model.ModerationVerdict,
) error {
	if asset == nil {
		return nil
	}
	public := verdict == model.ModerationVerdictPass
	return s.imageAccess.Apply(ctx, ImageAccessChange{
		ImageAssetID: asset.ID, SourceModerationRecordID: sourceModerationRecordID,
		Public: public, PurgeRequired: imageAccessPurgeRequired(public, asset.Moderation),
	})
}

func (s *ModerationService) reconcileImageAccess(
	ctx context.Context,
	asset *model.ImageAsset,
	sourceModerationRecordID uint64,
) error {
	if asset == nil {
		return nil
	}
	public := asset.Moderation == model.ModerationStatusPass
	return s.imageAccess.Apply(ctx, ImageAccessChange{
		ImageAssetID: asset.ID, SourceModerationRecordID: sourceModerationRecordID,
		Public: public, PurgeRequired: imageAccessPurgeRequired(public, asset.Moderation),
	})
}

func imageAccessPurgeRequired(public bool, previous model.ModerationStatus) bool {
	return !public || previous == model.ModerationStatusBlock ||
		previous == model.ModerationStatusReview
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
