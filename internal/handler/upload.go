package handler

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/jingyijun/danshi_backend_go/internal/pkg/envelope"
	"github.com/jingyijun/danshi_backend_go/internal/router/middleware"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

// Upload 处理客户端直传对象存储的两阶段协议。
type Upload struct {
	service *service.UploadService
}

// NewUpload 创建上传 handler。
func NewUpload(uploadService *service.UploadService) *Upload {
	return &Upload{service: uploadService}
}

type uploadPresignRequest struct {
	Purpose     string `json:"purpose"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	ContentMD5  string `json:"content_md5"`
}

// Presign 签发把上传声明固定进签名的 PUT URL。
func (h *Upload) Presign(ctx context.Context, c *app.RequestContext) {
	var request uploadPresignRequest
	if err := bindJSON(c, &request); err != nil {
		middleware.Fail(ctx, c, err)
		return
	}
	principal, err := middleware.CurrentPrincipal(c)
	if err != nil {
		failService(ctx, c, err)
		return
	}
	result, err := h.service.Presign(ctx, principal.User.ID, service.UploadPresignInput{
		Purpose: request.Purpose, ContentType: request.ContentType,
		Size: request.Size, ContentMD5: request.ContentMD5,
	})
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("上传凭证签发成功", result))
}

// Complete 在持有上传资产行锁时校验对象并触发图片机审。
func (h *Upload) Complete(ctx context.Context, c *app.RequestContext) {
	uploadID, err := positivePathID(c.Param("upload_id"), "upload_id")
	principal, principalErr := middleware.CurrentPrincipal(c)
	if err == nil {
		err = principalErr
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	result, err := h.service.Complete(ctx, uploadID, principal.User.ID)
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("上传完成", result))
}
