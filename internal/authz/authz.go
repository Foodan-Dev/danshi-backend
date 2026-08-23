// Package authz 集中定义角色到能力的映射与能力并集计算。
package authz

import "github.com/jingyijun/danshi_backend_go/internal/model"

// Capability 是业务端点检查的权限能力，而不是用户角色。
type Capability string

// 平台能力枚举值。
const (
	CapUseApp                     Capability = "use_app"
	CapReviewDictionarySuggestion Capability = "review_dictionary_suggestion"
	CapManageDictionary           Capability = "manage_dictionary"
	CapReviewContent              Capability = "review_content"
	CapManageContent              Capability = "manage_content"
	CapViewUserEvidence           Capability = "view_user_evidence"
	CapBanUser                    Capability = "ban_user"
	CapListUsers                  Capability = "list_users"
	CapManageUserRoles            Capability = "manage_user_roles"
	CapListAdmins                 Capability = "list_admins"
)

var roleCapabilities = map[model.UserRole][]Capability{
	model.UserRoleUser: {
		CapUseApp,
	},
	model.UserRoleDictReviewer: {
		CapUseApp,
		CapReviewDictionarySuggestion,
		CapManageDictionary,
	},
	model.UserRoleModerator: {
		CapUseApp,
		CapReviewContent,
		CapManageContent,
		CapViewUserEvidence,
		CapBanUser,
	},
	model.UserRoleSuperAdmin: {
		CapUseApp,
		CapReviewDictionarySuggestion,
		CapManageDictionary,
		CapReviewContent,
		CapManageContent,
		CapViewUserEvidence,
		CapBanUser,
		CapListUsers,
		CapManageUserRoles,
		CapListAdmins,
	},
}

// HasCapability 按所有绑定角色的能力并集判断；无绑定身份按普通用户处理。
func HasCapability(roles []model.UserRole, capability Capability) bool {
	if len(roles) == 0 {
		return roleHasCapability(model.UserRoleUser, capability)
	}
	for _, role := range roles {
		if roleHasCapability(role, capability) {
			return true
		}
	}
	return false
}

// IsManagedRole 报告角色是否应写入 user_roles；普通用户由无绑定表达。
func IsManagedRole(role model.UserRole) bool {
	return role == model.UserRoleDictReviewer || role == model.UserRoleModerator ||
		role == model.UserRoleSuperAdmin
}

func roleHasCapability(role model.UserRole, capability Capability) bool {
	for _, candidate := range roleCapabilities[role] {
		if candidate == capability {
			return true
		}
	}
	return false
}
