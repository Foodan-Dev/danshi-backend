package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestAdminPostDeleteRollsBackWhenImageAccessIntentFailsAgainstPostgres18(t *testing.T) {
	database := testutil.OpenPostgres(t)
	fixtures := testutil.NewFixtures(t, database.GORM)
	author := fixtures.CreateUser()
	admin := fixtures.CreateUser(testutil.WithUserRole(model.UserRoleModerator))
	image := fixtures.CreateImage(author.ID)
	post := fixtures.CreatePost(author.ID, testutil.WithPostImages(image))
	controllerErr := errors.New("image access outbox unavailable")
	adminService := service.NewAdminService(
		service.DiscardModerationAlerter{},
		failingImageAccessController{err: controllerErr},
		nil,
		time.Minute,
	)

	err := database.DB.RunInTx(context.Background(), func(ctx context.Context) error {
		_, deleteErr := adminService.DeletePost(ctx, post.Post.ID, admin.ID)
		return deleteErr
	})
	require.ErrorIs(t, err, controllerErr)

	var storedPost model.Post
	require.NoError(t, database.GORM.First(&storedPost, post.Post.ID).Error)
	require.Nil(t, storedPost.DeletedAt, "intent 失败必须回滚帖子软删除")
	require.Nil(t, storedPost.DeletedReason)
	require.Nil(t, storedPost.DeletedBy)
	var storedImage model.ImageAsset
	require.NoError(t, database.GORM.First(&storedImage, image.ID).Error)
	require.Equal(t, model.ModerationStatusPass, storedImage.Moderation,
		"intent 失败必须回滚图片当前审核状态")
	var moderationCount int64
	require.NoError(t, database.GORM.Model(&model.ModerationRecord{}).
		Where("image_asset_id = ? AND provider = ?", image.ID, "admin_post_delete").
		Count(&moderationCount).Error)
	require.Zero(t, moderationCount, "intent 失败必须回滚追加审核流水")
	var intentCount int64
	require.NoError(t, database.GORM.Model(&model.ImageAccessIntent{}).
		Where("image_asset_id = ?", image.ID).Count(&intentCount).Error)
	require.Zero(t, intentCount)
}

type failingImageAccessController struct{ err error }

func (f failingImageAccessController) Apply(context.Context, service.ImageAccessChange) error {
	return f.err
}
