package authz_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/authz"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
)

func TestRoleCapabilityMatrixAndUnion(t *testing.T) {
	roles := []model.UserRole{
		model.UserRoleUser,
		model.UserRoleDictReviewer,
		model.UserRoleModerator,
		model.UserRoleSuperAdmin,
	}
	capabilities := []authz.Capability{
		authz.CapUseApp,
		authz.CapReviewDictionarySuggestion,
		authz.CapManageDictionary,
		authz.CapReviewContent,
		authz.CapManageContent,
		authz.CapViewUserEvidence,
		authz.CapBanUser,
		authz.CapListUsers,
		authz.CapManageUserRoles,
		authz.CapListAdmins,
	}
	want := map[model.UserRole]map[authz.Capability]bool{
		model.UserRoleUser: {
			authz.CapUseApp: true,
		},
		model.UserRoleDictReviewer: {
			authz.CapUseApp: true, authz.CapReviewDictionarySuggestion: true,
			authz.CapManageDictionary: true,
		},
		model.UserRoleModerator: {
			authz.CapUseApp: true, authz.CapReviewContent: true,
			authz.CapManageContent: true, authz.CapViewUserEvidence: true,
			authz.CapBanUser: true,
		},
		model.UserRoleSuperAdmin: {
			authz.CapUseApp: true, authz.CapReviewDictionarySuggestion: true,
			authz.CapManageDictionary: true, authz.CapReviewContent: true,
			authz.CapManageContent: true, authz.CapViewUserEvidence: true,
			authz.CapBanUser: true, authz.CapListUsers: true,
			authz.CapManageUserRoles: true, authz.CapListAdmins: true,
		},
	}
	for _, role := range roles {
		for _, capability := range capabilities {
			require.Equal(t, want[role][capability], authz.HasCapability(
				[]model.UserRole{role}, capability,
			), "role=%s capability=%s", role, capability)
		}
	}
	require.True(t, authz.HasCapability(
		[]model.UserRole{model.UserRoleModerator, model.UserRoleDictReviewer},
		authz.CapReviewContent,
	))
	require.True(t, authz.HasCapability(
		[]model.UserRole{model.UserRoleModerator, model.UserRoleDictReviewer},
		authz.CapManageDictionary,
	))
	require.False(t, authz.HasCapability(nil, authz.CapBanUser))
}
