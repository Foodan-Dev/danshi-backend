package router_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	dbinfra "github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

type commentActors struct {
	PostAuthor service.AuthResult
	Commenter  service.AuthResult
	Replier    service.AuthResult
	Mentioned  service.AuthResult
}

func fixedVerdictModerator(verdict model.ModerationVerdict) *testutil.MockModeration {
	mock := testutil.NewMockModeration()
	mock.SetDefaultContent(testutil.ContentVerdict(verdict, nil, nil))
	return mock
}

func TestCommentDomainAgainstPostgres(t *testing.T) {
	h := testutil.NewHarness(t)
	gdb, database := h.Database.GORM, h.Database.DB
	sender, engine := h.Email, h.Engine
	actors := registerCommentActors(t, engine, sender)
	dictionaries := h.Fixtures.CreateDictionaries()
	fixture := postFixture{
		Canteen: dictionaries.Canteen, Window: dictionaries.Window,
		Cuisine: dictionaries.Cuisine, Flavors: dictionaries.Flavors,
	}

	t.Run("comment route inventory", func(t *testing.T) {
		testCommentRouteInventory(t, engine)
	})

	t.Run("create true chain without history mentions and moderation", func(t *testing.T) {
		testCommentCreateContract(t, engine, gdb, actors, fixture)
	})

	t.Run("parent content and mention boundary matrix", func(t *testing.T) {
		testCommentCreateBoundaries(t, engine, gdb, h.Fixtures, actors, fixture)
	})

	t.Run("edit appends version and reruns moderation", func(t *testing.T) {
		testCommentEditVersion(t, engine, gdb, actors, fixture)
	})

	t.Run("edit delete authorization and deleted guards", func(t *testing.T) {
		testCommentMutationGuards(t, engine, gdb, actors, fixture)
	})

	t.Run("concurrent revisions start at one without gaps", func(t *testing.T) {
		testConcurrentCommentEdits(t, engine, gdb, database, actors, fixture)
	})

	t.Run("history failure rolls back main and mentions", func(t *testing.T) {
		testCommentHistoryFailureRollback(t, engine, gdb, actors, fixture)
	})

	t.Run("review is author-only and records moderation", func(t *testing.T) {
		testCommentModerationReview(t, engine, gdb, database, actors, fixture)
	})

	t.Run("block soft deletes after moderation", func(t *testing.T) {
		testCommentModerationBlock(t, engine, gdb, database, actors, fixture)
	})

	t.Run("soft delete preserves replies and counters", func(t *testing.T) {
		testCommentSoftDelete(t, engine, gdb, actors, fixture)
	})

	t.Run("default ascending cursor stays stable without placeholders", func(t *testing.T) {
		testCommentAscendingCursorWithoutPlaceholders(t, engine, gdb, actors, fixture)
	})

	t.Run("hot composite cursor stays stable", func(t *testing.T) {
		testCommentHotCursor(t, engine, gdb, actors, fixture)
	})

	t.Run("like is idempotent and recreatable", func(t *testing.T) {
		testCommentLikes(t, engine, gdb, actors, fixture)
	})

	t.Run("deleted post comments are read only for post author", func(t *testing.T) {
		testDeletedPostCommentsReadOnly(t, engine, actors, fixture)
	})

	t.Run("moderation failures roll back comment transaction", func(t *testing.T) {
		testCommentModerationFailureRollback(t, gdb, database, actors, fixture)
	})
}

func testCommentRouteInventory(t *testing.T, engine *server.Hertz) {
	t.Helper()
	operations := make([]string, 0)
	for _, route := range engine.Routes() {
		if strings.HasPrefix(route.Path, "/api/v2/comments") ||
			(strings.HasPrefix(route.Path, "/api/v2/posts/") && strings.Contains(route.Path, "/comments")) {
			operations = append(operations, route.Method+" "+route.Path)
		}
	}
	require.ElementsMatch(t, []string{
		"GET /api/v2/posts/:post_id/comments",
		"POST /api/v2/posts/:post_id/comments",
		"GET /api/v2/comments/:comment_id/replies",
		"PUT /api/v2/comments/:comment_id",
		"GET /api/v2/comments/:comment_id/history",
		"POST /api/v2/comments/:comment_id/like",
		"DELETE /api/v2/comments/:comment_id/like",
		"DELETE /api/v2/comments/:comment_id",
	}, operations)
}

func testCommentCreateContract(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	actors commentActors,
	fixture postFixture,
) {
	t.Helper()
	post := createPost(t, engine, actors.PostAuthor.Token,
		sharePostPayload(fixture, "评论链帖子", []string{"评论链"}))
	root := createComment(t, engine, actors.Commenter.Token, post.ID, map[string]any{
		"content": "@被提及者 楼主评论", "mentioned_user_ids": []uint64{actors.Mentioned.User.ID},
	})
	firstReply := createComment(t, engine, actors.Replier.Token, post.ID, map[string]any{
		"content": "回复楼主", "parent_id": root.Comment.ID,
	})
	secondReply := createComment(t, engine, actors.PostAuthor.Token, post.ID, map[string]any{
		"content": "@被提及者 回复楼内回复", "parent_id": firstReply.Comment.ID,
		"mentioned_user_ids": []uint64{actors.Mentioned.User.ID, actors.Mentioned.User.ID},
	})

	var rootRow, firstRow, secondRow model.Comment
	require.NoError(t, gdb.First(&rootRow, root.Comment.ID).Error)
	require.NoError(t, gdb.First(&firstRow, firstReply.Comment.ID).Error)
	require.NoError(t, gdb.First(&secondRow, secondReply.Comment.ID).Error)
	require.Nil(t, rootRow.ParentID)
	require.Nil(t, rootRow.RootID)
	require.Equal(t, actors.PostAuthor.User.ID, rootRow.ReplyToUserID)
	require.Equal(t, rootRow.ID, *firstRow.ParentID)
	require.Equal(t, rootRow.ID, *firstRow.RootID)
	require.Equal(t, actors.Commenter.User.ID, firstRow.ReplyToUserID)
	require.Equal(t, firstRow.ID, *secondRow.ParentID, "parent_id 必须保留真实回复链")
	require.Equal(t, rootRow.ID, *secondRow.RootID, "root_id 必须始终指向所属楼")
	require.Equal(t, actors.Replier.User.ID, secondRow.ReplyToUserID)
	require.EqualValues(t, 2, rootRow.ReplyCount)
	require.Zero(t, firstRow.ReplyCount)
	require.Zero(t, secondRow.ReplyCount)

	for _, commentID := range []uint64{rootRow.ID, firstRow.ID, secondRow.ID} {
		var histories []model.CommentHistory
		require.NoError(t, gdb.Where("comment_id = ?", commentID).Find(&histories).Error)
		require.Empty(t, histories, "创建评论不得写入编辑历史")
		var moderation model.ModerationRecord
		require.NoError(t, gdb.Where("comment_id = ?", commentID).First(&moderation).Error)
		require.Equal(t, model.ModerationVerdictPass, moderation.Verdict)
	}

	var mentions []model.CommentMention
	require.NoError(t, gdb.Where("comment_id = ?", secondRow.ID).Find(&mentions).Error)
	require.Len(t, mentions, 1, "重复提及必须落成一条关联")
	require.Equal(t, actors.Mentioned.User.ID, mentions[0].UserID)

	status, response, _ := performJSON(t, engine, http.MethodGet,
		fmt.Sprintf("/api/v2/posts/%d/comments", post.ID), nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusOK, status)
	var listed service.CommentList
	decodeData(t, response, &listed)
	require.Len(t, listed.Comments, 1)
	require.EqualValues(t, 2, listed.Comments[0].ReplyCount)
	require.Len(t, listed.Comments[0].Replies, 2)
	require.Equal(t, firstReply.Comment.ID, listed.Comments[0].Replies[0].ID)
	require.Equal(t, secondReply.Comment.ID, listed.Comments[0].Replies[1].ID)
	require.Equal(t, actors.Replier.User.ID, listed.Comments[0].Replies[1].ReplyTo.ID)

	status, response, _ = performJSON(t, engine, http.MethodPost,
		fmt.Sprintf("/api/v2/posts/%d/comments", post.ID), map[string]any{
			"content": "伪造回复对象", "parent_id": root.Comment.ID,
			"reply_to_user_id": actors.Mentioned.User.ID,
		}, actors.Replier.Token)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, apierr.BizValidation, response.ErrorCode)
	requireFieldError(t, status, response, "reply_to_user_id", apierr.FieldConflict)

	status, response, _ = performJSON(t, engine, http.MethodGet,
		commentPath(root.Comment.ID)+"/replies?limit=1", nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusOK, status)
	var replies service.CommentReplies
	decodeData(t, response, &replies)
	require.True(t, replies.Pagination.HasMore)
	require.NotNil(t, replies.Pagination.NextCursor)
	require.Len(t, replies.Replies, 1)
	require.Equal(t, firstReply.Comment.ID, replies.Replies[0].ID)

	status, _, _ = performJSON(t, engine, http.MethodDelete,
		commentPath(firstReply.Comment.ID), nil, actors.Replier.Token)
	require.Equal(t, http.StatusOK, status)
	status, response, _ = performJSON(t, engine, http.MethodGet,
		commentPath(root.Comment.ID)+"/replies?limit=1&cursor="+*replies.Pagination.NextCursor,
		nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusOK, status)
	var secondReplyPage service.CommentReplies
	decodeData(t, response, &secondReplyPage)
	require.False(t, secondReplyPage.Pagination.HasMore)
	require.Nil(t, secondReplyPage.Pagination.NextCursor)
	require.Equal(t, []uint64{secondReply.Comment.ID}, commentItemIDs(secondReplyPage.Replies))

	status, response, _ = performJSON(t, engine, http.MethodGet,
		commentPath(secondReply.Comment.ID)+"/replies", nil, actors.PostAuthor.Token)
	requireFieldError(t, status, response, "comment_id", apierr.FieldConflict)
}

func testCommentCreateBoundaries(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	fixtures *testutil.Fixtures,
	actors commentActors,
	fixture postFixture,
) {
	t.Helper()
	firstPost := createPost(t, engine, actors.PostAuthor.Token,
		sharePostPayload(fixture, "评论父链边界甲", []string{"父链甲"}))
	secondPost := createPost(t, engine, actors.PostAuthor.Token,
		sharePostPayload(fixture, "评论父链边界乙", []string{"父链乙"}))
	root := createComment(t, engine, actors.Commenter.Token, firstPost.ID,
		map[string]any{"content": "父链根评论"})
	otherRoot := createComment(t, engine, actors.Commenter.Token, secondPost.ID,
		map[string]any{"content": "别帖根评论"})

	status, response, _ := performJSON(t, engine, http.MethodPost,
		fmt.Sprintf("/api/v2/posts/%d/comments", firstPost.ID), map[string]any{
			"content": "回复不存在评论", "parent_id": uint64(9_999_999_999),
		}, actors.Replier.Token)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizCommentNotFound, response.ErrorCode)
	status, response, _ = performJSON(t, engine, http.MethodPost,
		fmt.Sprintf("/api/v2/posts/%d/comments", firstPost.ID), map[string]any{
			"content": "零 parent id", "parent_id": 0,
		}, actors.Replier.Token)
	requireFieldError(t, status, response, "parent_id", apierr.FieldInvalidFormat)

	status, response, _ = performJSON(t, engine, http.MethodPost,
		fmt.Sprintf("/api/v2/posts/%d/comments", firstPost.ID), map[string]any{
			"content": "回复别帖评论", "parent_id": otherRoot.Comment.ID,
		}, actors.Replier.Token)
	requireFieldError(t, status, response, "parent_id", apierr.FieldConflict)

	deletedParent := createComment(t, engine, actors.Commenter.Token, firstPost.ID,
		map[string]any{"content": "即将删除的父评论"})
	status, _, _ = performJSON(t, engine, http.MethodDelete,
		commentPath(deletedParent.Comment.ID), nil, actors.Commenter.Token)
	require.Equal(t, http.StatusOK, status)
	status, response, _ = performJSON(t, engine, http.MethodPost,
		fmt.Sprintf("/api/v2/posts/%d/comments", firstPost.ID), map[string]any{
			"content": "回复已删除评论", "parent_id": deletedParent.Comment.ID,
		}, actors.Replier.Token)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizCommentDeleted, response.ErrorCode)

	for _, test := range []struct {
		name    string
		content string
		code    apierr.FieldCode
	}{
		{name: "empty", content: "　\n", code: apierr.FieldRequired},
		{name: "over unicode rune limit", content: strings.Repeat("评", 2001), code: apierr.FieldTooLong},
	} {
		t.Run(test.name, func(t *testing.T) {
			requestStatus, requestResponse, _ := performJSON(t, engine, http.MethodPost,
				fmt.Sprintf("/api/v2/posts/%d/comments", firstPost.ID),
				map[string]any{"content": test.content}, actors.Commenter.Token)
			requireFieldError(t, requestStatus, requestResponse, "content", test.code)
		})
	}

	var mentionsBeforeForgery int64
	mentionNotifications := gdb.Model(&model.Notification{}).Where(
		"recipient_id = ? AND sender_id = ? AND type = ?",
		actors.Mentioned.User.ID, actors.Commenter.User.ID, model.NotificationTypeMention,
	)
	require.NoError(t, mentionNotifications.Count(&mentionsBeforeForgery).Error)
	status, response, _ = performJSON(t, engine, http.MethodPost,
		fmt.Sprintf("/api/v2/posts/%d/comments", firstPost.ID), map[string]any{
			"content":            "正文没有任何 @ 标记",
			"mentioned_user_ids": []uint64{actors.Mentioned.User.ID},
		}, actors.Commenter.Token)
	requireFieldError(t, status, response, "mentioned_user_ids", apierr.FieldConflict)
	var mentionsAfterForgery int64
	require.NoError(t, mentionNotifications.Count(&mentionsAfterForgery).Error)
	require.Equal(t, mentionsBeforeForgery, mentionsAfterForgery,
		"正文未 @ 用户时不得产生提及通知")

	status, response, _ = performJSON(t, engine, http.MethodPost,
		fmt.Sprintf("/api/v2/posts/%d/comments", firstPost.ID), map[string]any{
			"content": "提及不存在用户", "mentioned_user_ids": []uint64{9_999_999_999},
		}, actors.Commenter.Token)
	requireFieldError(t, status, response, "mentioned_user_ids", apierr.FieldConflict)

	deletedUser := fixtures.CreateUser(testutil.WithDeletedUser(time.Now().UTC()))
	status, response, _ = performJSON(t, engine, http.MethodPost,
		fmt.Sprintf("/api/v2/posts/%d/comments", firstPost.ID), map[string]any{
			"content": "提及已注销用户", "mentioned_user_ids": []uint64{deletedUser.ID},
		}, actors.Commenter.Token)
	requireFieldError(t, status, response, "mentioned_user_ids", apierr.FieldConflict)

	selfMention := createComment(t, engine, actors.Commenter.Token, firstPost.ID, map[string]any{
		"content": "@评论作者" + strings.Repeat("界", 1995), "parent_id": root.Comment.ID,
		"mentioned_user_ids": []uint64{actors.Commenter.User.ID},
	})
	var mention model.CommentMention
	require.NoError(t, gdb.Where(
		"comment_id = ? AND user_id = ?", selfMention.Comment.ID, actors.Commenter.User.ID,
	).First(&mention).Error)
	var selfNotifications int64
	require.NoError(t, gdb.Model(&model.Notification{}).Where(
		"recipient_id = ? AND sender_id = ? AND type = ? AND related_comment_id = ?",
		actors.Commenter.User.ID, actors.Commenter.User.ID,
		model.NotificationTypeMention, selfMention.Comment.ID,
	).Count(&selfNotifications).Error)
	require.Zero(t, selfNotifications, "提及自己保留关系但不得给自己发通知")

	departed := fixtures.CreateUser()
	var firstPostRow model.Post
	require.NoError(t, gdb.First(&firstPostRow, firstPost.ID).Error)
	departedComment := fixtures.CreateComment(firstPostRow, departed.ID,
		testutil.WithCommentContent("已注销作者评论仍保留"))
	departedAt := time.Now().UTC()
	require.NoError(t, gdb.Model(&model.User{}).Where("id = ?", departed.ID).
		UpdateColumn("deleted_at", departedAt).Error)
	status, response, _ = performJSON(t, engine, http.MethodGet,
		fmt.Sprintf("/api/v2/posts/%d/comments?limit=100", firstPost.ID), nil, actors.Commenter.Token)
	require.Equal(t, http.StatusOK, status)
	var departedList service.CommentList
	decodeData(t, response, &departedList)
	foundDeparted := false
	for _, item := range departedList.Comments {
		if item.ID == departedComment.Comment.ID {
			foundDeparted = true
			require.Equal(t, "已注销用户", item.Author.Name)
			require.Nil(t, item.Author.AvatarURL)
		}
	}
	require.True(t, foundDeparted)

	for _, path := range []string{
		fmt.Sprintf("/api/v2/posts/%d/comments?sort_by=unknown", firstPost.ID),
		fmt.Sprintf("/api/v2/posts/%d/comments?cursor=not-a-cursor", firstPost.ID),
		commentPath(root.Comment.ID) + "/replies?limit=101",
	} {
		status, response, _ = performJSON(t, engine, http.MethodGet, path, nil, actors.Commenter.Token)
		require.Equal(t, http.StatusUnprocessableEntity, status, "path=%s", path)
		require.Equal(t, apierr.BizValidation, response.ErrorCode)
	}
}

func testCommentEditVersion(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	actors commentActors,
	fixture postFixture,
) {
	t.Helper()
	post := createPost(t, engine, actors.PostAuthor.Token,
		sharePostPayload(fixture, "评论编辑帖子", []string{"评论编辑"}))
	created := createComment(t, engine, actors.Commenter.Token, post.ID, map[string]any{
		"content": "@被提及者 编辑前", "mentioned_user_ids": []uint64{actors.Mentioned.User.ID},
	})
	status, response, _ := performJSON(t, engine, http.MethodPut, commentPath(created.Comment.ID), map[string]any{
		"content": "@回复作者 编辑后的正文", "mentioned_user_ids": []uint64{actors.Replier.User.ID},
	}, actors.Commenter.Token)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
	var updated service.CommentMutationResult
	decodeData(t, response, &updated)
	require.Equal(t, "@回复作者 编辑后的正文", updated.Comment.Content)
	require.True(t, updated.Comment.IsEdited)

	var stored model.Comment
	require.NoError(t, gdb.First(&stored, created.Comment.ID).Error)
	require.Equal(t, "@回复作者 编辑后的正文", stored.Content)
	var histories []model.CommentHistory
	require.NoError(t, gdb.Where("comment_id = ?", stored.ID).Order("revision").Find(&histories).Error)
	require.Equal(t, []int32{1}, commentRevisions(histories))
	require.Equal(t, []string{"@被提及者 编辑前"}, commentContents(histories))
	var mentionIDs []uint64
	require.NoError(t, gdb.Model(&model.CommentMention{}).Where("comment_id = ?", stored.ID).
		Pluck("user_id", &mentionIDs).Error)
	require.Equal(t, []uint64{actors.Replier.User.ID}, mentionIDs)

	for _, content := range []string{"第三版正文", "第四版正文"} {
		status, response, _ = performJSON(t, engine, http.MethodPut, commentPath(stored.ID), map[string]any{
			"content": content, "mentioned_user_ids": []uint64{},
		}, actors.Commenter.Token)
		require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
	}
	require.NoError(t, gdb.First(&stored, created.Comment.ID).Error)
	require.Equal(t, "第四版正文", stored.Content)
	require.NoError(t, gdb.Where("comment_id = ?", stored.ID).Order("revision").Find(&histories).Error)
	require.Equal(t, []int32{1, 2, 3}, commentRevisions(histories))
	require.Equal(t, []string{
		"@被提及者 编辑前", "@回复作者 编辑后的正文", "第三版正文",
	}, commentContents(histories))
	var moderationCount int64
	require.NoError(t, gdb.Model(&model.ModerationRecord{}).
		Where("comment_id = ?", stored.ID).Count(&moderationCount).Error)
	require.EqualValues(t, 4, moderationCount, "创建和每次编辑必须分别产生审核记录")
	var moderationRevisions []int32
	require.NoError(t, gdb.Model(&model.ModerationRecord{}).Where("comment_id = ?", stored.ID).
		Order("id").Pluck("content_revision", &moderationRevisions).Error)
	require.Equal(t, []int32{1, 2, 3, 4}, moderationRevisions,
		"编辑后的审核记录必须绑定新当前版本")

	status, response, _ = performJSON(t, engine, http.MethodGet,
		commentPath(stored.ID)+"/history", nil, actors.Commenter.Token)
	require.Equal(t, http.StatusOK, status)
	var historyList service.CommentHistoryList
	decodeData(t, response, &historyList)
	require.Equal(t, []int32{3, 2, 1}, []int32{
		historyList.Histories[0].Revision,
		historyList.Histories[1].Revision,
		historyList.Histories[2].Revision,
	})

	status, _, _ = performJSON(t, engine, http.MethodPut, commentPath(stored.ID), map[string]any{
		"content": "越权编辑",
	}, actors.Replier.Token)
	require.Equal(t, http.StatusForbidden, status)
}

func testCommentMutationGuards(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	actors commentActors,
	fixture postFixture,
) {
	t.Helper()
	post := createPost(t, engine, actors.PostAuthor.Token,
		sharePostPayload(fixture, "评论变更守卫", []string{"评论守卫"}))
	comment := createComment(t, engine, actors.Commenter.Token, post.ID,
		map[string]any{"content": "评论变更守卫目标"})

	status, response, _ := performJSON(t, engine, http.MethodPut,
		commentPath(comment.Comment.ID), map[string]any{"content": "越权编辑"}, actors.Replier.Token)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, apierr.BizNotOwner, response.ErrorCode)
	status, response, _ = performJSON(t, engine, http.MethodDelete,
		commentPath(comment.Comment.ID), nil, actors.Replier.Token)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, apierr.BizNotOwner, response.ErrorCode)

	status, _, _ = performJSON(t, engine, http.MethodDelete,
		commentPath(comment.Comment.ID), nil, actors.Commenter.Token)
	require.Equal(t, http.StatusOK, status)
	var stored model.Comment
	require.NoError(t, gdb.First(&stored, comment.Comment.ID).Error)
	require.NotNil(t, stored.DeletedAt)
	status, response, _ = performJSON(t, engine, http.MethodGet,
		commentPath(comment.Comment.ID)+"/history", nil, actors.Commenter.Token)
	require.Equal(t, http.StatusOK, status, "作者仍可查看已删除评论的修改记录")
	status, response, _ = performJSON(t, engine, http.MethodGet,
		commentPath(comment.Comment.ID)+"/history", nil, actors.Replier.Token)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, apierr.BizNotOwner, response.ErrorCode)

	requests := []struct {
		method string
		path   string
		body   any
		code   apierr.BizCode
	}{
		{method: http.MethodPut, path: commentPath(comment.Comment.ID), body: map[string]any{"content": "删除后编辑"}, code: apierr.BizCommentDeleted},
		{method: http.MethodPost, path: commentPath(comment.Comment.ID) + "/like", code: apierr.BizCommentNotFound},
		{method: http.MethodDelete, path: commentPath(comment.Comment.ID) + "/like", code: apierr.BizCommentNotFound},
		{method: http.MethodDelete, path: commentPath(comment.Comment.ID), code: apierr.BizCommentDeleted},
	}
	for _, request := range requests {
		status, response, _ = performJSON(
			t, engine, request.method, request.path, request.body, actors.Commenter.Token,
		)
		require.Equal(t, http.StatusNotFound, status, "method=%s path=%s", request.method, request.path)
		require.Equal(t, request.code, response.ErrorCode)
	}
}

func testConcurrentCommentEdits(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	database *dbinfra.DB,
	actors commentActors,
	fixture postFixture,
) {
	t.Helper()
	post := createPost(t, engine, actors.PostAuthor.Token,
		sharePostPayload(fixture, "评论并发帖子", []string{"评论并发"}))
	created := createComment(t, engine, actors.Commenter.Token, post.ID, map[string]any{"content": "并发初始"})
	commentService := service.NewCommentService(service.DirectPassContentModerator{})
	contents := []string{"并发编辑 A", "并发编辑 B"}
	errorsCh := make(chan error, len(contents))
	var wait sync.WaitGroup
	for _, content := range contents {
		wait.Add(1)
		go func(value string) {
			defer wait.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			errorsCh <- database.RunInTx(ctx, func(txCtx context.Context) error {
				_, err := commentService.Update(txCtx, created.Comment.ID,
					service.UpdateCommentInput{Content: value}, actors.Commenter.User.ID)
				return err
			})
		}(content)
	}
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		require.NoError(t, err)
	}
	var histories []model.CommentHistory
	require.NoError(t, gdb.Where("comment_id = ?", created.Comment.ID).
		Order("revision").Find(&histories).Error)
	require.Equal(t, []int32{1, 2}, commentRevisions(histories),
		"并发编辑必须从 1 开始且不得重复或跳号")
	var stored model.Comment
	require.NoError(t, gdb.First(&stored, created.Comment.ID).Error)
	require.ElementsMatch(t, []string{"并发初始", "并发编辑 A", "并发编辑 B"}, []string{
		histories[0].Content, histories[1].Content, stored.Content,
	})
}

func testCommentHistoryFailureRollback(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	actors commentActors,
	fixture postFixture,
) {
	t.Helper()
	post := createPost(t, engine, actors.PostAuthor.Token,
		sharePostPayload(fixture, "评论回滚帖子", []string{"评论回滚"}))
	created := createComment(t, engine, actors.Commenter.Token, post.ID, map[string]any{
		"content": "@被提及者 回滚前", "mentioned_user_ids": []uint64{actors.Mentioned.User.ID},
	})
	installCommentHistoryFailureTrigger(t, gdb)
	status, response, _ := performJSON(t, engine, http.MethodPut, commentPath(created.Comment.ID), map[string]any{
		"content": "@回复作者 不应提交", "mentioned_user_ids": []uint64{actors.Replier.User.ID},
	}, actors.Commenter.Token)
	require.Equal(t, http.StatusInternalServerError, status)
	require.Equal(t, apierr.BizInternal, response.ErrorCode)
	removeCommentHistoryFailureTrigger(t, gdb)

	var stored model.Comment
	require.NoError(t, gdb.First(&stored, created.Comment.ID).Error)
	require.Equal(t, "@被提及者 回滚前", stored.Content)
	var mentionIDs []uint64
	require.NoError(t, gdb.Model(&model.CommentMention{}).Where("comment_id = ?", stored.ID).
		Pluck("user_id", &mentionIDs).Error)
	require.Equal(t, []uint64{actors.Mentioned.User.ID}, mentionIDs)
	var historyCount int64
	require.NoError(t, gdb.Model(&model.CommentHistory{}).
		Where("comment_id = ?", stored.ID).Count(&historyCount).Error)
	require.Zero(t, historyCount)
	var moderationCount int64
	require.NoError(t, gdb.Model(&model.ModerationRecord{}).
		Where("comment_id = ?", stored.ID).Count(&moderationCount).Error)
	require.EqualValues(t, 1, moderationCount, "历史失败时不得追加审核流水")

	installCommentHistoryFailureTrigger(t, gdb)
	status, response, _ = performJSON(t, engine, http.MethodPost,
		fmt.Sprintf("/api/v2/posts/%d/comments", post.ID),
		map[string]any{"content": "创建不写历史"}, actors.Commenter.Token)
	require.Equal(t, http.StatusOK, status, "新建评论不得触发 history insert")
	removeCommentHistoryFailureTrigger(t, gdb)
	var failedCreateCount int64
	require.NoError(t, gdb.Model(&model.Comment{}).
		Where("post_id = ? AND content = ?", post.ID, "创建不写历史").Count(&failedCreateCount).Error)
	require.EqualValues(t, 1, failedCreateCount)
}

func testCommentModerationReview(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	database *dbinfra.DB,
	actors commentActors,
	fixture postFixture,
) {
	t.Helper()
	post := createPost(t, engine, actors.PostAuthor.Token,
		sharePostPayload(fixture, "评论复核帖子", []string{"评论复核"}))
	commentService := service.NewCommentService(fixedVerdictModerator(model.ModerationVerdictReview))
	var result *service.CommentMutationResult
	err := database.RunInTx(context.Background(), func(ctx context.Context) error {
		var createErr error
		result, createErr = commentService.Create(ctx, post.ID,
			service.CreateCommentInput{Content: "需人工复核但仅作者可见"}, actors.Commenter.User.ID)
		return createErr
	})
	require.NoError(t, err)
	require.Equal(t, model.ModerationStatusReview, result.Comment.Moderation)
	require.Equal(t, "需人工复核但仅作者可见", result.Comment.Content)

	var stored model.Comment
	require.NoError(t, gdb.First(&stored, result.Comment.ID).Error)
	require.Nil(t, stored.DeletedAt)
	require.Nil(t, stored.DeletedReason)
	require.Nil(t, stored.DeletedBy)
	require.Equal(t, model.ModerationStatusReview, stored.Moderation)
	assertCommentModerationEvidence(t, gdb, stored.ID, model.ModerationVerdictReview)
	var postRow model.Post
	require.NoError(t, gdb.First(&postRow, post.ID).Error)
	require.Zero(t, postRow.CommentCount, "待复核评论不得计入帖子评论数")
	var reviewNotifications int64
	require.NoError(t, gdb.Model(&model.Notification{}).Where(
		"related_post_id = ? AND sender_id = ?", post.ID, actors.Commenter.User.ID,
	).Count(&reviewNotifications).Error)
	require.Zero(t, reviewNotifications, "待复核评论不得通过通知正文向他人泄露")

	status, response, _ := performJSON(t, engine, http.MethodGet,
		fmt.Sprintf("/api/v2/posts/%d/comments", post.ID), nil, actors.Replier.Token)
	require.Equal(t, http.StatusOK, status)
	var listed service.CommentList
	decodeData(t, response, &listed)
	require.Empty(t, listed.Comments, "非作者不得看到待复核评论")

	status, response, _ = performJSON(t, engine, http.MethodGet,
		fmt.Sprintf("/api/v2/posts/%d/comments", post.ID), nil, actors.Commenter.Token)
	require.Equal(t, http.StatusOK, status)
	decodeData(t, response, &listed)
	require.Len(t, listed.Comments, 1)
	require.Equal(t, stored.ID, listed.Comments[0].ID)
	require.Equal(t, model.ModerationStatusReview, listed.Comments[0].Moderation)
	require.Equal(t, stored.Content, listed.Comments[0].Content)

	status, response, _ = performJSON(t, engine, http.MethodGet,
		commentPath(stored.ID)+"/replies", nil, actors.Replier.Token)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizCommentNotFound, response.ErrorCode)
	status, response, _ = performJSON(t, engine, http.MethodPost,
		commentPath(stored.ID)+"/like", nil, actors.Replier.Token)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizCommentNotFound, response.ErrorCode)

	visibleRoot := createComment(t, engine, actors.Replier.Token, post.ID,
		map[string]any{"content": "公开楼主评论"})
	parentID := visibleRoot.Comment.ID
	var reviewReply *service.CommentMutationResult
	err = database.RunInTx(context.Background(), func(ctx context.Context) error {
		var createErr error
		reviewReply, createErr = commentService.Create(ctx, post.ID, service.CreateCommentInput{
			Content: "待复核回复", ParentID: &parentID,
		}, actors.Commenter.User.ID)
		return createErr
	})
	require.NoError(t, err)
	require.Equal(t, model.ModerationStatusReview, reviewReply.Comment.Moderation)
	var visibleRootRow model.Comment
	require.NoError(t, gdb.First(&visibleRootRow, visibleRoot.Comment.ID).Error)
	require.Zero(t, visibleRootRow.ReplyCount, "待复核回复不得计入楼层回复数")
	require.NoError(t, gdb.First(&postRow, post.ID).Error)
	require.EqualValues(t, 1, postRow.CommentCount, "只统计通过审核的公开楼主评论")

	status, response, _ = performJSON(t, engine, http.MethodGet,
		commentPath(visibleRoot.Comment.ID)+"/replies", nil, actors.Replier.Token)
	require.Equal(t, http.StatusOK, status)
	var publicReplies service.CommentReplies
	decodeData(t, response, &publicReplies)
	require.Empty(t, publicReplies.Replies)
	status, response, _ = performJSON(t, engine, http.MethodGet,
		commentPath(visibleRoot.Comment.ID)+"/replies", nil, actors.Commenter.Token)
	require.Equal(t, http.StatusOK, status)
	decodeData(t, response, &publicReplies)
	require.Equal(t, []uint64{reviewReply.Comment.ID}, commentItemIDs(publicReplies.Replies))
	require.Equal(t, model.ModerationStatusReview, publicReplies.Replies[0].Moderation)
}

func testCommentModerationBlock(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	database *dbinfra.DB,
	actors commentActors,
	fixture postFixture,
) {
	t.Helper()
	post := createPost(t, engine, actors.PostAuthor.Token,
		sharePostPayload(fixture, "评论违规帖子", []string{"评论违规"}))
	commentService := service.NewCommentService(fixedVerdictModerator(model.ModerationVerdictBlock))
	var result *service.CommentMutationResult
	err := database.RunInTx(context.Background(), func(ctx context.Context) error {
		var createErr error
		result, createErr = commentService.Create(ctx, post.ID,
			service.CreateCommentInput{Content: "机审违规内容"}, actors.Commenter.User.ID)
		return createErr
	})
	require.NoError(t, err)
	require.Equal(t, model.ModerationStatusBlock, result.Comment.Moderation)
	require.Equal(t, "机审违规内容", result.Comment.Content)

	var stored model.Comment
	require.NoError(t, gdb.First(&stored, result.Comment.ID).Error)
	require.NotNil(t, stored.DeletedAt)
	require.Equal(t, model.DeleteReasonModeration, *stored.DeletedReason)
	require.Nil(t, stored.DeletedBy)
	require.Equal(t, model.ModerationStatusBlock, stored.Moderation)
	require.Equal(t, "机审违规内容", stored.Content, "软删除不得清空或覆盖原文")
	assertCommentModerationEvidence(t, gdb, stored.ID, model.ModerationVerdictBlock)
	var postRow model.Post
	require.NoError(t, gdb.First(&postRow, post.ID).Error)
	require.Zero(t, postRow.CommentCount)
	status, response, _ := performJSON(t, engine, http.MethodGet,
		fmt.Sprintf("/api/v2/posts/%d/comments", post.ID), nil, actors.Replier.Token)
	require.Equal(t, http.StatusOK, status)
	var listed service.CommentList
	decodeData(t, response, &listed)
	require.Empty(t, listed.Comments, "违规评论不得向非作者公开")

	status, response, recorder := performJSON(t, engine, http.MethodGet,
		fmt.Sprintf("/api/v2/posts/%d/comments", post.ID), nil, actors.Commenter.Token)
	require.Equal(t, http.StatusOK, status)
	decodeData(t, response, &listed)
	require.Len(t, listed.Comments, 1)
	require.Equal(t, stored.ID, listed.Comments[0].ID)
	require.Equal(t, model.ModerationStatusBlock, listed.Comments[0].Moderation)
	require.Equal(t, stored.Content, listed.Comments[0].Content)
	require.NotContains(t, string(recorder.Result().Body()), "该评论已删除")
	require.NotContains(t, string(recorder.Result().Body()), `"is_deleted"`)
}

func assertCommentModerationEvidence(
	t *testing.T,
	gdb *gorm.DB,
	commentID uint64,
	verdict model.ModerationVerdict,
) {
	t.Helper()
	var historyCount, moderationCount int64
	require.NoError(t, gdb.Model(&model.CommentHistory{}).
		Where("comment_id = ?", commentID).Count(&historyCount).Error)
	require.NoError(t, gdb.Model(&model.ModerationRecord{}).
		Where("comment_id = ? AND verdict = ?", commentID, verdict).Count(&moderationCount).Error)
	require.Zero(t, historyCount, "创建后的审核不应制造编辑历史")
	require.EqualValues(t, 1, moderationCount)
}

func testCommentSoftDelete(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	actors commentActors,
	fixture postFixture,
) {
	t.Helper()
	post := createPost(t, engine, actors.PostAuthor.Token,
		sharePostPayload(fixture, "评论软删帖子", []string{"评论软删"}))
	root := createComment(t, engine, actors.Commenter.Token, post.ID, map[string]any{"content": "要删除的楼主"})
	reply := createComment(t, engine, actors.Replier.Token, post.ID, map[string]any{
		"content": "必须保留的回复", "parent_id": root.Comment.ID,
	})
	status, _, _ := performJSON(t, engine, http.MethodDelete,
		commentPath(root.Comment.ID), nil, actors.Commenter.Token)
	require.Equal(t, http.StatusOK, status)

	var rootRow, replyRow model.Comment
	require.NoError(t, gdb.First(&rootRow, root.Comment.ID).Error)
	require.NoError(t, gdb.First(&replyRow, reply.Comment.ID).Error)
	require.NotNil(t, rootRow.DeletedAt)
	require.Equal(t, "要删除的楼主", rootRow.Content)
	require.Nil(t, replyRow.DeletedAt)
	require.Equal(t, model.ModerationStatusPass, replyRow.Moderation)
	require.Equal(t, rootRow.ID, *replyRow.RootID)
	require.EqualValues(t, 1, rootRow.ReplyCount)
	var postRow model.Post
	require.NoError(t, gdb.First(&postRow, post.ID).Error)
	require.Zero(t, postRow.CommentCount, "根评论隐藏后整层楼不得继续计入公开评论数")

	status, response, recorder := performJSON(t, engine, http.MethodGet,
		fmt.Sprintf("/api/v2/posts/%d/comments", post.ID), nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusOK, status)
	var listed service.CommentList
	decodeData(t, response, &listed)
	require.Empty(t, listed.Comments, "根评论删除后整层楼对前台不可见")
	require.NotContains(t, string(recorder.Result().Body()), "该评论已删除")
	require.NotContains(t, string(recorder.Result().Body()), `"is_deleted"`)

	status, response, _ = performJSON(t, engine, http.MethodGet,
		commentPath(root.Comment.ID)+"/replies", nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizCommentNotFound, response.ErrorCode)

	status, response, _ = performJSON(t, engine, http.MethodDelete,
		commentPath(root.Comment.ID), nil, actors.Commenter.Token)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizCommentDeleted, response.ErrorCode)

	status, _, _ = performJSON(t, engine, http.MethodDelete,
		commentPath(reply.Comment.ID), nil, actors.Replier.Token)
	require.Equal(t, http.StatusOK, status)
	require.NoError(t, gdb.First(&rootRow, root.Comment.ID).Error)
	require.Zero(t, rootRow.ReplyCount)
	require.NoError(t, gdb.First(&postRow, post.ID).Error)
	require.Zero(t, postRow.CommentCount)
}

func testCommentAscendingCursorWithoutPlaceholders(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	actors commentActors,
	fixture postFixture,
) {
	t.Helper()
	post := createPost(t, engine, actors.PostAuthor.Token,
		sharePostPayload(fixture, "评论正序稳定分页", []string{"评论正序分页"}))
	createdAt := time.Date(2026, time.August, 23, 14, 0, 0, 111111000, time.UTC)
	comments := make([]service.CommentMutationResult, 0, 5)
	for index := range 5 {
		comment := createComment(t, engine, actors.Commenter.Token, post.ID,
			map[string]any{"content": fmt.Sprintf("正序评论 %d", index)})
		require.NoError(t, gdb.Model(&model.Comment{}).Where("id = ?", comment.Comment.ID).
			UpdateColumn("created_at", createdAt.Add(time.Duration(index)*time.Microsecond)).Error)
		comments = append(comments, comment)
	}

	path := fmt.Sprintf("/api/v2/posts/%d/comments", post.ID)
	status, response, _ := performJSON(t, engine, http.MethodGet,
		path+"?limit=2", nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusOK, status)
	var first service.CommentList
	decodeData(t, response, &first)
	require.Equal(t, []uint64{comments[0].Comment.ID, comments[1].Comment.ID},
		commentItemIDs(first.Comments), "默认顺序必须是 created_at ASC, id ASC")
	require.True(t, first.Pagination.HasMore)
	require.NotNil(t, first.Pagination.NextCursor)

	status, _, _ = performJSON(t, engine, http.MethodDelete,
		commentPath(comments[0].Comment.ID), nil, actors.Commenter.Token)
	require.Equal(t, http.StatusOK, status)

	status, response, _ = performJSON(t, engine, http.MethodGet,
		path+"?limit=2&cursor="+*first.Pagination.NextCursor, nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusOK, status)
	var second service.CommentList
	decodeData(t, response, &second)
	require.Equal(t, []uint64{comments[2].Comment.ID, comments[3].Comment.ID},
		commentItemIDs(second.Comments), "删除第一页评论不得让第二页跳过未读评论")
	require.True(t, second.Pagination.HasMore)
	require.NotNil(t, second.Pagination.NextCursor)

	status, response, _ = performJSON(t, engine, http.MethodGet,
		path+"?limit=2&cursor="+*second.Pagination.NextCursor, nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusOK, status)
	var third service.CommentList
	decodeData(t, response, &third)
	require.Equal(t, []uint64{comments[4].Comment.ID}, commentItemIDs(third.Comments))
	require.False(t, third.Pagination.HasMore)
	require.Nil(t, third.Pagination.NextCursor)

	status, response, recorder := performJSON(t, engine, http.MethodGet,
		path+"?limit=100", nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusOK, status)
	var all service.CommentList
	decodeData(t, response, &all)
	require.Equal(t, []uint64{
		comments[1].Comment.ID, comments[2].Comment.ID,
		comments[3].Comment.ID, comments[4].Comment.ID,
	}, commentItemIDs(all.Comments))
	require.NotContains(t, string(recorder.Result().Body()), "该评论已删除")
	require.NotContains(t, string(recorder.Result().Body()), `"is_deleted"`)
}

func testCommentHotCursor(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	actors commentActors,
	fixture postFixture,
) {
	t.Helper()
	post := createPost(t, engine, actors.PostAuthor.Token,
		sharePostPayload(fixture, "评论热门游标", []string{"评论热门游标"}))
	createdAt := time.Date(2026, time.August, 23, 15, 0, 0, 222222000, time.UTC)
	comments := make([]service.CommentMutationResult, 0, 4)
	for index := range 4 {
		comment := createComment(t, engine, actors.Commenter.Token, post.ID,
			map[string]any{"content": fmt.Sprintf("热门评论 %d", index)})
		require.NoError(t, gdb.Model(&model.Comment{}).Where("id = ?", comment.Comment.ID).
			UpdateColumn("created_at", createdAt).Error)
		comments = append(comments, comment)
	}
	for _, comment := range comments[:2] {
		likeComment(t, engine, comment.Comment.ID, actors.Replier.Token)
		likeComment(t, engine, comment.Comment.ID, actors.Mentioned.Token)
	}
	likeComment(t, engine, comments[2].Comment.ID, actors.Replier.Token)

	path := fmt.Sprintf("/api/v2/posts/%d/comments?sort_by=hot&limit=2", post.ID)
	status, response, _ := performJSON(t, engine, http.MethodGet, path, nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusOK, status)
	var first service.CommentList
	decodeData(t, response, &first)
	require.Equal(t, []uint64{comments[1].Comment.ID, comments[0].Comment.ID},
		commentItemIDs(first.Comments), "同 like_count、created_at 时必须以 id DESC 打破平局")
	require.True(t, first.Pagination.HasMore)
	require.NotNil(t, first.Pagination.NextCursor)

	status, response, _ = performJSON(t, engine, http.MethodGet,
		path+"&cursor="+*first.Pagination.NextCursor, nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusOK, status)
	var second service.CommentList
	decodeData(t, response, &second)
	require.Equal(t, []uint64{comments[2].Comment.ID, comments[3].Comment.ID},
		commentItemIDs(second.Comments))
	require.False(t, second.Pagination.HasMore)
	require.Nil(t, second.Pagination.NextCursor)

	status, response, _ = performJSON(t, engine, http.MethodGet,
		fmt.Sprintf("/api/v2/posts/%d/comments?limit=2&cursor=%s", post.ID, *first.Pagination.NextCursor),
		nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusUnprocessableEntity, status, "hot 游标不得跨排序复用")
	requireFieldError(t, status, response, "cursor", apierr.FieldInvalidFormat)
}

func likeComment(t *testing.T, engine *server.Hertz, commentID uint64, token string) {
	t.Helper()
	status, response, _ := performJSON(
		t, engine, http.MethodPost, commentPath(commentID)+"/like", nil, token,
	)
	require.Equal(t, http.StatusOK, status, "message=%s", response.Message)
}

func testCommentLikes(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	actors commentActors,
	fixture postFixture,
) {
	t.Helper()
	post := createPost(t, engine, actors.PostAuthor.Token,
		sharePostPayload(fixture, "评论点赞帖子", []string{"评论点赞"}))
	comment := createComment(t, engine, actors.Commenter.Token, post.ID, map[string]any{"content": "点赞目标"})
	var stored model.Comment
	require.NoError(t, gdb.First(&stored, comment.Comment.ID).Error)
	updatedAt := stored.UpdatedAt
	likePath := commentPath(comment.Comment.ID) + "/like"
	for range 2 {
		status, response, _ := performJSON(t, engine, http.MethodPost, likePath, nil, actors.Replier.Token)
		require.Equal(t, http.StatusOK, status)
		var result service.CommentLikeResult
		decodeData(t, response, &result)
		require.True(t, result.IsLiked)
		require.EqualValues(t, 1, result.LikeCount)
	}
	for range 2 {
		status, response, _ := performJSON(t, engine, http.MethodDelete, likePath, nil, actors.Replier.Token)
		require.Equal(t, http.StatusOK, status)
		var result service.CommentLikeResult
		decodeData(t, response, &result)
		require.False(t, result.IsLiked)
		require.Zero(t, result.LikeCount)
	}
	status, response, _ := performJSON(t, engine, http.MethodPost, likePath, nil, actors.Replier.Token)
	require.Equal(t, http.StatusOK, status)
	var result service.CommentLikeResult
	decodeData(t, response, &result)
	require.EqualValues(t, 1, result.LikeCount, "取消后必须可以再次点赞")
	var likeCount int64
	require.NoError(t, gdb.Model(&model.CommentLike{}).
		Where("comment_id = ?", comment.Comment.ID).Count(&likeCount).Error)
	require.EqualValues(t, 1, likeCount)
	require.NoError(t, gdb.First(&stored, comment.Comment.ID).Error)
	require.True(t, stored.UpdatedAt.Equal(updatedAt), "点赞不得改写评论内容更新时间")

	status, _, _ = performJSON(t, engine, http.MethodDelete, likePath, nil, actors.Replier.Token)
	require.Equal(t, http.StatusOK, status)
	const concurrentLikes = 8
	ready := make(chan struct{}, concurrentLikes)
	start := make(chan struct{})
	results := make(chan asyncRequestResult, concurrentLikes)
	for range concurrentLikes {
		go func() {
			ready <- struct{}{}
			<-start
			requestStatus, requestResponse, raw, requestErr := performJSONRequest(
				engine, http.MethodPost, likePath, nil, actors.Replier.Token,
			)
			results <- asyncRequestResult{
				status: requestStatus, response: requestResponse, raw: raw, err: requestErr,
			}
		}()
	}
	for range concurrentLikes {
		<-ready
	}
	close(start)
	for range concurrentLikes {
		outcome := <-results
		require.NoError(t, outcome.err)
		require.Equal(t, http.StatusOK, outcome.status)
		var concurrent service.CommentLikeResult
		decodeData(t, outcome.response, &concurrent)
		require.EqualValues(t, 1, concurrent.LikeCount)
	}
	require.NoError(t, gdb.Model(&model.CommentLike{}).
		Where("comment_id = ?", comment.Comment.ID).Count(&likeCount).Error)
	require.EqualValues(t, 1, likeCount, "并发重复点赞只能形成一条关系")
}

func testDeletedPostCommentsReadOnly(
	t *testing.T,
	engine *server.Hertz,
	actors commentActors,
	fixture postFixture,
) {
	t.Helper()
	post := createPost(t, engine, actors.PostAuthor.Token,
		sharePostPayload(fixture, "删除帖子评论可见性", []string{"删帖评论"}))
	root := createComment(t, engine, actors.Commenter.Token, post.ID,
		map[string]any{"content": "帖子删除后不可见的评论"})
	reply := createComment(t, engine, actors.Replier.Token, post.ID,
		map[string]any{"content": "帖子删除后不可见的回复", "parent_id": root.Comment.ID})
	status, response, _ := performJSON(t, engine, http.MethodGet,
		fmt.Sprintf("/api/v2/posts/%d/comments", post.ID), nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusOK, status, "未删除帖子应保持正常可读")
	var beforeDelete service.CommentList
	decodeData(t, response, &beforeDelete)
	require.Equal(t, []uint64{root.Comment.ID}, commentItemIDs(beforeDelete.Comments))
	require.Equal(t, []uint64{reply.Comment.ID}, commentItemIDs(beforeDelete.Comments[0].Replies))

	status, _, _ = performJSON(t, engine, http.MethodDelete,
		postPath(post.ID), nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusOK, status)

	status, response, _ = performJSON(t, engine, http.MethodGet,
		fmt.Sprintf("/api/v2/posts/%d/comments", post.ID), nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusOK, status, "帖子作者应能读取自己已删除帖子的已有评论")
	var afterDelete service.CommentList
	decodeData(t, response, &afterDelete)
	require.Equal(t, []uint64{root.Comment.ID}, commentItemIDs(afterDelete.Comments))
	require.Equal(t, []uint64{reply.Comment.ID}, commentItemIDs(afterDelete.Comments[0].Replies))

	status, response, _ = performJSON(t, engine, http.MethodGet,
		commentPath(root.Comment.ID)+"/replies", nil, actors.PostAuthor.Token)
	require.Equal(t, http.StatusOK, status, "帖子作者应能读取自己已删除帖子的完整回复")
	var replies service.CommentReplies
	decodeData(t, response, &replies)
	require.Equal(t, []uint64{reply.Comment.ID}, commentItemIDs(replies.Replies))

	status, response, _ = performJSON(t, engine, http.MethodGet,
		fmt.Sprintf("/api/v2/posts/%d/comments", post.ID), nil, actors.Commenter.Token)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizPostDeleted, response.ErrorCode)
	status, response, _ = performJSON(t, engine, http.MethodGet,
		commentPath(root.Comment.ID)+"/replies", nil, actors.Commenter.Token)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizPostDeleted, response.ErrorCode)

	status, response, _ = performJSON(t, engine, http.MethodPost,
		fmt.Sprintf("/api/v2/posts/%d/comments", post.ID), map[string]any{"content": "删帖后新评论"},
		actors.PostAuthor.Token)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizPostDeleted, response.ErrorCode)

	for _, interaction := range []struct {
		method string
		suffix string
	}{
		{method: http.MethodPost, suffix: "/like"},
		{method: http.MethodDelete, suffix: "/like"},
		{method: http.MethodPost, suffix: "/favorite"},
		{method: http.MethodDelete, suffix: "/favorite"},
	} {
		status, response, _ = performJSON(t, engine, interaction.method,
			postPath(post.ID)+interaction.suffix, nil, actors.PostAuthor.Token)
		require.Equal(t, http.StatusNotFound, status)
		require.Equal(t, apierr.BizPostDeleted, response.ErrorCode)
	}

	for _, commentID := range []uint64{root.Comment.ID, reply.Comment.ID} {
		for _, method := range []string{http.MethodPost, http.MethodDelete} {
			status, response, _ = performJSON(t, engine, method,
				commentPath(commentID)+"/like", nil, actors.PostAuthor.Token)
			require.Equal(t, http.StatusConflict, status)
			require.Equal(t, apierr.BizPostNotPublished, response.ErrorCode)
		}
	}
	status, response, _ = performJSON(t, engine, http.MethodPut,
		commentPath(root.Comment.ID), map[string]any{"content": "删帖后修改评论"}, actors.Commenter.Token)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizPostDeleted, response.ErrorCode)
	status, response, _ = performJSON(t, engine, http.MethodDelete,
		commentPath(root.Comment.ID), nil, actors.Commenter.Token)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizPostDeleted, response.ErrorCode)

	status, response, _ = performJSON(t, engine, http.MethodGet,
		commentPath(root.Comment.ID)+"/history", nil, actors.Commenter.Token)
	require.Equal(t, http.StatusOK, status, "作者仍可审计自己在已删帖子下的评论历史")
}

func testCommentModerationFailureRollback(
	t *testing.T,
	gdb *gorm.DB,
	database *dbinfra.DB,
	actors commentActors,
	fixture postFixture,
) {
	t.Helper()
	postService := service.NewPostService(service.DirectPassContentModerator{})
	var post *service.PostCreateResult
	err := database.RunInTx(context.Background(), func(ctx context.Context) error {
		var createErr error
		post, createErr = postService.Create(
			ctx, createPostInput(t, fixture, "评论审核故障帖子", []string{}, nil), actors.PostAuthor.User.ID,
		)
		return createErr
	})
	require.NoError(t, err)

	for _, test := range []struct {
		name       string
		content    string
		moderation testutil.ContentModerationOutcome
		timeout    bool
	}{
		{
			name: "http 503", content: "评论审核 503 回滚",
			moderation: testutil.ContentHTTPFailure(http.StatusServiceUnavailable),
		},
		{
			name: "timeout", content: "评论审核超时回滚",
			moderation: testutil.ContentTimeout(), timeout: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			moderation := testutil.NewMockModeration()
			moderation.SetDefaultContent(test.moderation)
			commentService := service.NewCommentService(moderation)
			ctx := context.Background()
			var cancel context.CancelFunc
			if test.timeout {
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
			}
			err := database.RunInTx(ctx, func(txCtx context.Context) error {
				_, createErr := commentService.Create(txCtx, post.ID,
					service.CreateCommentInput{Content: test.content}, actors.Commenter.User.ID)
				return createErr
			})
			require.Error(t, err)
			if test.timeout {
				require.ErrorIs(t, err, context.DeadlineExceeded)
			} else {
				require.Equal(t, http.StatusServiceUnavailable, apierr.As(err).Status)
			}
			moderation.RequireContentCalls(t, 1)
			var commentCount int64
			require.NoError(t, gdb.Model(&model.Comment{}).
				Where("post_id = ? AND content = ?", post.ID, test.content).Count(&commentCount).Error)
			require.Zero(t, commentCount)
			var historyCount, recordCount int64
			require.NoError(t, gdb.Model(&model.CommentHistory{}).
				Where("content = ?", test.content).Count(&historyCount).Error)
			require.NoError(t, gdb.Model(&model.ModerationRecord{}).
				Where("comment_id IN (SELECT id FROM comments WHERE content = ?)", test.content).
				Count(&recordCount).Error)
			require.Zero(t, historyCount)
			require.Zero(t, recordCount)
		})
	}
}

func commentItemIDs(comments []service.CommentItem) []uint64 {
	ids := make([]uint64, 0, len(comments))
	for _, comment := range comments {
		ids = append(ids, comment.ID)
	}
	return ids
}

func registerCommentActors(
	t *testing.T,
	engine *server.Hertz,
	sender *captureEmailSender,
) commentActors {
	t.Helper()
	return commentActors{
		PostAuthor: registerPostTestUser(t, engine, sender, "comment-post-author@fdueat.com", "帖子作者"),
		Commenter:  registerPostTestUser(t, engine, sender, "comment-writer@fdueat.com", "评论作者"),
		Replier:    registerPostTestUser(t, engine, sender, "comment-replier@fdueat.com", "回复作者"),
		Mentioned:  registerPostTestUser(t, engine, sender, "comment-mentioned@fdueat.com", "被提及者"),
	}
}

func createComment(
	t *testing.T,
	engine *server.Hertz,
	token string,
	postID uint64,
	payload map[string]any,
) service.CommentMutationResult {
	t.Helper()
	status, response, _ := performJSON(t, engine, http.MethodPost,
		fmt.Sprintf("/api/v2/posts/%d/comments", postID), payload, token)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
	var result service.CommentMutationResult
	decodeData(t, response, &result)
	require.NotZero(t, result.Comment.ID)
	return result
}

func commentPath(commentID uint64) string {
	return fmt.Sprintf("/api/v2/comments/%d", commentID)
}

func commentRevisions(histories []model.CommentHistory) []int32 {
	revisions := make([]int32, 0, len(histories))
	for _, history := range histories {
		revisions = append(revisions, history.Revision)
	}
	return revisions
}

func commentContents(histories []model.CommentHistory) []string {
	contents := make([]string, 0, len(histories))
	for _, history := range histories {
		contents = append(contents, history.Content)
	}
	return contents
}

func installCommentHistoryFailureTrigger(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	require.NoError(t, gdb.Exec(`
		CREATE OR REPLACE FUNCTION comment_test_fail_history() RETURNS trigger
		LANGUAGE plpgsql AS $func$
		BEGIN
			RAISE EXCEPTION 'forced comment history failure';
		END;
		$func$;
		CREATE TRIGGER trg_comment_test_fail_history
		BEFORE INSERT ON comment_histories
		FOR EACH ROW EXECUTE FUNCTION comment_test_fail_history();
	`).Error)
	t.Cleanup(func() { removeCommentHistoryFailureTrigger(t, gdb) })
}

func removeCommentHistoryFailureTrigger(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	require.NoError(t, gdb.Exec(`
		DROP TRIGGER IF EXISTS trg_comment_test_fail_history ON comment_histories;
		DROP FUNCTION IF EXISTS comment_test_fail_history();
	`).Error)
}
