package tencentcloud

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	tencenterrors "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/errors"
	teo "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/teo/v20220901"

	"github.com/Foodan-Dev/danshi-backend/internal/config"
)

type edgeOneFakeResult struct {
	response *teo.CreatePurgeTaskResponse
	err      error
}

type fakeEdgeOnePurgeClient struct {
	mu       sync.Mutex
	results  []edgeOneFakeResult
	requests []*teo.CreatePurgeTaskRequest
	calledAt []time.Time
}

func (c *fakeEdgeOnePurgeClient) CreatePurgeTaskWithContext(
	_ context.Context,
	request *teo.CreatePurgeTaskRequest,
) (*teo.CreatePurgeTaskResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, request)
	c.calledAt = append(c.calledAt, time.Now())
	if len(c.results) == 0 {
		return successfulPurgeResponse("job-default"), nil
	}
	result := c.results[0]
	c.results = c.results[1:]
	return result.response, result.err
}

func successfulPurgeResponse(jobID string) *teo.CreatePurgeTaskResponse {
	response := teo.NewCreatePurgeTaskResponse()
	response.Response = &teo.CreatePurgeTaskResponseParams{JobId: common.StringPtr(jobID)}
	return response
}

func testEdgeOnePurger(client edgeOnePurgeClient, spacing time.Duration) *EdgeOnePurger {
	return newEdgeOnePurger(config.Config{
		COSImageDomain: "https://img.test.fdueat.com",
		EdgeOneZoneID:  "zone-test123",
	}, client, spacing)
}

func TestEdgeOnePurgerSubmitsOneExactURL(t *testing.T) {
	client := &fakeEdgeOnePurgeClient{}
	purger := testEdgeOnePurger(client, 0)
	target := "https://img.test.fdueat.com/users/42/2026/08/image.png"

	require.NoError(t, purger.PurgeURL(context.Background(), target))
	require.Len(t, client.requests, 1)
	request := client.requests[0]
	require.Equal(t, "zone-test123", *request.ZoneId)
	require.Equal(t, "purge_url", *request.Type)
	require.Equal(t, []string{
		target,
		target + "?imageMogr2/thumbnail/1440x1440>/format/webp/quality/85",
		target + "?imageMogr2/thumbnail/720x720>/format/webp/quality/80",
	}, dereferenceStrings(request.Targets))
	require.NotNil(t, request.EncodeUrl)
	require.False(t, *request.EncodeUrl)
	require.Nil(t, request.Method)
}

func TestEdgeOnePurgerRejectsExpandedOrForeignTargets(t *testing.T) {
	client := &fakeEdgeOnePurgeClient{}
	purger := testEdgeOnePurger(client, 0)
	for _, target := range []string{
		"http://img.test.fdueat.com/a.png",
		"https://other.example/a.png",
		"https://img.test.fdueat.com/",
		"https://img.test.fdueat.com/a.png?imageMogr2/thumbnail/64x64",
		"https://img.test.fdueat.com/a.png#fragment",
		"https://user@img.test.fdueat.com/a.png",
	} {
		t.Run(target, func(t *testing.T) {
			err := purger.PurgeURL(context.Background(), target)
			require.ErrorIs(t, err, errEdgeOnePurgeInvalidTarget)
		})
	}
	require.Empty(t, client.requests)
}

func TestEdgeOnePurgerRetriesOnlyTransientFailure(t *testing.T) {
	t.Run("transient then success", func(t *testing.T) {
		client := &fakeEdgeOnePurgeClient{results: []edgeOneFakeResult{
			{err: tencenterrors.NewTencentCloudSDKError(
				"InternalError.BackendError", "provider detail", "request-sensitive",
			)},
			{response: successfulPurgeResponse("job-ok")},
		}}
		purger := testEdgeOnePurger(client, 0)
		purger.retryDelay = 0
		require.NoError(t, purger.PurgeURL(
			context.Background(), "https://img.test.fdueat.com/a.png",
		))
		require.Len(t, client.requests, 2)
	})

	t.Run("permanent failure is sanitized and not retried", func(t *testing.T) {
		client := &fakeEdgeOnePurgeClient{results: []edgeOneFakeResult{{
			err: tencenterrors.NewTencentCloudSDKError(
				"UnauthorizedOperation.CamUnauthorized",
				"secret provider detail https://img.test.fdueat.com/private.png",
				"request-sensitive",
			),
		}}}
		purger := testEdgeOnePurger(client, 0)
		err := purger.PurgeURL(
			context.Background(), "https://img.test.fdueat.com/private.png",
		)
		require.ErrorIs(t, err, errEdgeOnePurgeRequest)
		require.NotContains(t, err.Error(), "provider detail")
		require.NotContains(t, err.Error(), "private.png")
		require.NotContains(t, err.Error(), "request-sensitive")
		require.Len(t, client.requests, 1)
	})

	t.Run("ambiguous network failure is not replayed", func(t *testing.T) {
		client := &fakeEdgeOnePurgeClient{results: []edgeOneFakeResult{{err: io.EOF}}}
		purger := testEdgeOnePurger(client, 0)
		err := purger.PurgeURL(
			context.Background(), "https://img.test.fdueat.com/network.png",
		)
		require.ErrorIs(t, err, errEdgeOnePurgeRequest)
		require.Len(t, client.requests, 1)
	})
}

func TestEdgeOnePurgerRejectsIncompleteOrFailedResponse(t *testing.T) {
	failed := successfulPurgeResponse("job-failed")
	failed.Response.FailedList = []*teo.FailReason{{
		Reason:  common.StringPtr("sensitive failure"),
		Targets: common.StringPtrs([]string{"https://img.test.fdueat.com/private.png"}),
	}}
	for _, response := range []*teo.CreatePurgeTaskResponse{nil, teo.NewCreatePurgeTaskResponse(), failed} {
		client := &fakeEdgeOnePurgeClient{results: []edgeOneFakeResult{{response: response}}}
		purger := testEdgeOnePurger(client, 0)
		err := purger.PurgeURL(
			context.Background(), "https://img.test.fdueat.com/private.png",
		)
		require.ErrorIs(t, err, errEdgeOnePurgeResponse)
		require.NotContains(t, err.Error(), "private.png")
		require.NotContains(t, err.Error(), "sensitive failure")
	}
}

func TestEdgeOnePurgerAppliesOwnDeadlineWhileRateLimited(t *testing.T) {
	client := &fakeEdgeOnePurgeClient{}
	purger := testEdgeOnePurger(client, time.Second)
	require.NoError(t, purger.PurgeURL(
		context.Background(), "https://img.test.fdueat.com/first.png",
	))
	purger.overallTTL = 10 * time.Millisecond
	err := purger.PurgeURL(context.Background(), "https://img.test.fdueat.com/second.png")
	require.True(t, errors.Is(err, context.DeadlineExceeded), "实际错误: %v", err)
	require.Len(t, client.requests, 1)
}

func dereferenceStrings(values []*string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != nil {
			result = append(result, *value)
		}
	}
	return result
}
