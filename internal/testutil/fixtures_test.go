package testutil_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/jwtx"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestCompleteWorldSatisfiesRealSchemaConstraints(t *testing.T) {
	harness := testutil.NewHarness(t)
	world := harness.CompleteWorld()

	require.Empty(t, world.Users.Ordinary.User.Roles)
	require.Equal(t, []model.UserRole{model.UserRoleModerator}, world.Users.Admin.User.Roles)
	require.Equal(t, []model.UserRole{model.UserRoleSuperAdmin}, world.Users.SuperAdmin.User.Roles)
	require.NotNil(t, world.Users.Banned.User.BannedUntil)
	require.NotNil(t, world.Users.Banned.User.BanReason)
	require.NotNil(t, world.Users.Deleted.User.DeletedAt)
	claims, err := jwtx.NewCodec(harness.Config.JWTSecretKey).Parse(
		world.Users.Ordinary.Token, jwtx.TypeAccess,
	)
	require.NoError(t, err)
	userID, err := claims.UserID()
	require.NoError(t, err)
	require.EqualValues(t, world.Users.Ordinary.User.ID, userID)
	require.EqualValues(t, world.Users.Ordinary.Session.ID, claims.SessionID)

	require.Equal(t, model.PostStatusDraft, world.Posts.Draft.Post.Status)
	require.Equal(t, model.PostStatusPending, world.Posts.Pending.Post.Status)
	require.Equal(t, model.PostStatusApproved, world.Posts.Approved.Post.Status)
	require.Equal(t, model.PostStatusRejected, world.Posts.Rejected.Post.Status)
	require.Len(t, world.Posts.Approved.Tags, 1)
	require.Len(t, world.Posts.Approved.Flavors, 1)
	require.Len(t, world.Posts.WithImage.Images, 1)
	var postHistoryCount int64
	require.NoError(t, harness.Database.GORM.Model(&model.PostHistory{}).Count(&postHistoryCount).Error)
	require.EqualValues(t, 5, postHistoryCount)
	var commentHistoryCount int64
	require.NoError(t, harness.Database.GORM.Model(&model.CommentHistory{}).Count(&commentHistoryCount).Error)
	require.EqualValues(t, 3, commentHistoryCount)

	require.Nil(t, world.Comments.Root.Comment.ParentID)
	require.Equal(t, &world.Comments.Root.Comment.ID, world.Comments.Reply.Comment.ParentID)
	require.Equal(t, &world.Comments.Root.Comment.ID, world.Comments.Reply.Comment.RootID)
	require.Equal(t, &world.Comments.Reply.Comment.ID, world.Comments.Deep.Comment.ParentID)
	require.Equal(t, &world.Comments.Root.Comment.ID, world.Comments.Deep.Comment.RootID)

	var storedPost model.Post
	require.NoError(t, harness.Database.GORM.First(
		&storedPost, world.Posts.Approved.Post.ID,
	).Error)
	require.EqualValues(t, 3, storedPost.CommentCount)
	var storedRoot model.Comment
	require.NoError(t, harness.Database.GORM.First(
		&storedRoot, world.Comments.Root.Comment.ID,
	).Error)
	require.EqualValues(t, 2, storedRoot.ReplyCount)

	invalid := model.UserRoleBinding{
		UserID: world.Users.Ordinary.User.ID, Role: model.UserRole("invalid"),
	}
	require.Error(t, harness.Database.GORM.Create(&invalid).Error,
		"真实 schema 必须拒绝夹具无法表示的非法角色")
}
