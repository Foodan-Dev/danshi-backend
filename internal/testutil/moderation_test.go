package testutil_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestMockModerationProgramsContentByTargetKeywordAndCall(t *testing.T) {
	mock := testutil.NewMockModeration()
	score := decimal.NewFromInt(87)
	mock.ProgramContent(
		testutil.ContentModerationRule{
			Target: service.ModerationTargetPost, Contains: "拦截",
			Outcome: testutil.ContentVerdict(
				model.ModerationVerdictBlock, []string{"abuse", "spam"}, &score,
			),
		},
		testutil.ContentModerationRule{
			Target: service.ModerationTargetComment,
			Outcome: testutil.ContentVerdict(
				model.ModerationVerdictReview, []string{"manual"}, nil,
			),
		},
		testutil.ContentModerationRule{
			Call: 3, Outcome: testutil.ContentInvalidVerdict(),
		},
	)

	first, err := mock.Review(context.Background(), service.ModerationRequest{
		Target: service.ModerationTargetPost, Text: "需要拦截的帖子",
	})
	require.NoError(t, err)
	require.Equal(t, model.ModerationVerdictBlock, first.Verdict)
	require.Equal(t, []string{"abuse", "spam"}, first.Labels)
	require.NotNil(t, first.Score)
	require.True(t, first.Score.Equal(score))

	second, err := mock.Review(context.Background(), service.ModerationRequest{
		Target: service.ModerationTargetComment, Text: "普通评论",
	})
	require.NoError(t, err)
	require.Equal(t, model.ModerationVerdictReview, second.Verdict)

	third, err := mock.Review(context.Background(), service.ModerationRequest{
		Target: service.ModerationTargetUser, Text: "第三次调用",
	})
	require.NoError(t, err)
	require.Equal(t, model.ModerationVerdict("invalid"), third.Verdict)
	mock.RequireContentCalls(t, 3)
	require.Equal(t, "需要拦截的帖子", mock.ContentCalls()[0].Text)
}

func TestMockModerationSupports5xxTimeoutAndRelease(t *testing.T) {
	mock := testutil.NewMockModeration()
	mock.SetDefaultContent(testutil.ContentHTTPFailure(http.StatusServiceUnavailable))
	_, err := mock.Review(context.Background(), service.ModerationRequest{})
	require.Error(t, err)
	require.Equal(t, http.StatusServiceUnavailable, apierr.As(err).Status)

	mock.SetDefaultContent(testutil.ContentTimeout())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = mock.Review(ctx, service.ModerationRequest{})
	require.ErrorIs(t, err, context.DeadlineExceeded)

	release := make(chan struct{})
	mock.SetDefaultContent(testutil.ContentModerationOutcome{
		Result:  testutil.ContentVerdict(model.ModerationVerdictPass, nil, nil).Result,
		Release: release,
	})
	done := make(chan error, 1)
	go func() {
		_, reviewErr := mock.Review(context.Background(), service.ModerationRequest{})
		done <- reviewErr
	}()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	require.True(t, mock.WaitForContentCalls(waitCtx, 3))
	select {
	case early := <-done:
		t.Fatalf("审核在释放前返回: %v", early)
	default:
	}
	close(release)
	require.NoError(t, <-done)
}

func TestMockModerationTriggersDuplicateOutOfOrderAndDeletedCallbacks(t *testing.T) {
	mock := testutil.NewMockModeration()
	mock.ProgramImage(
		testutil.ImageModerationRule{Call: 1, Outcome: testutil.ImagePending("job-1")},
		testutil.ImageModerationRule{Call: 2, Outcome: testutil.ImagePending("job-2")},
	)
	for index, key := range []string{"images/first.jpg", "images/second.jpg"} {
		submission, err := mock.SubmitImage(context.Background(), service.ImageModerationRequest{
			ImageAssetID: uint64(index + 1), ObjectKey: key,
		})
		require.NoError(t, err)
		require.NotNil(t, submission.ProviderJobID)
	}
	mock.RequireImageCalls(t, 2)

	seen := make(map[string]int)
	receiver := func(
		_ context.Context,
		callback service.ImageModerationCallback,
	) (*service.ImageModerationApplyResult, error) {
		if callback.ImageAssetID == 999 {
			return nil, errors.New("target was deleted")
		}
		seen[callback.ProviderJobID]++
		return &service.ImageModerationApplyResult{
			Duplicate: seen[callback.ProviderJobID] > 1,
		}, nil
	}
	score := decimal.NewFromInt(98)
	second, err := mock.TriggerImageCallback(
		context.Background(), "job-2", model.ModerationVerdictPass, receiver,
		testutil.CallbackLabels("safe"), testutil.CallbackScore(score),
		testutil.CallbackRawResponse(json.RawMessage(`{"job":"job-2"}`)),
	)
	require.NoError(t, err)
	require.False(t, second.Duplicate)
	first, err := mock.TriggerImageCallback(
		context.Background(), "job-1", model.ModerationVerdictReview, receiver,
	)
	require.NoError(t, err)
	require.False(t, first.Duplicate)
	duplicate, err := mock.TriggerImageCallback(
		context.Background(), "job-1", model.ModerationVerdictReview, receiver,
	)
	require.NoError(t, err)
	require.True(t, duplicate.Duplicate)
	_, err = mock.TriggerImageCallback(
		context.Background(), "job-2", model.ModerationVerdictBlock, receiver,
		testutil.CallbackImageAssetID(999),
	)
	require.EqualError(t, err, "target was deleted")

	mock.RequireCallbackOrder(t, "job-2", "job-1", "job-1", "job-2")
	calls := mock.CallbackCalls()
	require.Equal(t, []string{"safe"}, calls[0].Labels)
	require.NotNil(t, calls[0].Score)
	require.True(t, calls[0].Score.Equal(score))
	require.JSONEq(t, `{"job":"job-2"}`, string(calls[0].RawResponse))
}
