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

func TestCallbackDecoderMapsReviewAndRejectsFailedJob(t *testing.T) {
	decoder := CallbackDecoder{}
	callback, err := decoder.DecodeImageCallback([]byte(`{
  "EventName":"ReviewImage",
  "JobsDetail":{
    "JobId":"job-review","State":"Success","Object":"posts/1/a.jpg",
    "DataId":"image_asset:1","Result":2,"Score":73,
    "Label":"Ads","AdsInfo":{"HitFlag":1,"Category":"QR code"}
  }
}`))
	require.NoError(t, err)
	require.Equal(t, uint64(1), callback.ImageAssetID)
	require.Equal(t, model.ModerationVerdictReview, callback.Verdict)
	require.Equal(t, []string{"ad", "ads", "qr code"}, callback.Labels)

	_, err = decoder.DecodeImageCallback([]byte(`{
  "EventName":"ReviewImage",
  "JobsDetail":{"JobId":"failed","State":"Failed","DataId":"image_asset:1"}
}`))
	require.Error(t, err)
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
		StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header),
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
