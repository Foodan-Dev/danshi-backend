package localstorage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

func TestPrivateObjectRequiresUnexpiredSignedURL(t *testing.T) {
	storage := NewMemory()
	now := time.Unix(1_700_000_000, 0).UTC()
	storage.now = func() time.Time { return now }
	const objectKey = "posts/42/private.jpg"
	_, err := storage.PresignPut(context.Background(), service.StoragePresignRequest{
		ObjectKey: objectKey, ContentLength: 2048, TTL: 10 * time.Minute,
	})
	require.NoError(t, err)
	publicURL, err := storage.PublicURL(objectKey)
	require.NoError(t, err)
	require.NoError(t, storage.SetObjectPublicAccess(context.Background(), objectKey, false))
	_, err = storage.ReadURL(publicURL)
	require.ErrorIs(t, err, errAccessDenied)

	signedURL, err := storage.PresignGet(context.Background(), objectKey, time.Hour)
	require.NoError(t, err)
	meta, err := storage.ReadURL(signedURL)
	require.NoError(t, err)
	require.True(t, meta.Exists)
	require.EqualValues(t, 2048, meta.ContentLength)

	now = now.Add(time.Hour)
	_, err = storage.ReadURL(signedURL)
	require.ErrorIs(t, err, errSignedExpired)
}
