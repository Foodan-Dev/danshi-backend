package secretbox

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSealOpenRoundTripAndAuthentication(t *testing.T) {
	box, err := New("secretbox-test-key-long-enough")
	require.NoError(t, err)

	first, err := box.Seal([]byte("654321"))
	require.NoError(t, err)
	second, err := box.Seal([]byte("654321"))
	require.NoError(t, err)
	require.NotEqual(t, first, second, "每次加密必须使用新的 nonce")

	plaintext, err := box.Open(first)
	require.NoError(t, err)
	require.Equal(t, "654321", string(plaintext))
	first[0] ^= 1
	_, err = box.Open(first)
	require.Error(t, err)

	other, err := New("another-secretbox-test-key")
	require.NoError(t, err)
	_, err = other.Open(second)
	require.Error(t, err)
}

func TestRejectsEmptyKeyAndMalformedPayload(t *testing.T) {
	_, err := New("")
	require.Error(t, err)
	box, err := New("secretbox-test-key-long-enough")
	require.NoError(t, err)
	_, err = box.Open([]byte("short"))
	require.Error(t, err)
}
