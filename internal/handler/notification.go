package handler

import (
	"context"
	"strconv"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/envelope"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/pagination"
	"github.com/jingyijun/danshi_backend_go/internal/router/middleware"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

// Notification 处理 Notification 域 HTTP 请求。
type Notification struct {
	service *service.NotificationService
}

// NewNotification 创建通知 handler。
func NewNotification(notificationService *service.NotificationService) *Notification {
	return &Notification{service: notificationService}
}

// List 返回当前用户通知列表。
func (h *Notification) List(ctx context.Context, c *app.RequestContext) {
	principal, err := middleware.CurrentPrincipal(c)
	params, paramsErr := pagination.Parse(c.Query("page"), c.Query("limit"))
	if err == nil {
		err = paramsErr
	}
	isRead, readErr := optionalBool(c.Query("is_read"))
	if err == nil {
		err = readErr
	}
	var result *service.NotificationList
	if err == nil {
		result, err = h.service.List(ctx, principal.User.ID, service.NotificationListInput{
			IsRead: isRead, Type: c.Query("type"), Pagination: params,
		})
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("请求成功", result))
}

// UnreadCount 返回当前用户未读通知数。
func (h *Notification) UnreadCount(ctx context.Context, c *app.RequestContext) {
	principal, err := middleware.CurrentPrincipal(c)
	var result *service.NotificationStats
	if err == nil {
		result, err = h.service.UnreadCount(ctx, principal.User.ID)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("请求成功", result))
}

// MarkRead 标记一条属于当前用户的通知。
func (h *Notification) MarkRead(ctx context.Context, c *app.RequestContext) {
	notificationID, err := strconv.ParseUint(c.Param("notification_id"), 10, 64)
	if err != nil || notificationID == 0 {
		middleware.Fail(ctx, c, apierr.InvalidField(
			"notification_id", apierr.FieldInvalidFormat, "notification_id 必须是正整数",
		))
		return
	}
	principal, err := middleware.CurrentPrincipal(c)
	if err == nil {
		err = h.service.MarkRead(ctx, notificationID, principal.User.ID)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK[any]("标记成功", nil))
}

// MarkAllRead 标记当前用户全部未读通知。
func (h *Notification) MarkAllRead(ctx context.Context, c *app.RequestContext) {
	principal, err := middleware.CurrentPrincipal(c)
	var result *service.NotificationMarked
	if err == nil {
		result, err = h.service.MarkAllRead(ctx, principal.User.ID)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("全部标记成功", result))
}

func optionalBool(raw string) (*bool, error) {
	if raw == "" {
		return nil, nil
	}
	switch strings.ToLower(raw) {
	case "true":
		value := true
		return &value, nil
	case "false":
		value := false
		return &value, nil
	default:
		return nil, apierr.InvalidField("is_read", apierr.FieldInvalidFormat, "is_read 只能是 true 或 false")
	}
}
