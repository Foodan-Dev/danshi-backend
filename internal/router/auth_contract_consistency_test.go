package router_test

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestAuthErrorContractConsistency(t *testing.T) {
	h := testutil.NewHarness(t)
	actor := h.Fixtures.CreateActor(h.Config)
	t.Run("reset_password_error_field_matches_input", func(t *testing.T) {
		status, response, _ := performJSON(t, h.Engine, http.MethodPost, "/api/v2/auth/password-resets", map[string]any{"email": actor.User.Email, "verification_code": "123456", "new_password": "short"}, "")
		require.Equal(t, http.StatusUnprocessableEntity, status, response.Message)
		var data struct {
			Errors []apierr.FieldError `json:"errors"`
		}
		decodeData(t, response, &data)
		require.Len(t, data.Errors, 1)
		require.Equal(t, "new_password", data.Errors[0].Field)
	})
	t.Run("spec_declares_new_failure_responses", func(t *testing.T) {
		raw, err := os.ReadFile("../../api/openapi.json")
		require.NoError(t, err)
		var spec struct {
			Paths map[string]map[string]struct {
				Responses map[string]json.RawMessage `json:"responses"`
			} `json:"paths"`
		}
		require.NoError(t, json.Unmarshal(raw, &spec))
		_, hasCooldown := spec.Paths["/api/v2/users/{user_id}"]["put"].Responses["429"]
		require.True(t, hasCooldown)
		_, hasUnavailable := spec.Paths["/api/v2/auth/register"]["post"].Responses["503"]
		require.True(t, hasUnavailable)
	})
}
