package tencentcloud

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	tencenterrors "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/errors"
	teo "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/teo/v20220901"

	"github.com/Foodan-Dev/danshi-backend/internal/config"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

type edgeOneFakeResult struct {
	response *teo.CreatePurgeTaskResponse
	err      error
}

type edgeOneDescribeResult struct {
	response *teo.DescribePurgeTasksResponse
	err      error
}

type fakeEdgeOnePurgeClient struct {
	mu               sync.Mutex
	results          []edgeOneFakeResult
	requests         []*teo.CreatePurgeTaskRequest
	calledAt         []time.Time
	describeResults  []edgeOneDescribeResult
	describeRequests []*teo.DescribePurgeTasksRequest
}

func (c *fakeEdgeOnePurgeClient) DescribePurgeTasksWithContext(
	_ context.Context,
	request *teo.DescribePurgeTasksRequest,
) (*teo.DescribePurgeTasksResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.describeRequests = append(c.describeRequests, request)
	if len(c.describeResults) > 0 {
		result := c.describeResults[0]
		c.describeResults = c.describeResults[1:]
		return result.response, result.err
	}
	response := teo.NewDescribePurgeTasksResponse()
	response.Response = &teo.DescribePurgeTasksResponseParams{
		TotalCount: common.Uint64Ptr(0), Tasks: []*teo.Task{},
	}
	return response, nil
}

func purgeTasksResponse(tasks ...*teo.Task) *teo.DescribePurgeTasksResponse {
	response := teo.NewDescribePurgeTasksResponse()
	response.Response = &teo.DescribePurgeTasksResponseParams{
		TotalCount: common.Uint64Ptr(uint64(len(tasks))), Tasks: tasks,
	}
	return response
}

func purgeTask(jobID string, target string, status string) *teo.Task {
	return &teo.Task{
		JobId: common.StringPtr(jobID), Target: common.StringPtr(target),
		Type: common.StringPtr("purge_url"), Status: common.StringPtr(status),
	}
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

	submission, err := purger.Submit(context.Background(), target)
	require.NoError(t, err)
	require.Equal(t, "job-default", submission.JobID)
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
			_, err := purger.Submit(context.Background(), target)
			require.Error(t, err)
			require.Equal(t, service.ImageCachePurgeErrorPermanent,
				service.ClassifyImageCachePurgeError(err))
		})
	}
	require.Empty(t, client.requests)
}

func TestEdgeOnePurgerClassifiesCreateFailureWithoutReplay(t *testing.T) {
	t.Run("transient is delegated to durable worker", func(t *testing.T) {
		client := &fakeEdgeOnePurgeClient{results: []edgeOneFakeResult{
			{err: tencenterrors.NewTencentCloudSDKError(
				"InternalError.BackendError", "provider detail", "request-sensitive",
			)},
			{response: successfulPurgeResponse("job-ok")},
		}}
		purger := testEdgeOnePurger(client, 0)
		_, err := purger.Submit(
			context.Background(), "https://img.test.fdueat.com/a.png",
		)
		require.Error(t, err)
		require.Equal(t, service.ImageCachePurgeErrorRetryable,
			service.ClassifyImageCachePurgeError(err))
		require.Len(t, client.requests, 1)
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
		_, err := purger.Submit(
			context.Background(), "https://img.test.fdueat.com/private.png",
		)
		require.Equal(t, service.ImageCachePurgeErrorPermanent,
			service.ClassifyImageCachePurgeError(err))
		require.NotContains(t, err.Error(), "provider detail")
		require.NotContains(t, err.Error(), "private.png")
		require.NotContains(t, err.Error(), "request-sensitive")
		require.Len(t, client.requests, 1)
	})

	t.Run("ambiguous network failure is not replayed", func(t *testing.T) {
		client := &fakeEdgeOnePurgeClient{results: []edgeOneFakeResult{{err: io.EOF}}}
		purger := testEdgeOnePurger(client, 0)
		_, err := purger.Submit(
			context.Background(), "https://img.test.fdueat.com/network.png",
		)
		require.Equal(t, service.ImageCachePurgeErrorUnknown,
			service.ClassifyImageCachePurgeError(err))
		require.Len(t, client.requests, 1)
	})
}

func TestEdgeOnePurgerRejectsIncompleteOrFailedResponse(t *testing.T) {
	failed := successfulPurgeResponse("job-failed")
	failed.Response.FailedList = []*teo.FailReason{{
		Reason:  common.StringPtr("sensitive failure"),
		Targets: common.StringPtrs([]string{"https://img.test.fdueat.com/private.png"}),
	}}
	for _, response := range []*teo.CreatePurgeTaskResponse{nil, teo.NewCreatePurgeTaskResponse()} {
		client := &fakeEdgeOnePurgeClient{results: []edgeOneFakeResult{{response: response}}}
		purger := testEdgeOnePurger(client, 0)
		_, err := purger.Submit(
			context.Background(), "https://img.test.fdueat.com/private.png",
		)
		require.Equal(t, service.ImageCachePurgeErrorUnknown,
			service.ClassifyImageCachePurgeError(err))
		require.NotContains(t, err.Error(), "private.png")
	}
	client := &fakeEdgeOnePurgeClient{results: []edgeOneFakeResult{{response: failed}}}
	purger := testEdgeOnePurger(client, 0)
	submission, err := purger.Submit(
		context.Background(), "https://img.test.fdueat.com/private.png",
	)
	require.NoError(t, err)
	require.Equal(t, "job-failed", submission.JobID)
	require.True(t, submission.Partial, "即时 FailedList 不得导致已受理 JobId 丢失")
}

func TestEdgeOnePurgerAppliesOwnDeadlineWhileRateLimited(t *testing.T) {
	client := &fakeEdgeOnePurgeClient{}
	purger := testEdgeOnePurger(client, time.Second)
	_, err := purger.Submit(context.Background(), "https://img.test.fdueat.com/first.png")
	require.NoError(t, err)
	purger.overallTTL = 10 * time.Millisecond
	_, err = purger.Submit(context.Background(), "https://img.test.fdueat.com/second.png")
	require.Equal(t, service.ImageCachePurgeErrorUnknown,
		service.ClassifyImageCachePurgeError(err))
	require.Len(t, client.requests, 1)
}

func TestEdgeOnePurgerDescribeAggregatesEveryExactTarget(t *testing.T) {
	base := "https://img.test.fdueat.com/a.png"
	targets := service.ImageCacheURLs(base)
	client := &fakeEdgeOnePurgeClient{describeResults: []edgeOneDescribeResult{{
		response: purgeTasksResponse(
			purgeTask("job-1", targets[2], "success"),
			purgeTask("job-1", targets[0], "success"),
			purgeTask("job-1", targets[1], "success"),
		),
	}}}
	purger := testEdgeOnePurger(client, 0)
	state, err := purger.Describe(context.Background(), base, "job-1")
	require.NoError(t, err)
	require.Equal(t, service.ImageCachePurgeSuccess, state)
	require.Len(t, client.describeRequests, 1)
	request := client.describeRequests[0]
	require.Equal(t, "zone-test123", *request.ZoneId)
	require.Nil(t, request.StartTime)
	require.Equal(t, "job-id", *request.Filters[0].Name)
	require.Equal(t, []string{"job-1"}, dereferenceStrings(request.Filters[0].Values))
	require.False(t, *request.Filters[0].Fuzzy)

	client = &fakeEdgeOnePurgeClient{describeResults: []edgeOneDescribeResult{{
		response: purgeTasksResponse(
			purgeTask("job-2", targets[0], "success"),
			purgeTask("job-2", targets[1], "failed"),
			purgeTask("job-2", targets[2], "processing"),
		),
	}}}
	state, err = testEdgeOnePurger(client, 0).Describe(context.Background(), base, "job-2")
	require.NoError(t, err)
	require.Equal(t, service.ImageCachePurgeFailed, state)
}

func TestEdgeOnePurgerRecoverSearchesExactWindowAndEffect(t *testing.T) {
	base := "https://img.test.fdueat.com/recover.png"
	targets := service.ImageCacheURLs(base)
	started := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	windowTask := purgeTask("job-recovered", targets[0], "success")
	windowTask.CreateTime = common.StringPtr(started.Add(10 * time.Second).Format(time.RFC3339))
	client := &fakeEdgeOnePurgeClient{describeResults: []edgeOneDescribeResult{
		{response: purgeTasksResponse(windowTask)},
		{response: purgeTasksResponse(
			purgeTask("job-recovered", targets[0], "success"),
			purgeTask("job-recovered", targets[1], "success"),
			purgeTask("job-recovered", targets[2], "success"),
		)},
	}}
	purger := testEdgeOnePurger(client, 0)
	recovery, err := purger.Recover(context.Background(), base, started, started.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, recovery.EffectSucceeded)
	require.True(t, recovery.Found)
	require.Equal(t, "job-recovered", recovery.JobID)
	require.Equal(t, service.ImageCachePurgeSuccess, recovery.State)
	require.False(t, recovery.Ambiguous)
	require.Len(t, client.describeRequests, 2)
	require.Empty(t, client.requests)
	window := client.describeRequests[0]
	require.Equal(t, started.Format(time.RFC3339), *window.StartTime)
	require.Equal(t, started.Add(time.Minute).Format(time.RFC3339), *window.EndTime)
	require.Equal(t, "target", *window.Filters[0].Name)
	require.Equal(t, []string{targets[0]}, dereferenceStrings(window.Filters[0].Values))
	require.Equal(t, "job-id", *client.describeRequests[1].Filters[0].Name)
}

func TestEdgeOnePurgerRecoverConfirmsEffectWithoutGuessingAmongCandidates(t *testing.T) {
	base := "https://img.test.fdueat.com/ambiguous.png"
	targets := service.ImageCacheURLs(base)
	started := time.Date(2026, 8, 24, 11, 0, 0, 0, time.UTC)
	processingTask := purgeTask("job-a-processing", targets[0], "processing")
	processingTask.CreateTime = common.StringPtr(started.Add(10 * time.Second).Format(time.RFC3339))
	successTask := purgeTask("job-b-success", targets[0], "success")
	successTask.CreateTime = common.StringPtr(started.Add(20 * time.Second).Format(time.RFC3339))
	client := &fakeEdgeOnePurgeClient{describeResults: []edgeOneDescribeResult{
		{response: purgeTasksResponse(processingTask, successTask)},
		{response: purgeTasksResponse(
			purgeTask("job-a-processing", targets[0], "processing"),
			purgeTask("job-a-processing", targets[1], "success"),
			purgeTask("job-a-processing", targets[2], "success"),
		)},
		{response: purgeTasksResponse(
			purgeTask("job-b-success", targets[0], "success"),
			purgeTask("job-b-success", targets[1], "success"),
			purgeTask("job-b-success", targets[2], "success"),
		)},
	}}

	recovery, err := testEdgeOnePurger(client, 0).Recover(
		context.Background(), base, started, started.Add(time.Minute),
	)

	require.NoError(t, err)
	require.True(t, recovery.EffectSucceeded)
	require.True(t, recovery.Ambiguous)
	require.False(t, recovery.Found)
	require.Empty(t, recovery.JobID)
	require.Empty(t, recovery.State)
	require.Len(t, client.describeRequests, 3)
	require.Empty(t, client.requests)
}

func TestEdgeOnePurgerDescribeFollowsPagination(t *testing.T) {
	base := "https://img.test.fdueat.com/pages.png"
	targets := service.ImageCacheURLs(base)
	first := purgeTasksResponse(purgeTask("job-pages", targets[0], "success"))
	*first.Response.TotalCount = 3
	second := purgeTasksResponse(
		purgeTask("job-pages", targets[1], "success"),
		purgeTask("job-pages", targets[2], "success"),
	)
	*second.Response.TotalCount = 3
	client := &fakeEdgeOnePurgeClient{describeResults: []edgeOneDescribeResult{
		{response: first}, {response: second},
	}}
	state, err := testEdgeOnePurger(client, 0).Describe(context.Background(), base, "job-pages")
	require.NoError(t, err)
	require.Equal(t, service.ImageCachePurgeSuccess, state)
	require.Len(t, client.describeRequests, 2)
	require.EqualValues(t, 0, *client.describeRequests[0].Offset)
	require.EqualValues(t, 1, *client.describeRequests[1].Offset)
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
