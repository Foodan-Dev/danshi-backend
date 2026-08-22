package router_test

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

func TestNotificationDomainAgainstPostgres(t *testing.T) {
	gdb, database := openAuthPostgres(t)
	cfg := authTestConfig()
	sender := newCaptureEmailSender()
	engine := authTestEngine(cfg, database, sender)
	actors := registerCommentActors(t, engine, sender)
	fixture := loadPostFixture(t, gdb)

	t.Run("notification route inventory", func(t *testing.T) {
		testNotificationRouteInventory(t, engine)
	})

	t.Run("producer semantics reads and idempotency", func(t *testing.T) {
		testNotificationProducersAndReads(t, engine, gdb, actors, fixture)
	})
}

func testNotificationRouteInventory(t *testing.T, engine *server.Hertz) {
	t.Helper()
	operations := make([]string, 0)
	for _, route := range engine.Routes() {
		if strings.HasPrefix(route.Path, "/api/v2/notifications") {
			operations = append(operations, route.Method+" "+route.Path)
		}
	}
	require.ElementsMatch(t, []string{
		"GET /api/v2/notifications",
		"GET /api/v2/notifications/unread-count",
		"PUT /api/v2/notifications/:notification_id/read",
		"PUT /api/v2/notifications/read-all",
	}, operations)
}

func testNotificationProducersAndReads(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	actors commentActors,
	fixture postFixture,
) {
	t.Helper()
	post := createPost(t, engine, actors.PostAuthor.Token,
		sharePostPayload(fixture, "通知语义帖子", []string{"通知语义"}))
	longContent := strings.Repeat("预览", 60)
	root := createComment(t, engine, actors.Commenter.Token, post.ID, map[string]any{
		"content": longContent, "mentioned_user_ids": []uint64{actors.Mentioned.User.ID},
	})
	firstReply := createComment(t, engine, actors.Replier.Token, post.ID, map[string]any{
		"content": "第一条回复正文", "parent_id": root.Comment.ID,
	})
	secondReply := createComment(t, engine, actors.Mentioned.Token, post.ID, map[string]any{
		"content": "回复楼内回复的正文", "parent_id": firstReply.Comment.ID,
	})

	postLikePath := postPath(post.ID) + "/like"
	for range 2 {
		status, _, _ := performJSON(t, engine, http.MethodPost, postLikePath, nil, actors.Commenter.Token)
		require.Equal(t, http.StatusOK, status)
	}
	commentLikePath := commentPath(root.Comment.ID) + "/like"
	for range 2 {
		status, _, _ := performJSON(t, engine, http.MethodPost, commentLikePath, nil, actors.Mentioned.Token)
		require.Equal(t, http.StatusOK, status)
	}
	followPath := userPath(actors.PostAuthor.User.ID) + "/follow"
	for range 2 {
		status, _, _ := performJSON(t, engine, http.MethodPost, followPath, nil, actors.Commenter.Token)
		require.Equal(t, http.StatusOK, status)
	}

	// 自己点赞、评论、回复和提及自己都不得产生通知。
	status, _, _ := performJSON(t, engine, http.MethodPost, postLikePath, nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusOK, status)
	status, _, _ = performJSON(t, engine, http.MethodPost, commentLikePath, nil, actors.Commenter.Token)
	require.Equal(t, http.StatusOK, status)
	selfRoot := createComment(t, engine, actors.PostAuthor.Token, post.ID, map[string]any{
		"content": "自己评论自己的帖子", "mentioned_user_ids": []uint64{actors.PostAuthor.User.ID},
	})
	createComment(t, engine, actors.PostAuthor.Token, post.ID, map[string]any{
		"content": "自己回复自己的评论", "parent_id": selfRoot.Comment.ID,
	})

	preview := string([]rune(longContent)[:100])
	commentNotification := requireNotification(
		t, gdb, actors.PostAuthor.User.ID, actors.Commenter.User.ID, model.NotificationTypeComment,
	)
	require.Equal(t, post.ID, *commentNotification.RelatedPostID)
	require.Nil(t, commentNotification.RelatedCommentID)
	require.Equal(t, preview, *commentNotification.Content)

	mentionNotification := requireNotification(
		t, gdb, actors.Mentioned.User.ID, actors.Commenter.User.ID, model.NotificationTypeMention,
	)
	require.Equal(t, root.Comment.ID, *mentionNotification.RelatedCommentID,
		"mention 必须指向新建评论")
	require.Equal(t, preview, *mentionNotification.Content)

	replyNotification := requireNotification(
		t, gdb, actors.Replier.User.ID, actors.Mentioned.User.ID, model.NotificationTypeReply,
	)
	require.Equal(t, firstReply.Comment.ID, *replyNotification.RelatedCommentID,
		"reply 必须指向实际被回复的父评论")
	require.NotEqual(t, secondReply.Comment.ID, *replyNotification.RelatedCommentID,
		"reply 不得指向新回复自身")
	require.Equal(t, "回复楼内回复的正文", *replyNotification.Content,
		"预览必须来自新回复正文，而不是父评论正文")

	likeCommentNotification := requireNotification(
		t, gdb, actors.Commenter.User.ID, actors.Mentioned.User.ID, model.NotificationTypeLikeComment,
	)
	require.Equal(t, root.Comment.ID, *likeCommentNotification.RelatedCommentID)
	require.Nil(t, likeCommentNotification.Content)

	likePostNotification := requireNotification(
		t, gdb, actors.PostAuthor.User.ID, actors.Commenter.User.ID, model.NotificationTypeLikePost,
	)
	require.Equal(t, post.ID, *likePostNotification.RelatedPostID)
	require.Nil(t, likePostNotification.Content)

	followNotification := requireNotification(
		t, gdb, actors.PostAuthor.User.ID, actors.Commenter.User.ID, model.NotificationTypeFollow,
	)
	require.Nil(t, followNotification.RelatedPostID)
	require.Nil(t, followNotification.RelatedCommentID)
	require.Nil(t, followNotification.Content)

	assertSingleNotification(t, gdb, actors.PostAuthor.User.ID,
		actors.Commenter.User.ID, model.NotificationTypeLikePost)
	assertSingleNotification(t, gdb, actors.Commenter.User.ID,
		actors.Mentioned.User.ID, model.NotificationTypeLikeComment)
	assertSingleNotification(t, gdb, actors.PostAuthor.User.ID,
		actors.Commenter.User.ID, model.NotificationTypeFollow)
	assertNoSelfNotifications(t, gdb)

	testNotificationReadEndpoints(t, engine, gdb, actors, commentNotification.ID, post.ID, root.Comment.ID)
}

func testNotificationReadEndpoints(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	actors commentActors,
	commentNotificationID uint64,
	postID uint64,
	rootCommentID uint64,
) {
	t.Helper()
	status, response, _ := performJSON(t, engine, http.MethodGet,
		"/api/v2/notifications/unread-count", nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusOK, status)
	var stats service.NotificationStats
	decodeData(t, response, &stats)
	require.EqualValues(t, 3, stats.UnreadCount, "评论、帖子点赞与关注各一条")

	status, response, _ = performJSON(t, engine, http.MethodGet,
		"/api/v2/notifications?type=comment", nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusOK, status)
	var comments service.NotificationList
	decodeData(t, response, &comments)
	require.Len(t, comments.Notifications, 1)
	require.Equal(t, postID, *comments.Notifications[0].RelatedID)
	require.Equal(t, "post", *comments.Notifications[0].RelatedType)
	require.Equal(t, postID, *comments.Notifications[0].PostID)
	require.EqualValues(t, 3, comments.UnreadCount, "筛选不得改变全局未读数")

	status, response, _ = performJSON(t, engine, http.MethodGet,
		"/api/v2/notifications?type=reply", nil, actors.Replier.Token)
	require.Equal(t, http.StatusOK, status)
	var replies service.NotificationList
	decodeData(t, response, &replies)
	require.Len(t, replies.Notifications, 1)
	require.NotEqual(t, rootCommentID, *replies.Notifications[0].RelatedID)
	require.Equal(t, "comment", *replies.Notifications[0].RelatedType)
	require.Equal(t, postID, *replies.Notifications[0].PostID,
		"评论类通知必须批量解析所属帖子供前端跳转")

	readPath := fmt.Sprintf("/api/v2/notifications/%d/read", commentNotificationID)
	status, response, _ = performJSON(t, engine, http.MethodPut, readPath, nil, actors.Replier.Token)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizNotificationNotFound, response.ErrorCode,
		"非收件人不得获知通知是否存在")
	status, _, _ = performJSON(t, engine, http.MethodPut, readPath, nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusOK, status)
	status, _, _ = performJSON(t, engine, http.MethodPut, readPath, nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusOK, status, "单条已读应幂等")

	status, response, _ = performJSON(t, engine, http.MethodPut,
		"/api/v2/notifications/read-all", nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusOK, status, "静态 read-all 路由不得被参数路由吞掉")
	var marked service.NotificationMarked
	decodeData(t, response, &marked)
	require.EqualValues(t, 2, marked.MarkedCount)
	status, response, _ = performJSON(t, engine, http.MethodPut,
		"/api/v2/notifications/read-all", nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusOK, status)
	decodeData(t, response, &marked)
	require.Zero(t, marked.MarkedCount, "read-all 应幂等")

	status, response, _ = performJSON(t, engine, http.MethodGet,
		"/api/v2/notifications?is_read=true", nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusOK, status)
	var read service.NotificationList
	decodeData(t, response, &read)
	require.Len(t, read.Notifications, 3)
	require.Zero(t, read.UnreadCount)

	status, _, _ = performJSON(t, engine, http.MethodGet,
		"/api/v2/notifications?is_read=maybe", nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	status, _, _ = performJSON(t, engine, http.MethodGet,
		"/api/v2/notifications?type=unknown", nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusUnprocessableEntity, status)

	var enabled atomic.Bool
	var count atomic.Int64
	registerQueryCounter(t, gdb, &enabled, &count)
	measure := func(limit int) int64 {
		count.Store(0)
		enabled.Store(true)
		status, _, _ := performJSON(t, engine, http.MethodGet,
			fmt.Sprintf("/api/v2/notifications?page=1&limit=%d", limit), nil, actors.PostAuthor.Token)
		enabled.Store(false)
		require.Equal(t, http.StatusOK, status)
		return count.Load()
	}
	one, three := measure(1), measure(3)
	require.Positive(t, one)
	require.Equal(t, one, three, "通知列表 SELECT 数不得随 page_size 增长")
}

func requireNotification(
	t *testing.T,
	gdb *gorm.DB,
	recipientID uint64,
	senderID uint64,
	notificationType model.NotificationType,
) model.Notification {
	t.Helper()
	var notification model.Notification
	require.NoError(t, gdb.Where(
		"recipient_id = ? AND sender_id = ? AND type = ?",
		recipientID, senderID, notificationType,
	).Order("id DESC").First(&notification).Error)
	return notification
}

func assertSingleNotification(
	t *testing.T,
	gdb *gorm.DB,
	recipientID uint64,
	senderID uint64,
	notificationType model.NotificationType,
) {
	t.Helper()
	var count int64
	require.NoError(t, gdb.Model(&model.Notification{}).Where(
		"recipient_id = ? AND sender_id = ? AND type = ?",
		recipientID, senderID, notificationType,
	).Count(&count).Error)
	require.EqualValues(t, 1, count, "重复动作不得产生重复通知")
}

func assertNoSelfNotifications(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	var count int64
	require.NoError(t, gdb.Model(&model.Notification{}).
		Where("recipient_id = sender_id").Count(&count).Error)
	require.Zero(t, count, "自己操作自己不得产生通知")
}
