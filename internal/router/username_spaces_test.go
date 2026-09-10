package router_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestUsernameSpacesAcrossIdentityFlows(t *testing.T) {
	cfg := testutil.DefaultConfig()
	cfg.EmailVerificationRequired = false
	h := testutil.NewHarness(t, testutil.WithConfig(cfg))
	password := "password-123"
	register := func(email, name string) (int, testAPIResponse) {
		status, response, _ := performJSON(t, h.Engine, http.MethodPost, "/api/v2/auth/register", map[string]any{"email": email, "password": password, "username": name}, "")
		return status, response
	}
	status, response := register("spaces@fdueat.com", "  Ｕt   美食  ")
	require.Equal(t, http.StatusOK, status, response.Message)
	var auth service.AuthResult
	decodeData(t, response, &auth)
	require.Equal(t, "Ut 美食", auth.User.Username)
	status, response, _ = performJSON(t, h.Engine, http.MethodPost, "/api/v2/auth/login", map[string]any{"identifier": "  uT    美食 ", "password": password}, "")
	require.Equal(t, http.StatusOK, status, response.Message)
	status, response = register("spaces-conflict@fdueat.com", "ut  美食")
	require.Equal(t, http.StatusConflict, status, response.Message)
	require.Equal(t, apierr.BizUsernameTaken, response.ErrorCode)
	status, response, _ = performJSON(t, h.Engine, http.MethodPut, userPath(auth.User.ID), map[string]any{"username": "  Ut    美食 "}, auth.Token)
	require.Equal(t, http.StatusOK, status, response.Message)
	var count int64
	require.NoError(t, h.Database.GORM.Table("user_name_change_records").Where("user_id = ?", auth.User.ID).Count(&count).Error)
	require.Zero(t, count, "仅空格不同不应消耗改名额度")
	h.Moderation.SetDefaultContent(testutil.ContentVerdict(model.ModerationVerdictReview, nil, nil))
	status, response, _ = performJSON(t, h.Engine, http.MethodPut, userPath(auth.User.ID), map[string]any{"username": "  新的   名称 "}, auth.Token)
	require.Equal(t, http.StatusConflict, status, response.Message)
	var machine model.ModerationRecord
	require.NoError(t, h.Database.GORM.Where("user_id = ? AND verdict='review'", auth.User.ID).First(&machine).Error)
	require.Equal(t, "新的 名称", *machine.UsernameCandidate)
	admin := h.Fixtures.CreateActor(cfg, testutil.WithUserRole(model.UserRoleModerator))
	status, response, _ = performJSON(t, h.Engine, http.MethodPut, fmt.Sprintf("/api/v2/admin/moderation-records/%d/review", machine.ID), map[string]any{"verdict": "pass"}, admin.Token)
	require.Equal(t, http.StatusOK, status, response.Message)
	var user model.User
	require.NoError(t, h.Database.GORM.First(&user, auth.User.ID).Error)
	require.Equal(t, "新的 名称", user.Username)
	for i, name := range []string{"  ", " admin  ", "a\tb", "a\nb", "a-b"} {
		status, response = register(fmt.Sprintf("invalid-space-%d@fdueat.com", i), name)
		require.Equal(t, http.StatusUnprocessableEntity, status, response.Message)
	}
	// 直接写库不能绕过规范化或归属唯一性。
	for i, name := range []string{"Ut  美食", " 新的 名称", "新的 名称 ", "新的  名称"} {
		err := h.Database.GORM.Create(&model.User{Email: fmt.Sprintf("db-space-%d@fdueat.com", i), Username: name, PasswordHash: "x"}).Error
		require.Error(t, err)
	}
}
