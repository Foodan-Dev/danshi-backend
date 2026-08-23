package testutil_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jingyijun/danshi_backend_go/internal/service"
	"github.com/jingyijun/danshi_backend_go/internal/testutil"
)

func TestMockImageStorageControlsObjectMetadataMD5AndURL(t *testing.T) {
	storage := testutil.NewMockImageStorage()
	request := service.StoragePresignRequest{
		ObjectKey: "posts/42/image.jpg", ContentType: "image/jpeg",
		ContentLength: 2048, ContentMD5: "expected-md5", TTL: time.Minute,
	}
	ticket, err := storage.PresignPut(context.Background(), request)
	require.NoError(t, err)
	require.Contains(t, ticket.UploadURL, "posts%2F42%2Fimage.jpg")
	storage.RequirePresignCalls(t, 1)
	require.Equal(t, request, storage.PresignCalls()[0])

	meta, err := storage.HeadObject(context.Background(), request.ObjectKey)
	require.NoError(t, err)
	require.False(t, meta.Exists, "presign 本身不应凭空创建对象")

	storage.PutObject(request.ObjectKey, testutil.StoredObject{
		ContentLength: 1024, ContentMD5: request.ContentMD5,
	})
	meta, err = storage.HeadObject(context.Background(), request.ObjectKey)
	require.NoError(t, err)
	require.True(t, meta.Exists)
	require.EqualValues(t, 1024, meta.ContentLength, "可构造 complete 大小不符")

	storage.PutObject(request.ObjectKey, testutil.StoredObject{
		ContentLength: request.ContentLength, ContentMD5: "wrong-md5",
	})
	_, err = storage.HeadObject(context.Background(), request.ObjectKey)
	require.ErrorIs(t, err, testutil.ErrMockContentMD5Mismatch)

	storage.SetPublicURL(request.ObjectKey, "https://cdn.example.test/exact.jpg")
	publicURL, err := storage.PublicURL(request.ObjectKey)
	require.NoError(t, err)
	require.Equal(t, "https://cdn.example.test/exact.jpg", publicURL)
	require.Equal(t, []string{request.ObjectKey}, storage.PublicURLCalls())
	_, err = storage.ReadPublicURL(publicURL)
	require.NoError(t, err)
	require.NoError(t, storage.SetObjectPublicAccess(context.Background(), request.ObjectKey, false))
	_, err = storage.ReadPublicURL(publicURL)
	require.ErrorIs(t, err, testutil.ErrMockPublicAccessDenied)
	require.NoError(t, storage.SetObjectPublicAccess(context.Background(), request.ObjectKey, true))
	_, err = storage.ReadPublicURL(publicURL)
	require.NoError(t, err)
	require.Equal(t, []testutil.StorageAccessCall{
		{ObjectKey: request.ObjectKey, Public: false},
		{ObjectKey: request.ObjectKey, Public: true},
	}, storage.AccessCalls())
}

func TestMockImageStorageBlocksDeleteForExpiryCompleteRace(t *testing.T) {
	storage := testutil.NewMockImageStorage()
	storage.PutObject("race.jpg", testutil.StoredObject{ContentLength: 10})
	release := make(chan struct{})
	storage.QueueDelete(testutil.StorageDeleteBehavior{Release: release})
	done := make(chan error, 1)
	go func() { done <- storage.DeleteObject(context.Background(), "race.jpg") }()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.True(t, storage.WaitForDeleteCalls(ctx, 1))
	meta, err := storage.HeadObject(context.Background(), "race.jpg")
	require.NoError(t, err)
	require.True(t, meta.Exists, "阻塞中的清理尚未删除对象")
	select {
	case early := <-done:
		t.Fatalf("删除在释放前返回: %v", early)
	default:
	}
	close(release)
	require.NoError(t, <-done)
	meta, err = storage.HeadObject(context.Background(), "race.jpg")
	require.NoError(t, err)
	require.False(t, meta.Exists)
	storage.RequireDeleteCalls(t, 1)
}

func TestMockImageStorageQueuesProviderFailures(t *testing.T) {
	storage := testutil.NewMockImageStorage()
	headErr := errors.New("HEAD 5xx")
	storage.QueueHead(testutil.StorageHeadBehavior{Err: headErr})
	_, err := storage.HeadObject(context.Background(), "failure.jpg")
	require.ErrorIs(t, err, headErr)

	presignErr := errors.New("presign 5xx")
	storage.QueuePresign(testutil.StoragePresignBehavior{Err: presignErr})
	_, err = storage.PresignPut(context.Background(), service.StoragePresignRequest{
		ObjectKey: "failure.jpg", TTL: time.Minute,
	})
	require.ErrorIs(t, err, presignErr)

	accessErr := errors.New("ACL 5xx")
	storage.PutObject("failure.jpg", testutil.StoredObject{ContentLength: 10})
	storage.QueueAccess(testutil.StorageAccessBehavior{Err: accessErr})
	err = storage.SetObjectPublicAccess(context.Background(), "failure.jpg", false)
	require.ErrorIs(t, err, accessErr)
}
