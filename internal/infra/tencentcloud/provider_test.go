package tencentcloud

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" // #nosec G505 -- 测试按 COS V5 公布协议独立复算 SHA-1 签名。
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/config"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

func TestPresignCOSPutMatchesIndependentSignature(t *testing.T) {
	bucketURL, err := url.Parse("https://bucket-1250000000.cos.ap-shanghai.myqcloud.com")
	require.NoError(t, err)
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	request := service.StoragePresignRequest{
		ObjectKey: "posts/42/2026/08/fixed.jpg", ContentType: "image/jpeg",
		ContentLength: 1234, ContentMD5: "1B2M2Y8AsgTpgAmY7PhCfg==",
		TTL: 10 * time.Minute,
	}
	const secretID = "AKIDEXAMPLE"
	const secretKey = "secret-key-for-independent-signature-test"

	rawURL, err := PresignCOSPut(
		context.Background(), bucketURL, secretID, secretKey, request, startedAt,
	)
	require.NoError(t, err)
	presigned, err := url.Parse(rawURL)
	require.NoError(t, err)
	query := presigned.Query()
	keyTime := "1700000000;1700000600"
	require.Equal(t, secretID, query.Get("q-ak"))
	require.Equal(t, "sha1", query.Get("q-sign-algorithm"))
	require.Equal(t, keyTime, query.Get("q-sign-time"))
	require.Equal(t, keyTime, query.Get("q-key-time"))
	require.Equal(t,
		"content-length;content-md5;content-type;host", query.Get("q-header-list"))
	require.Empty(t, query.Get("q-url-param-list"))

	canonicalHeaders := strings.Join([]string{
		"content-length=" + strconv.FormatInt(request.ContentLength, 10),
		"content-md5=" + cosSignatureEscape(request.ContentMD5),
		"content-type=" + cosSignatureEscape(request.ContentType),
		"host=" + cosSignatureEscape(bucketURL.Host),
	}, "&")
	formatString := "put\n/" + request.ObjectKey + "\n\n" + canonicalHeaders + "\n"
	formatDigest := sha1.Sum([]byte(formatString)) // #nosec G401 -- COS V5 协议固定使用 SHA-1。
	stringToSign := "sha1\n" + keyTime + "\n" + hex.EncodeToString(formatDigest[:]) + "\n"
	signKey := hex.EncodeToString(testHMACSHA1([]byte(secretKey), keyTime))
	expectedSignature := hex.EncodeToString(testHMACSHA1([]byte(signKey), stringToSign))
	require.Equal(t, expectedSignature, query.Get("q-signature"))
}

func TestPresignCOSGetMatchesIndependentSignature(t *testing.T) {
	bucketURL, err := url.Parse("https://bucket-1250000000.cos.ap-shanghai.myqcloud.com")
	require.NoError(t, err)
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	const objectKey = "posts/42/2026/08/private.jpg"
	const secretID = "AKIDEXAMPLE"
	const secretKey = "secret-key-for-independent-signature-test"

	rawURL, err := PresignCOSGet(
		context.Background(), bucketURL, secretID, secretKey, objectKey, time.Hour, startedAt,
	)
	require.NoError(t, err)
	presigned, err := url.Parse(rawURL)
	require.NoError(t, err)
	query := presigned.Query()
	keyTime := "1700000000;1700003600"
	require.Equal(t, secretID, query.Get("q-ak"))
	require.Equal(t, keyTime, query.Get("q-sign-time"))
	require.Equal(t, keyTime, query.Get("q-key-time"))
	require.Equal(t, "host", query.Get("q-header-list"))
	require.Empty(t, query.Get("q-url-param-list"))

	canonicalHeaders := "host=" + cosSignatureEscape(bucketURL.Host)
	formatString := "get\n/" + objectKey + "\n\n" + canonicalHeaders + "\n"
	formatDigest := sha1.Sum([]byte(formatString)) // #nosec G401 -- COS V5 协议固定使用 SHA-1。
	stringToSign := "sha1\n" + keyTime + "\n" + hex.EncodeToString(formatDigest[:]) + "\n"
	signKey := hex.EncodeToString(testHMACSHA1([]byte(secretKey), keyTime))
	expectedSignature := hex.EncodeToString(testHMACSHA1([]byte(signKey), stringToSign))
	require.Equal(t, expectedSignature, query.Get("q-signature"))
}

func TestProviderReviewAndSubmitImageWithoutNetwork(t *testing.T) {
	transport := &captureTencentTransport{}
	provider, err := NewProvider(providerTestConfig(), &http.Client{Transport: transport})
	require.NoError(t, err)

	result, err := provider.Review(context.Background(), service.ModerationRequest{
		Target: service.ModerationTargetComment, Text: "需要审核的文本",
	})
	require.NoError(t, err)
	require.Equal(t, model.ModerationVerdictReview, result.Verdict)
	require.Equal(t, model.ModerationProviderTencentCI, result.Provider)
	require.NotNil(t, result.ProviderJobID)
	require.Equal(t, "text-job-1", *result.ProviderJobID)
	require.Equal(t, []string{"abuse"}, result.Labels)
	require.NotNil(t, result.Score)
	require.Equal(t, "87", result.Score.String())
	require.Contains(t, string(result.RawResponse), "text-job-1")

	submission, err := provider.SubmitImage(context.Background(), service.ImageModerationRequest{
		ImageAssetID: 88, ObjectKey: "posts/88/test.jpg",
	})
	require.NoError(t, err)
	require.NotNil(t, submission.ProviderJobID)
	require.Equal(t, "image-job-1", *submission.ProviderJobID)
	require.NoError(t, provider.SetObjectPublicAccess(
		context.Background(), "posts/88/test.jpg", false,
	))
	require.NoError(t, provider.SetObjectPublicAccess(
		context.Background(), "posts/88/test.jpg", true,
	))

	requests := transport.all()
	require.Len(t, requests, 4)
	require.Equal(t, http.MethodPost, requests[0].method)
	require.Equal(t, "/text/auditing", requests[0].path)
	require.Contains(t, requests[0].body,
		base64.StdEncoding.EncodeToString([]byte("需要审核的文本")))
	require.NotContains(t, requests[0].body, "需要审核的文本")
	require.Contains(t, requests[0].body, "test-biz-type")

	require.Equal(t, http.MethodGet, requests[1].method)
	require.Equal(t, "image_asset:88", requests[1].query.Get("dataid"))
	require.Equal(t, "1", requests[1].query.Get("async"))
	callback, err := url.Parse(requests[1].query.Get("callback"))
	require.NoError(t, err)
	require.Equal(t, "callback-token", callback.Query().Get("token"))
	require.Equal(t, http.MethodPut, requests[2].method)
	require.Equal(t, "/posts/88/test.jpg", requests[2].path)
	require.Contains(t, requests[2].query, "acl")
	require.Equal(t, "private", requests[2].header.Get("x-cos-acl"))
	require.Equal(t, http.MethodPut, requests[3].method)
	require.Equal(t, "public-read", requests[3].header.Get("x-cos-acl"))
}

func TestProviderExternalCallsCreateLowCardinalityClientSpans(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	ctx, parent := tracerProvider.Tracer("test-parent").Start(context.Background(), "request")
	transport := &captureTencentTransport{}
	provider, err := NewProvider(providerTestConfig(), &http.Client{Transport: transport})
	require.NoError(t, err)

	_, err = provider.Review(ctx, service.ModerationRequest{
		Target: service.ModerationTargetComment, Text: "PRIVATE-TEXT-MUST-NOT-BE-TRACED",
	})
	require.NoError(t, err)
	_, err = provider.SubmitImage(ctx, service.ImageModerationRequest{
		ImageAssetID: 88, ObjectKey: "PRIVATE-OBJECT-KEY-MUST-NOT-BE-TRACED.jpg",
	})
	require.NoError(t, err)
	require.NoError(t, provider.SetObjectPublicAccess(
		ctx, "PRIVATE-OBJECT-KEY-MUST-NOT-BE-TRACED.jpg", true,
	))
	meta, err := provider.HeadObject(ctx, "PRIVATE-OBJECT-KEY-MUST-NOT-BE-TRACED.jpg")
	require.NoError(t, err)
	require.True(t, meta.Exists)
	require.EqualValues(t, 1234, meta.ContentLength)
	require.NoError(t, provider.DeleteObject(ctx, "PRIVATE-OBJECT-KEY-MUST-NOT-BE-TRACED.jpg"))
	parent.End()

	spans := recorder.Ended()
	require.Len(t, spans, 6)
	require.Equal(t, []string{
		"EXTERNAL tencent_ci ReviewText",
		"EXTERNAL tencent_ci SubmitImage",
		"EXTERNAL tencent_cos PutObjectACL",
		"EXTERNAL tencent_cos HeadObject",
		"EXTERNAL tencent_cos DeleteObject",
		"request",
	}, []string{
		spans[0].Name(), spans[1].Name(), spans[2].Name(),
		spans[3].Name(), spans[4].Name(), spans[5].Name(),
	})
	for _, span := range spans[:5] {
		require.Equal(t, spans[5].SpanContext().SpanID(), span.Parent().SpanID())
		require.NotContains(t, span.Name(), "PRIVATE")
		for _, item := range span.Attributes() {
			require.NotContains(t, item.Value.String(), "PRIVATE")
		}
	}
}

func TestCallbackDecoderMapsOfficialDetailCallbacks(t *testing.T) {
	decoder := CallbackDecoder{}
	t.Run("success", func(t *testing.T) {
		body := []byte(`{
  "EventName":"ReviewImage",
  "JobsDetail":{
    "JobId":"job-review","State":"Success","CreationTime":"2021-08-10T21:01:10+08:00",
    "Object":"posts/1/a.jpg","DataId":"image_asset:1","Label":"Ads","Result":2,
    "Score":73,"Category":"","SubLabel":"",
    "PornInfo":{"HitFlag":0,"Score":0,"Label":"","Category":"","SubLabel":""},
    "AdsInfo":{"HitFlag":1,"Score":73,"Label":"","Category":"QR code","SubLabel":""},
    "BucketId":"bucket-1250000000","Region":"ap-shanghai","ForbidState":0
  }
}`)
		callback, err := decoder.DecodeImageCallback(body)
		require.NoError(t, err)
		require.Equal(t, uint64(1), callback.ImageAssetID)
		require.Equal(t, "job-review", callback.ProviderJobID)
		require.Equal(t, model.ModerationVerdictReview, callback.Verdict)
		require.Equal(t, []string{"ad", "ads", "qr code"}, callback.Labels)
		require.NotNil(t, callback.Score)
		require.Equal(t, "73", callback.Score.String())
		require.JSONEq(t, string(body), string(callback.RawResponse))
	})

	t.Run("failed becomes terminal review", func(t *testing.T) {
		body := []byte(`{
  "EventName":"ReviewImage",
  "JobsDetail":{
    "Code":"InvalidImage","Message":"image width and height are too small",
    "JobId":"job-failed","State":"Failed","Object":"posts/1/tiny.png",
    "DataId":"image_asset:2"
  }
}`)
		callback, err := decoder.DecodeImageCallback(body)
		require.NoError(t, err)
		require.Equal(t, uint64(2), callback.ImageAssetID)
		require.Equal(t, "job-failed", callback.ProviderJobID)
		require.Equal(t, "posts/1/tiny.png", callback.ObjectKey)
		require.Equal(t, model.ModerationVerdictReview, callback.Verdict)
		require.Equal(t, []string{"provider_failed"}, callback.Labels)
		require.Nil(t, callback.Score)
		require.JSONEq(t, string(body), string(callback.RawResponse))
	})

	t.Run("non terminal state is rejected", func(t *testing.T) {
		_, err := decoder.DecodeImageCallback([]byte(`{
  "EventName":"ReviewImage",
  "JobsDetail":{"JobId":"auditing","State":"Auditing","DataId":"image_asset:1"}
}`))
		require.ErrorContains(t, err, "状态无效")
	})

	t.Run("success without result is rejected", func(t *testing.T) {
		_, err := decoder.DecodeImageCallback([]byte(`{
  "EventName":"ReviewImage",
  "JobsDetail":{"JobId":"missing-result","State":"Success","DataId":"image_asset:1"}
}`))
		require.ErrorContains(t, err, "缺少 Result")
	})
}

func TestCallbackDecoderMapsOfficialSimpleCallbacks(t *testing.T) {
	decoder := CallbackDecoder{}
	t.Run("success", func(t *testing.T) {
		callback, err := decoder.DecodeImageCallback([]byte(`{
  "code":0,
  "data":{
    "event":"ReviewImage","result":1,"trace_id":"simple-block",
    "data_id":"image_asset:3","url":"https://example.test/a.jpg",
    "porn_info":{"hit_flag":1,"label":"Porn","score":99}
  },
  "message":"success"
}`))
		require.NoError(t, err)
		require.Equal(t, uint64(3), callback.ImageAssetID)
		require.Equal(t, "simple-block", callback.ProviderJobID)
		require.Equal(t, model.ModerationVerdictBlock, callback.Verdict)
		require.Equal(t, []string{"porn"}, callback.Labels)
		require.Nil(t, callback.Score, "Simple 回调没有综合置信度字段")
	})

	t.Run("provider failure becomes terminal review", func(t *testing.T) {
		callback, err := decoder.DecodeImageCallback([]byte(`{
  "code":-1,
  "data":{
    "event":"ReviewImage","trace_id":"simple-failed","data_id":"image_asset:4"
  },
  "message":"failed"
}`))
		require.NoError(t, err)
		require.Equal(t, uint64(4), callback.ImageAssetID)
		require.Equal(t, model.ModerationVerdictReview, callback.Verdict)
		require.Equal(t, []string{"provider_failed"}, callback.Labels)
	})
}

func TestProviderFailureIsFailClosed(t *testing.T) {
	provider, err := NewProvider(providerTestConfig(), &http.Client{
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("provider unavailable")
		}),
	})
	require.NoError(t, err)
	_, err = provider.Review(context.Background(), service.ModerationRequest{Text: "text"})
	require.Equal(t, http.StatusServiceUnavailable, apierr.As(err).Status)
}

func TestProviderImageModerationProbeClassifiesAuthorizationAndNetworkFailures(t *testing.T) {
	t.Run("success is a side effect free GET", func(t *testing.T) {
		var request *http.Request
		provider, err := NewProvider(providerTestConfig(), &http.Client{
			Transport: roundTripperFunc(func(got *http.Request) (*http.Response, error) {
				request = got
				return &http.Response{
					StatusCode: http.StatusOK, Status: "200 OK", Request: got,
					Header: http.Header{}, Body: io.NopCloser(strings.NewReader("<CIStatus>on</CIStatus>")),
				}, nil
			}),
		})
		require.NoError(t, err)
		require.NoError(t, provider.ProbeImageModeration(context.Background()))
		require.Equal(t, http.MethodGet, request.Method)
		require.Equal(t, "/", request.URL.Path)
		require.Empty(t, request.URL.RawQuery)
	})

	t.Run("HTTP 403 AccessDenied is authorization", func(t *testing.T) {
		provider, err := NewProvider(providerTestConfig(), &http.Client{
			Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusForbidden, Status: "403 Forbidden", Request: request,
					Header: http.Header{}, Body: io.NopCloser(strings.NewReader(
						"<Error><Code>AccessDenied</Code><Message>denied</Message></Error>",
					)),
				}, nil
			}),
		})
		require.NoError(t, err)
		err = provider.ProbeImageModeration(context.Background())
		require.Error(t, err)
		require.Equal(t, service.ImageModerationProbeAuthorization,
			service.ClassifyImageModerationProbeError(err))
	})

	t.Run("network error is transient", func(t *testing.T) {
		provider, err := NewProvider(providerTestConfig(), &http.Client{
			Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("network timeout")
			}),
		})
		require.NoError(t, err)
		err = provider.ProbeImageModeration(context.Background())
		require.Error(t, err)
		require.Equal(t, service.ImageModerationProbeTransient,
			service.ClassifyImageModerationProbeError(err))
	})

	t.Run("HTTP 5xx is transient", func(t *testing.T) {
		provider, err := NewProvider(providerTestConfig(), &http.Client{
			Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusServiceUnavailable, Status: "503 Service Unavailable",
					Request: request, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(
						"<Error><Code>ServiceUnavailable</Code></Error>",
					)),
				}, nil
			}),
		})
		require.NoError(t, err)
		err = provider.ProbeImageModeration(context.Background())
		require.Error(t, err)
		require.Equal(t, service.ImageModerationProbeTransient,
			service.ClassifyImageModerationProbeError(err))
	})
}

func providerTestConfig() config.Config {
	return config.Config{
		TencentSecretID: "test-secret-id", TencentSecretKey: "test-secret-key",
		COSBucket: "bucket-1250000000", COSRegion: "ap-shanghai",
		COSImageDomain: "https://img.example.test", TencentCIBizType: "test-biz-type",
		TencentCICallbackURL:    "https://api.example.test/api/v2/moderation/tencent-ci/callback",
		ModerationCallbackToken: "callback-token",
	}
}

func cosSignatureEscape(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}

func testHMACSHA1(key []byte, value string) []byte {
	digest := hmac.New(sha1.New, key) // #nosec G401 -- COS V5 协议固定使用 SHA-1。
	_, _ = digest.Write([]byte(value))
	return digest.Sum(nil)
}

type capturedTencentRequest struct {
	method string
	path   string
	query  url.Values
	header http.Header
	body   string
}

type captureTencentTransport struct {
	mu       sync.Mutex
	requests []capturedTencentRequest
}

func (t *captureTencentTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var body []byte
	if request.Body != nil {
		var err error
		body, err = io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
	}
	t.mu.Lock()
	t.requests = append(t.requests, capturedTencentRequest{
		method: request.Method, path: request.URL.Path,
		query: request.URL.Query(), header: request.Header.Clone(), body: string(body),
	})
	t.mu.Unlock()
	responseBody := `<RecognitionResult><JobId>image-job-1</JobId><State>Submitted</State></RecognitionResult>`
	if request.URL.Path == "/text/auditing" {
		responseBody = `<Response><JobsDetail><JobId>text-job-1</JobId><State>Success</State>` +
			`<Label>Abuse</Label><Result>2</Result><Score>87</Score>` +
			`<AbuseInfo><HitFlag>1</HitFlag><Score>87</Score></AbuseInfo>` +
			`</JobsDetail><RequestId>request-1</RequestId></Response>`
	}
	return &http.Response{
		StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{"Content-Length": {"1234"}},
		Body: io.NopCloser(strings.NewReader(responseBody)), Request: request,
	}, nil
}

func (t *captureTencentTransport) all() []capturedTencentRequest {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]capturedTencentRequest(nil), t.requests...)
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
