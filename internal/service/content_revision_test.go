package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/model"
)

func TestModerationWritebackKeepsSubmittedContentRevision(t *testing.T) {
	result := ModerationResult{
		Provider: model.ModerationProvider("delayed_provider"),
		Verdict:  model.ModerationVerdictReview,
	}

	postRecord := moderationRecordForPost(101, 2, result)
	require.NotNil(t, postRecord.ContentRevision)
	require.EqualValues(t, 2, *postRecord.ContentRevision,
		"写回结论必须使用调用审核时捕获的帖子版本")

	commentRecord := moderationRecordForComment(202, 3, result)
	require.NotNil(t, commentRecord.ContentRevision)
	require.EqualValues(t, 3, *commentRecord.ContentRevision,
		"写回结论必须使用调用审核时捕获的评论版本")
}
