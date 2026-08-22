package service

import (
	"context"
	"crypto/md5" // #nosec G501 -- Content-MD5 是 COS 协议字段，不用于密码学安全决策。
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/ptime"
	"github.com/jingyijun/danshi_backend_go/internal/repository"
)

const defaultExpiredUploadBatch = 100

var contentTypeExtensions = map[string]string{
	"image/jpeg": "jpg",
	"image/png":  "png",
	"image/gif":  "gif",
	"image/webp": "webp",
	"image/bmp":  "bmp",
	"image/heic": "heic",
	"image/heif": "heif",
}

var purposeDirectories = map[model.ImagePurpose]string{
	model.ImagePurposePost:   "posts",
	model.ImagePurposeAvatar: "avatars",
}

// ImageStorage 是对象存储供应商替换边界。
type ImageStorage interface {
	PresignPut(ctx context.Context, request StoragePresignRequest) (StorageUploadTicket, error)
	HeadObject(ctx context.Context, objectKey string) (StorageObjectMeta, error)
	DeleteObject(ctx context.Context, objectKey string) error
	PublicURL(objectKey string) (string, error)
}

// StoragePresignRequest 是签入 PUT URL 的全部稳定字段。
type StoragePresignRequest struct {
	ObjectKey     string
	ContentType   string
	ContentLength int64
	ContentMD5    string
	TTL           time.Duration
}

// StorageUploadTicket 是对象存储返回的直传凭证。
type StorageUploadTicket struct {
	UploadURL string
	ExpiresAt time.Time
}

// StorageObjectMeta 是 complete 校验所需的对象元信息。
type StorageObjectMeta struct {
	Exists        bool
	ContentLength int64
}

// UploadPresignInput 是申请直传凭证的输入。
type UploadPresignInput struct {
	Purpose     string
	ContentType string
	Size        int64
	ContentMD5  string
}

// UploadPresignResult 不暴露可推导公开 URL 的对象键。
type UploadPresignResult struct {
	UploadID  uint64     `json:"upload_id"`
	Method    string     `json:"method"`
	UploadURL string     `json:"upload_url"`
	ExpiresAt ptime.Time `json:"expires_at"`
}

// UploadCompleteResult 是确认直传后的资产信息。
type UploadCompleteResult struct {
	UploadID  uint64            `json:"upload_id"`
	ObjectKey string            `json:"object_key"`
	PublicURL string            `json:"public_url"`
	Status    model.ImageStatus `json:"status"`
}

// UploadService 编排 presign、complete、图片送审与过期回收。
type UploadService struct {
	storage        ImageStorage
	imageModerator ImageModerator
	moderation     *ModerationService
	maxImageBytes  int64
	presignTTL     time.Duration
	assets         repository.UploadRepository
}

// NewUploadService 创建上传服务。
func NewUploadService(
	storage ImageStorage,
	imageModerator ImageModerator,
	moderation *ModerationService,
	maxImageBytes int64,
	presignTTL time.Duration,
) *UploadService {
	if storage == nil {
		storage = UnavailableImageStorage{}
	}
	if imageModerator == nil {
		imageModerator = UnavailableImageModerator{}
	}
	if moderation == nil {
		moderation = NewModerationService(nil)
	}
	return &UploadService{
		storage: storage, imageModerator: imageModerator, moderation: moderation,
		maxImageBytes: maxImageBytes, presignTTL: presignTTL,
	}
}

// Presign 校验上传声明、签发 PUT URL 并创建 pending 资产。
func (s *UploadService) Presign(
	ctx context.Context,
	userID uint64,
	input UploadPresignInput,
) (*UploadPresignResult, error) {
	purpose, directory, extension, err := s.validatePresign(input)
	if err != nil {
		return nil, err
	}
	objectKey, err := newObjectKey(directory, userID, extension, time.Now().UTC())
	if err != nil {
		return nil, apierr.Internal(err)
	}
	ticket, err := s.storage.PresignPut(ctx, StoragePresignRequest{
		ObjectKey: objectKey, ContentType: input.ContentType,
		ContentLength: input.Size, ContentMD5: input.ContentMD5, TTL: s.presignTTL,
	})
	if err != nil {
		return nil, storageUnavailable(err)
	}
	size := input.Size
	asset := &model.ImageAsset{
		UploaderID: &userID, Purpose: purpose, ObjectKey: objectKey, PublicURL: "",
		ContentType: input.ContentType, Size: &size, Status: model.ImageStatusPending,
		Moderation: model.ModerationStatusPending,
	}
	if err := s.assets.Create(ctx, asset); err != nil {
		return nil, apierr.Internal(err)
	}
	return &UploadPresignResult{
		UploadID: asset.ID, Method: "PUT", UploadURL: ticket.UploadURL,
		ExpiresAt: ptime.Time(ticket.ExpiresAt.UTC()),
	}, nil
}

// Complete 持有资产行锁校验直传对象，随后触发图片审核。
func (s *UploadService) Complete(
	ctx context.Context,
	uploadID uint64,
	userID uint64,
) (*UploadCompleteResult, error) {
	asset, err := s.assets.LockByID(ctx, uploadID)
	if err != nil {
		return nil, repository.ToAPIError(err, apierr.BizUploadNotFound, "上传记录")
	}
	if asset.UploaderID == nil || *asset.UploaderID != userID {
		return nil, apierr.Forbidden(apierr.BizImageNotOwned, "无权操作该上传记录")
	}
	if asset.Status != model.ImageStatusPending {
		return nil, apierr.Conflict(apierr.BizUploadClosed, "上传记录状态已结束")
	}
	meta, err := s.storage.HeadObject(ctx, asset.ObjectKey)
	if err != nil {
		return nil, storageUnavailable(err)
	}
	if !meta.Exists {
		return nil, apierr.Conflict(apierr.BizUploadIncomplete, "图片尚未上传到存储")
	}
	if err := s.validateStoredObject(ctx, asset, meta); err != nil {
		return nil, err
	}
	publicURL, err := s.storage.PublicURL(asset.ObjectKey)
	if err != nil {
		return nil, storageUnavailable(err)
	}
	if err := s.assets.MarkComplete(ctx, asset.ID, publicURL, meta.ContentLength); err != nil {
		return nil, apierr.Internal(err)
	}
	submission, err := s.imageModerator.SubmitImage(ctx, ImageModerationRequest{
		ImageAssetID: asset.ID, ObjectKey: asset.ObjectKey,
	})
	if err != nil {
		return nil, err
	}
	if submission.Immediate != nil {
		if _, err := s.moderation.ApplyImageResult(ctx, *submission.Immediate); err != nil {
			return nil, err
		}
	} else if submission.ProviderJobID == nil || strings.TrimSpace(*submission.ProviderJobID) == "" {
		return nil, apierr.Internal(errImmediateImageResultMissingJobID)
	}
	return &UploadCompleteResult{
		UploadID: asset.ID, ObjectKey: asset.ObjectKey, PublicURL: publicURL,
		Status: model.ImageStatusReady,
	}, nil
}

// ExpirePending 回收一批过期且尚未完成的上传；被 complete 锁住的行会跳过。
func (s *UploadService) ExpirePending(
	ctx context.Context,
	before time.Time,
	limit int,
) (int, error) {
	if limit <= 0 {
		limit = defaultExpiredUploadBatch
	}
	assets, err := s.assets.LockExpiredPending(ctx, before, limit)
	if err != nil {
		return 0, apierr.Internal(err)
	}
	for i := range assets {
		if err := s.storage.DeleteObject(ctx, assets[i].ObjectKey); err != nil {
			return 0, storageUnavailable(err)
		}
		if err := s.assets.Retire(ctx, assets[i].ID); err != nil {
			return 0, apierr.Internal(err)
		}
	}
	return len(assets), nil
}

func (s *UploadService) validatePresign(
	input UploadPresignInput,
) (model.ImagePurpose, string, string, error) {
	purpose := model.ImagePurpose(input.Purpose)
	directory, exists := purposeDirectories[purpose]
	if !exists {
		return "", "", "", apierr.InvalidField("purpose", apierr.FieldInvalidEnum, "不支持的上传用途")
	}
	extension, exists := contentTypeExtensions[input.ContentType]
	if !exists {
		return "", "", "", apierr.InvalidField("content_type", apierr.FieldInvalidEnum, "不支持的图片类型")
	}
	if input.Size <= 0 || input.Size > s.maxImageBytes {
		return "", "", "", apierr.InvalidField(
			"size", apierr.FieldOutOfRange, "图片大小必须在允许范围内",
		)
	}
	digest, err := base64.StdEncoding.DecodeString(input.ContentMD5)
	if err != nil || len(input.ContentMD5) != 24 || len(digest) != md5.Size ||
		base64.StdEncoding.EncodeToString(digest) != input.ContentMD5 {
		return "", "", "", apierr.InvalidField(
			"content_md5", apierr.FieldInvalidFormat,
			"content_md5 必须是 RFC 1864 标准 Base64 MD5",
		)
	}
	return purpose, directory, extension, nil
}

func (s *UploadService) validateStoredObject(
	ctx context.Context,
	asset *model.ImageAsset,
	meta StorageObjectMeta,
) error {
	if meta.ContentLength <= 0 || meta.ContentLength > s.maxImageBytes {
		if err := s.storage.DeleteObject(ctx, asset.ObjectKey); err != nil {
			return storageUnavailable(err)
		}
		return apierr.Conflict(apierr.BizUploadSizeMismatch, "图片超过大小限制")
	}
	if asset.Size != nil && meta.ContentLength != *asset.Size {
		if err := s.storage.DeleteObject(ctx, asset.ObjectKey); err != nil {
			return storageUnavailable(err)
		}
		return apierr.Conflict(apierr.BizUploadSizeMismatch, "图片大小与声明不一致")
	}
	return nil
}

func newObjectKey(
	directory string,
	userID uint64,
	extension string,
	now time.Time,
) (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%d/%s/%s.%s", directory, userID, now.Format("2006/01"),
		hex.EncodeToString(random), extension), nil
}

func storageUnavailable(err error) error {
	return apierr.ServiceUnavailable("图片存储暂时不可用，请稍后重试").WithCause(err)
}

var errImageStorageUnconfigured = errors.New("image storage is not configured")

// UnavailableImageStorage 是生产未配置对象存储时的 fail-closed 实现。
type UnavailableImageStorage struct{}

// PresignPut 拒绝签发伪造的上传凭证。
func (UnavailableImageStorage) PresignPut(
	context.Context,
	StoragePresignRequest,
) (StorageUploadTicket, error) {
	return StorageUploadTicket{}, errImageStorageUnconfigured
}

// HeadObject 拒绝伪造对象存在。
func (UnavailableImageStorage) HeadObject(context.Context, string) (StorageObjectMeta, error) {
	return StorageObjectMeta{}, errImageStorageUnconfigured
}

// DeleteObject 拒绝伪造对象已删除。
func (UnavailableImageStorage) DeleteObject(context.Context, string) error {
	return errImageStorageUnconfigured
}

// PublicURL 拒绝构造未配置存储的公开 URL。
func (UnavailableImageStorage) PublicURL(string) (string, error) {
	return "", errImageStorageUnconfigured
}
