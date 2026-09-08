package handler

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUsernameInputCompatibility(t *testing.T) {
	for _, input := range []string{`{"username":"Alice"}`, `{"name":"Alice"}`, `{"username":"Alice","name":"Alice"}`} {
		var registration registerRequest
		require.NoError(t, json.Unmarshal([]byte(input), &registration))
		require.Equal(t, "Alice", registration.Username)
		var update updateUserRequest
		require.NoError(t, json.Unmarshal([]byte(input), &update))
		require.True(t, update.UsernameSet)
		require.Equal(t, "Alice", *update.Username)
	}
	for _, input := range []string{`{"username":"Alice","name":"Bob"}`, `{"username":null,"name":"Alice"}`, `{"username":"Alice","name":null}`} {
		require.Error(t, json.Unmarshal([]byte(input), &registerRequest{}))
		require.Error(t, json.Unmarshal([]byte(input), &updateUserRequest{}))
	}
	var missing updateUserRequest
	require.NoError(t, json.Unmarshal([]byte(`{"bio":"hello"}`), &missing))
	require.False(t, missing.UsernameSet)
	var explicitNull updateUserRequest
	require.NoError(t, json.Unmarshal([]byte(`{"username":null}`), &explicitNull))
	require.True(t, explicitNull.UsernameSet)
	require.Nil(t, explicitNull.Username)
}
