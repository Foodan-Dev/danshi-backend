package router_test

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/pagination"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

type cursorPostFeed struct {
	Posts      []service.PostListItem `json:"posts"`
	Pagination pagination.CursorMeta  `json:"pagination"`
}

type offsetPostFeed struct {
	Posts      []service.PostListItem `json:"posts"`
	Pagination pagination.Meta        `json:"pagination"`
}

func TestPostLatestCursorPaginationAgainstPostgres(t *testing.T) {
	h := testutil.NewHarness(t)
	actor := h.Fixtures.CreateActor(h.Config)
	base := time.Date(2026, time.August, 23, 12, 0, 0, 123456000, time.UTC)

	t.Run("insertion between pages does not repeat", func(t *testing.T) {
		posts := createCursorPosts(t, h, actor.User.ID, "游标插入", base, 4, false)
		first := getCursorPostFeed(t, h, actor.Token, url.Values{
			"tags": {"游标插入"}, "limit": {"2"},
		})
		require.Equal(t, []uint64{posts[3].ID, posts[2].ID}, postListIDs(first.Posts))
		require.True(t, first.Pagination.HasMore)
		require.NotNil(t, first.Pagination.NextCursor)

		inserted := h.Fixtures.CreatePost(actor.User.ID,
			testutil.WithPostTitle("翻页途中插入"), testutil.WithPostTags("游标插入"))
		require.NoError(t, h.Database.GORM.Model(&model.Post{}).Where("id = ?", inserted.Post.ID).
			UpdateColumn("created_at", base.Add(10*time.Microsecond)).Error)

		second := getCursorPostFeed(t, h, actor.Token, url.Values{
			"tags": {"游标插入"}, "limit": {"2"}, "cursor": {*first.Pagination.NextCursor},
		})
		require.Equal(t, []uint64{posts[1].ID, posts[0].ID}, postListIDs(second.Posts))
		require.NotContains(t, postListIDs(second.Posts), posts[2].ID,
			"新记录把列表下移后不得重复第一页边界项")
	})

	t.Run("deletion between pages does not skip", func(t *testing.T) {
		posts := createCursorPosts(t, h, actor.User.ID, "游标删除", base.Add(time.Second), 4, false)
		first := getCursorPostFeed(t, h, actor.Token, url.Values{
			"tags": {"游标删除"}, "limit": {"2"},
		})
		deletedAt := time.Now().UTC()
		reason := model.DeleteReasonAuthor
		require.NoError(t, h.Database.GORM.Model(&model.Post{}).
			Where("id = ?", first.Posts[0].ID).UpdateColumns(map[string]any{
			"deleted_at": deletedAt, "deleted_reason": reason, "deleted_by": actor.User.ID,
		}).Error)

		second := getCursorPostFeed(t, h, actor.Token, url.Values{
			"tags": {"游标删除"}, "limit": {"2"}, "cursor": {*first.Pagination.NextCursor},
		})
		require.Equal(t, []uint64{posts[1].ID, posts[0].ID}, postListIDs(second.Posts),
			"第一页行被软删后不得跳过尚未读取的第一条")
	})

	t.Run("same microsecond and filters form a total order", func(t *testing.T) {
		posts := createCursorPosts(t, h, actor.User.ID, "游标同微秒", base.Add(2*time.Second), 5, true)
		h.Fixtures.CreatePost(actor.User.ID,
			testutil.WithPostTitle("同微秒但筛选外"), testutil.WithPostTags("其它游标组"))

		values := url.Values{
			"tags": {"游标同微秒"}, "category": {string(model.PostCategoryFood)}, "limit": {"2"},
		}
		got := collectCursorPostIDs(t, h, actor.Token, values)
		want := []uint64{posts[4].ID, posts[3].ID, posts[2].ID, posts[1].ID, posts[0].ID}
		require.Equal(t, want, got, "相同 created_at 必须由 id DESC 保证不重不漏")

		first := getCursorPostFeed(t, h, actor.Token, values)
		tampered := tamperCursor(*first.Pagination.NextCursor)
		values.Set("cursor", tampered)
		status, response, _ := performJSON(t, h.Engine, http.MethodGet,
			"/api/v2/posts?"+values.Encode(), nil, actor.Token)
		requireFieldError(t, status, response, "cursor", apierr.FieldInvalidFormat)
	})
}

func TestNotificationCursorPaginationAgainstPostgres(t *testing.T) {
	h := testutil.NewHarness(t)
	base := time.Date(2026, time.August, 23, 13, 0, 0, 654321000, time.UTC)

	t.Run("insertion between pages does not repeat", func(t *testing.T) {
		recipient := h.Fixtures.CreateActor(h.Config)
		sender := h.Fixtures.CreateUser()
		rows := createCursorNotifications(t, h.Database.GORM, recipient.User.ID, sender.ID, base, 4, false)
		first := getNotificationPage(t, h, recipient.Token, url.Values{"limit": {"2"}})
		require.Equal(t, []uint64{rows[3].ID, rows[2].ID}, notificationIDs(first.Notifications))

		inserted := model.Notification{
			RecipientID: recipient.User.ID, SenderID: sender.ID, Type: model.NotificationTypeFollow,
			CreatedAt: base.Add(10 * time.Microsecond), UpdatedAt: base.Add(10 * time.Microsecond),
		}
		require.NoError(t, h.Database.GORM.Create(&inserted).Error)
		second := getNotificationPage(t, h, recipient.Token, url.Values{
			"limit": {"2"}, "cursor": {*first.Pagination.NextCursor},
		})
		require.Equal(t, []uint64{rows[1].ID, rows[0].ID}, notificationIDs(second.Notifications))

		status, response, _ := performJSON(t, h.Engine, http.MethodGet,
			"/api/v2/notifications?limit=2&cursor="+url.QueryEscape(tamperCursor(*first.Pagination.NextCursor)),
			nil, recipient.Token)
		requireFieldError(t, status, response, "cursor", apierr.FieldInvalidFormat)
	})

	t.Run("deletion between pages does not skip", func(t *testing.T) {
		recipient := h.Fixtures.CreateActor(h.Config)
		sender := h.Fixtures.CreateUser()
		rows := createCursorNotifications(
			t, h.Database.GORM, recipient.User.ID, sender.ID, base.Add(time.Second), 4, false,
		)
		first := getNotificationPage(t, h, recipient.Token, url.Values{"limit": {"2"}})
		require.NoError(t, h.Database.GORM.Delete(&model.Notification{}, first.Notifications[0].ID).Error)
		second := getNotificationPage(t, h, recipient.Token, url.Values{
			"limit": {"2"}, "cursor": {*first.Pagination.NextCursor},
		})
		require.Equal(t, []uint64{rows[1].ID, rows[0].ID}, notificationIDs(second.Notifications),
			"已读边界之前的通知被删除后不得跳过未读项")
	})

	t.Run("same microsecond remains correct with filters", func(t *testing.T) {
		recipient := h.Fixtures.CreateActor(h.Config)
		sender := h.Fixtures.CreateUser()
		rows := createCursorNotifications(
			t, h.Database.GORM, recipient.User.ID, sender.ID, base.Add(2*time.Second), 5, true,
		)
		read := model.Notification{
			RecipientID: recipient.User.ID, SenderID: sender.ID, Type: model.NotificationTypeFollow,
			IsRead: true, CreatedAt: base.Add(2 * time.Second), UpdatedAt: base.Add(2 * time.Second),
		}
		require.NoError(t, h.Database.GORM.Create(&read).Error)

		values := url.Values{"is_read": {"false"}, "type": {"follow"}, "limit": {"2"}}
		var got []uint64
		for {
			page := getNotificationPage(t, h, recipient.Token, values)
			got = append(got, notificationIDs(page.Notifications)...)
			if !page.Pagination.HasMore {
				require.Nil(t, page.Pagination.NextCursor)
				break
			}
			require.NotNil(t, page.Pagination.NextCursor)
			values.Set("cursor", *page.Pagination.NextCursor)
		}
		want := []uint64{rows[4].ID, rows[3].ID, rows[2].ID, rows[1].ID, rows[0].ID}
		require.Equal(t, want, got)
		require.NotContains(t, got, read.ID, "游标必须与 is_read/type 筛选同时生效")
	})
}

func createCursorPosts(
	t *testing.T,
	h *testutil.Harness,
	authorID uint64,
	tag string,
	base time.Time,
	count int,
	sameMicrosecond bool,
) []model.Post {
	t.Helper()
	posts := make([]model.Post, 0, count)
	for index := range count {
		fixture := h.Fixtures.CreatePost(authorID,
			testutil.WithPostTitle(fmt.Sprintf("%s-%d", tag, index)), testutil.WithPostTags(tag))
		createdAt := base
		if !sameMicrosecond {
			createdAt = base.Add(time.Duration(index) * time.Microsecond)
		}
		require.NoError(t, h.Database.GORM.Model(&model.Post{}).Where("id = ?", fixture.Post.ID).
			UpdateColumn("created_at", createdAt).Error)
		fixture.Post.CreatedAt = createdAt
		posts = append(posts, fixture.Post)
	}
	return posts
}

func getCursorPostFeed(
	t *testing.T,
	h *testutil.Harness,
	token string,
	values url.Values,
) cursorPostFeed {
	t.Helper()
	status, response, _ := performJSON(t, h.Engine, http.MethodGet,
		"/api/v2/posts?"+values.Encode(), nil, token)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
	var page cursorPostFeed
	decodeData(t, response, &page)
	return page
}

func collectCursorPostIDs(
	t *testing.T,
	h *testutil.Harness,
	token string,
	values url.Values,
) []uint64 {
	t.Helper()
	values = cloneURLValues(values)
	var ids []uint64
	for {
		page := getCursorPostFeed(t, h, token, values)
		ids = append(ids, postListIDs(page.Posts)...)
		if !page.Pagination.HasMore {
			require.Nil(t, page.Pagination.NextCursor)
			return ids
		}
		require.NotNil(t, page.Pagination.NextCursor)
		values.Set("cursor", *page.Pagination.NextCursor)
	}
}

func createCursorNotifications(
	t *testing.T,
	gdb *gorm.DB,
	recipientID uint64,
	senderID uint64,
	base time.Time,
	count int,
	sameMicrosecond bool,
) []model.Notification {
	t.Helper()
	rows := make([]model.Notification, 0, count)
	for index := range count {
		createdAt := base
		if !sameMicrosecond {
			createdAt = base.Add(time.Duration(index) * time.Microsecond)
		}
		row := model.Notification{
			RecipientID: recipientID, SenderID: senderID, Type: model.NotificationTypeFollow,
			CreatedAt: createdAt, UpdatedAt: createdAt,
		}
		require.NoError(t, gdb.Create(&row).Error)
		rows = append(rows, row)
	}
	return rows
}

func getNotificationPage(
	t *testing.T,
	h *testutil.Harness,
	token string,
	values url.Values,
) service.NotificationList {
	t.Helper()
	status, response, _ := performJSON(t, h.Engine, http.MethodGet,
		"/api/v2/notifications?"+values.Encode(), nil, token)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
	var page service.NotificationList
	decodeData(t, response, &page)
	return page
}

func notificationIDs(items []service.NotificationItem) []uint64 {
	ids := make([]uint64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func tamperCursor(token string) string {
	last := byte('A')
	if token[len(token)-1] == 'A' {
		last = 'B'
	}
	return token[:len(token)-1] + string(last)
}

func cloneURLValues(values url.Values) url.Values {
	clone := make(url.Values, len(values))
	for key, items := range values {
		clone[key] = append([]string{}, items...)
	}
	return clone
}
