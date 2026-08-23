package repository

import (
	"context"
	"errors"
	"sort"
	"time"

	"gorm.io/gorm/clause"

	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
)

// ModerationRepository 是审核回调与人工复核使用的持久化边界。
type ModerationRepository struct{}

// LockPostForReview 锁定整帖人工复核的帖子主体。
func (ModerationRepository) LockPostForReview(
	ctx context.Context,
	postID uint64,
) (*model.Post, error) {
	var post model.Post
	err := db.FromContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", postID).First(&post).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &post, nil
}

// LockImagesForPost 按资产 id 升序锁定一条帖子的全部当前附图。
func (ModerationRepository) LockImagesForPost(
	ctx context.Context,
	postID uint64,
) ([]model.ImageAsset, error) {
	assets := make([]model.ImageAsset, 0)
	err := db.FromContext(ctx).Table("image_assets AS image").Select("image.*").
		Joins("JOIN post_images AS pi ON pi.image_asset_id = image.id").
		Where("pi.post_id = ?", postID).Order("image.id").
		Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "image"}}).
		Scan(&assets).Error
	return assets, err
}

// LatestPostModeration 返回帖子正文当前生效的最新审核结论。
func (ModerationRepository) LatestPostModeration(
	ctx context.Context,
	postID uint64,
) (*model.ModerationRecord, error) {
	var record model.ModerationRecord
	err := db.FromContext(ctx).Where("post_id = ?", postID).
		Order("created_at DESC, id DESC").First(&record).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &record, nil
}

// LockPendingReviewRecordsForPost 锁定正文及当前全部附图尚未处理的机器 review 记录。
func (ModerationRepository) LockPendingReviewRecordsForPost(
	ctx context.Context,
	postID uint64,
) ([]model.ModerationRecord, error) {
	records := make([]model.ModerationRecord, 0)
	err := pendingModerationQuery(ctx).
		Where(`mr.post_id = ? OR EXISTS (
			SELECT 1 FROM post_images AS pi
			WHERE pi.post_id = ? AND pi.image_asset_id = mr.image_asset_id
		)`, postID, postID).
		Order("mr.id").
		Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "mr"}}).
		Find(&records).Error
	return records, err
}

// PendingPostIDsForImages 返回仍待审且引用任一目标图片的帖子 id。
func (ModerationRepository) PendingPostIDsForImages(
	ctx context.Context,
	imageAssetIDs []uint64,
) ([]uint64, error) {
	if len(imageAssetIDs) == 0 {
		return []uint64{}, nil
	}
	var postIDs []uint64
	err := db.FromContext(ctx).Table("posts AS p").Distinct("p.id").
		Joins("JOIN post_images AS pi ON pi.post_id = p.id").
		Where("pi.image_asset_id IN ? AND p.status = ? AND p.deleted_at IS NULL",
			imageAssetIDs, model.PostStatusPending).
		Order("p.id").Pluck("p.id", &postIDs).Error
	return postIDs, err
}

// FindRecordForReview 读取不可变机器记录，用于确定统一锁序中的目标对象。
func (ModerationRepository) FindRecordForReview(
	ctx context.Context,
	moderationRecordID uint64,
) (*model.ModerationRecord, error) {
	var record model.ModerationRecord
	err := db.FromContext(ctx).Where("id = ?", moderationRecordID).First(&record).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &record, nil
}

// LockRecordForReview 锁定一条待人工复核的机器记录。
func (ModerationRepository) LockRecordForReview(
	ctx context.Context,
	moderationRecordID uint64,
) (*model.ModerationRecord, error) {
	var record model.ModerationRecord
	err := db.FromContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", moderationRecordID).First(&record).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &record, nil
}

// LockManualTarget 在审核记录之前锁目标，保持与各业务域写路径相同的锁序。
func (ModerationRepository) LockManualTarget(
	ctx context.Context,
	record *model.ModerationRecord,
) error {
	tx := db.FromContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"})
	var err error
	switch {
	case record.PostID != nil:
		err = tx.Where("id = ?", *record.PostID).First(&model.Post{}).Error
	case record.CommentID != nil:
		err = tx.Where("id = ?", *record.CommentID).First(&model.Comment{}).Error
	case record.TagID != nil:
		err = tx.Where("id = ?", *record.TagID).First(&model.Tag{}).Error
	case record.UserID != nil:
		err = tx.Where("id = ?", *record.UserID).First(&model.User{}).Error
	case record.ImageAssetID != nil:
		err = errors.New("图片人工复核必须先按帖子、资产顺序锁定")
	default:
		err = ErrNotFound
	}
	return NormalizeError(err)
}

// LockPendingPostsForImage 按 id 升序锁定引用指定图片的待审帖子。
func (ModerationRepository) LockPendingPostsForImage(
	ctx context.Context,
	imageAssetID uint64,
) ([]uint64, error) {
	var postIDs []uint64
	err := db.FromContext(ctx).Table("posts AS p").
		Select("p.id").
		Joins("JOIN post_images AS pi ON pi.post_id = p.id").
		Where("pi.image_asset_id = ? AND p.status = ? AND p.deleted_at IS NULL",
			imageAssetID, model.PostStatusPending).
		Order("p.id").Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "p"}}).
		Pluck("p.id", &postIDs).Error
	return postIDs, err
}

// LockImagesForPosts 锁定目标图片及候选帖子引用的全部图片，锁序固定为资产 id 升序。
func (ModerationRepository) LockImagesForPosts(
	ctx context.Context,
	imageAssetID uint64,
	postIDs []uint64,
) ([]model.ImageAsset, error) {
	imageIDs := []uint64{imageAssetID}
	if len(postIDs) > 0 {
		var related []uint64
		if err := db.FromContext(ctx).Model(&model.PostImage{}).
			Where("post_id IN ?", postIDs).Pluck("image_asset_id", &related).Error; err != nil {
			return nil, err
		}
		imageIDs = append(imageIDs, related...)
	}
	imageIDs = uniqueModerationIDs(imageIDs)
	var assets []model.ImageAsset
	err := db.FromContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id IN ?", imageIDs).Order("id").Find(&assets).Error
	return assets, err
}

// FindMachineRecordByProviderJobID 返回已经落库的异步审核事实。
func (ModerationRepository) FindMachineRecordByProviderJobID(
	ctx context.Context,
	provider model.ModerationProvider,
	providerJobID string,
) (*model.ModerationRecord, error) {
	var record model.ModerationRecord
	err := db.FromContext(ctx).
		Where("provider = ? AND provider_job_id = ?", provider, providerJobID).
		First(&record).Error
	if err != nil {
		return nil, NormalizeError(err)
	}
	return &record, nil
}

// CreateMachineRecordIfNew 幂等追加一条带外部任务号的机器审核记录。
func (ModerationRepository) CreateMachineRecordIfNew(
	ctx context.Context,
	record *model.ModerationRecord,
) (bool, error) {
	result := db.FromContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "provider"}, {Name: "provider_job_id"}},
		TargetWhere: clause.Where{Exprs: []clause.Expression{
			clause.Expr{SQL: "provider_job_id IS NOT NULL"},
		}},
		DoNothing: true,
	}).Create(record)
	return result.RowsAffected == 1, result.Error
}

// CreateManualRecord 追加人工复核记录；数据库触发器负责闭合 supersedes 关系。
func (ModerationRepository) CreateManualRecord(
	ctx context.Context,
	record *model.ModerationRecord,
) error {
	return db.FromContext(ctx).Create(record).Error
}

// UpdateImageModeration 写回图片当前结论，不修改既有审核流水。
func (ModerationRepository) UpdateImageModeration(
	ctx context.Context,
	imageAssetID uint64,
	status model.ModerationStatus,
) error {
	result := db.FromContext(ctx).Model(&model.ImageAsset{}).Where("id = ?", imageAssetID).
		UpdateColumn("moderation", status)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// ApplyManualTextVerdict 把人工结论写回当前文本对象。
func (ModerationRepository) ApplyManualTextVerdict(
	ctx context.Context,
	original *model.ModerationRecord,
	verdict model.ModerationVerdict,
) error {
	switch {
	case original.PostID != nil:
		return applyManualPostVerdict(ctx, original, verdict)
	case original.CommentID != nil:
		return applyManualCommentVerdict(ctx, original, verdict)
	case original.TagID != nil:
		return applyManualTagVerdict(ctx, *original.TagID, verdict)
	case original.UserID != nil:
		return nil // 用户表没有审核状态；昵称/简介按设计保持现值并告警管理员。
	default:
		return ErrNotFound
	}
}

// PostModerationTransitions 汇总一次聚合重算造成的帖子状态流转。
type PostModerationTransitions struct {
	Approved int64
	Rejected int64
}

// ReconcilePendingPosts 按“block 优先、review 次之、全 pass 发布”重算待审帖子。
func (ModerationRepository) ReconcilePendingPosts(
	ctx context.Context,
	postIDs []uint64,
) (PostModerationTransitions, error) {
	if len(postIDs) == 0 {
		return PostModerationTransitions{}, nil
	}
	postIDs = uniqueModerationIDs(postIDs)
	rejected := db.FromContext(ctx).Exec(`
		UPDATE posts AS p
		SET status = ?
		WHERE p.id IN ?
		  AND p.status = ?
		  AND p.deleted_at IS NULL
		  AND (
		    EXISTS (
		      SELECT 1
		      FROM post_images AS pi
		      JOIN image_assets AS ia ON ia.id = pi.image_asset_id
		      WHERE pi.post_id = p.id AND ia.moderation = ?
		    )
		    OR (
		      SELECT mr.verdict
		      FROM moderation_records AS mr
		      WHERE mr.post_id = p.id
		      ORDER BY mr.created_at DESC, mr.id DESC
		      LIMIT 1
		    ) = ?
		  )
	`, model.PostStatusRejected, postIDs, model.PostStatusPending,
		model.ModerationStatusBlock, model.ModerationVerdictBlock)
	if rejected.Error != nil {
		return PostModerationTransitions{}, rejected.Error
	}
	approved := db.FromContext(ctx).Exec(`
		UPDATE posts AS p
		SET status = ?
		WHERE p.id IN ?
		  AND p.status = ?
		  AND p.deleted_at IS NULL
		  AND NOT EXISTS (
		    SELECT 1
		    FROM post_images AS pi
		    JOIN image_assets AS ia ON ia.id = pi.image_asset_id
		    WHERE pi.post_id = p.id AND ia.moderation <> ?
		  )
		  AND (
		    SELECT mr.verdict
			    FROM moderation_records AS mr
			    WHERE mr.post_id = p.id
			    ORDER BY mr.created_at DESC, mr.id DESC
		    LIMIT 1
		  ) = ?
	`, model.PostStatusApproved, postIDs, model.PostStatusPending,
		model.ModerationStatusPass, model.ModerationVerdictPass)
	if approved.Error != nil {
		return PostModerationTransitions{}, approved.Error
	}
	return PostModerationTransitions{
		Approved: approved.RowsAffected,
		Rejected: rejected.RowsAffected,
	}, nil
}

func uniqueModerationIDs(values []uint64) []uint64 {
	seen := make(map[uint64]struct{}, len(values))
	out := make([]uint64, 0, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func applyManualPostVerdict(
	ctx context.Context,
	original *model.ModerationRecord,
	verdict model.ModerationVerdict,
) error {
	status := model.PostStatusRejected
	imageClause := ""
	if verdict == model.ModerationVerdictPass {
		status = model.PostStatusApproved
		imageClause = `AND NOT EXISTS (
			SELECT 1 FROM post_images AS pi
			JOIN image_assets AS ia ON ia.id = pi.image_asset_id
			WHERE pi.post_id = posts.id AND ia.moderation <> 'pass'
		)`
	}
	return db.FromContext(ctx).Exec(`
		UPDATE posts
			SET status = ?
			WHERE id = ? AND status = 'pending' AND deleted_at IS NULL
			  `+imageClause,
		status, *original.PostID).Error
}

func applyManualCommentVerdict(
	ctx context.Context,
	original *model.ModerationRecord,
	verdict model.ModerationVerdict,
) error {
	if verdict == model.ModerationVerdictPass {
		return nil
	}
	now := time.Now().UTC()
	return db.FromContext(ctx).Model(&model.Comment{}).
		Where("id = ? AND deleted_at IS NULL", *original.CommentID).
		Updates(map[string]any{
			"deleted_at": now, "deleted_reason": model.DeleteReasonModeration, "deleted_by": nil,
		}).Error
}

func applyManualTagVerdict(
	ctx context.Context,
	tagID uint64,
	verdict model.ModerationVerdict,
) error {
	updates := map[string]any{"moderation": model.ModerationStatus(verdict)}
	if verdict == model.ModerationVerdictBlock {
		updates["deleted_at"] = time.Now().UTC()
	}
	return db.FromContext(ctx).Model(&model.Tag{}).Where("id = ?", tagID).Updates(updates).Error
}
