package testutil_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/jwtx"
	"github.com/jingyijun/danshi_backend_go/internal/testutil"
)

func TestCompleteWorldSatisfiesRealSchemaConstraints(t *testing.T) {
	harness := testutil.NewHarness(t)
	world := harness.CompleteWorld()

	require.Equal(t, model.UserRoleUser, world.Users.Ordinary.User.Role)
	require.Equal(t, model.UserRoleAdmin, world.Users.Admin.User.Role)
	require.Equal(t, model.UserRoleSuperAdmin, world.Users.SuperAdmin.User.Role)
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
	require.NotNil(t, world.Posts.Approved.History)

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

	invalid := model.User{
		Email: "invalid-role@fdueat.com", PasswordHash: "test",
		Role: model.UserRole("invalid"),
	}
	require.Error(t, harness.Database.GORM.Create(&invalid).Error,
		"真实 schema 必须拒绝夹具无法表示的非法角色")
}
