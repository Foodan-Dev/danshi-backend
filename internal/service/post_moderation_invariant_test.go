package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/model"
)

func TestAggregatePostModerationKeepsPendingImageUnpublishable(t *testing.T) {
	status, err := aggregatePostModeration(model.ModerationVerdictPass, []model.ImageAsset{{
		Moderation: model.ModerationStatusPending,
	}})
	require.NoError(t, err)
	require.Equal(t, model.PostStatusPending, status,
		"正文通过也不能让仍待图片结论的帖子进入公开状态")
}
