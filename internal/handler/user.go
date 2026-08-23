package handler

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/httpx"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/envelope"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/pagination"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

// User 处理 User 域 HTTP 请求。
type User struct {
	service *service.UserService
}

// NewUser 创建用户 handler。
func NewUser(userService *service.UserService) *User { return &User{service: userService} }

type updateUserRequest struct {
	Name         *string `json:"name"`
	NameSet      bool    `json:"-"`
	Bio          *string `json:"bio"`
	BioSet       bool    `json:"-"`
	Gender       *string `json:"gender"`
	GenderSet    bool    `json:"-"`
	AvatarURL    *string `json:"avatar_url"`
	AvatarURLSet bool    `json:"-"`
}

// UnmarshalJSON 保留“字段缺席”与“显式 null”的差异，支持真正的局部更新。
func (r *updateUserRequest) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	decode := func(name string, target **string, present *bool) error {
		raw, exists := fields[name]
		if !exists {
			return nil
		}
		*present = true
		return json.Unmarshal(raw, target)
	}
	if err := decode("name", &r.Name, &r.NameSet); err != nil {
		return err
	}
	if err := decode("bio", &r.Bio, &r.BioSet); err != nil {
		return err
	}
	if err := decode("gender", &r.Gender, &r.GenderSet); err != nil {
		return err
	}
	return decode("avatar_url", &r.AvatarURL, &r.AvatarURLSet)
}

// Profile 返回用户主页。
func (h *User) Profile(ctx context.Context, c *app.RequestContext) {
	userID, principal, err := userRequestIdentity(c)
	var result *service.UserProfile
	if err == nil {
		result, err = h.service.Profile(ctx, userID, principal.User.ID)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("请求成功", result))
}

// Update 局部更新本人资料。
func (h *User) Update(ctx context.Context, c *app.RequestContext) {
	userID, principal, err := userRequestIdentity(c)
	var request updateUserRequest
	if err == nil {
		err = bindJSON(c, &request)
	}
	var result *service.UserUpdateResult
	if err == nil {
		result, err = h.service.Update(ctx, userID, principal.User.ID, service.UpdateUserInput{
			Name: request.Name, NameSet: request.NameSet, Bio: request.Bio, BioSet: request.BioSet,
			Gender: request.Gender, GenderSet: request.GenderSet,
			AvatarURL: request.AvatarURL, AvatarURLSet: request.AvatarURLSet,
		})
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("更新成功", result))
}

// Delete 软注销本人账号并撤销全部会话。
func (h *User) Delete(ctx context.Context, c *app.RequestContext) {
	userID, principal, err := userRequestIdentity(c)
	var result *service.UserDeleteResult
	if err == nil {
		result, err = h.service.Delete(ctx, userID, principal.User.ID)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("注销成功", result))
}

// Posts 返回用户帖子列表。
func (h *User) Posts(ctx context.Context, c *app.RequestContext) {
	query, err := bindQuery[userPostsQuery](c)
	var userID uint64
	var principal *service.Principal
	var params pagination.Params
	if err == nil {
		userID, principal, params, err = userListIdentity(c, query.Pagination)
	}
	var result *service.PostList
	if err == nil {
		result, err = h.service.Posts(ctx, userID, string(query.Status), params, principal.User.ID)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("请求成功", result))
}

// Favorites 返回本人收藏列表。
func (h *User) Favorites(ctx context.Context, c *app.RequestContext) {
	query, err := bindQuery[paginationQuery](c)
	var userID uint64
	var principal *service.Principal
	var params pagination.Params
	if err == nil {
		userID, principal, params, err = userListIdentity(c, query)
	}
	var result *service.PostList
	if err == nil {
		result, err = h.service.Favorites(ctx, userID, params, principal.User.ID)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("请求成功", result))
}

// Follow 幂等关注用户。
func (h *User) Follow(ctx context.Context, c *app.RequestContext) {
	h.followAction(ctx, c, true)
}

// Unfollow 幂等取消关注用户。
func (h *User) Unfollow(ctx context.Context, c *app.RequestContext) {
	h.followAction(ctx, c, false)
}

// Following 返回用户关注列表。
func (h *User) Following(ctx context.Context, c *app.RequestContext) {
	h.followList(ctx, c, true)
}

// Followers 返回用户粉丝列表。
func (h *User) Followers(ctx context.Context, c *app.RequestContext) {
	h.followList(ctx, c, false)
}

func (h *User) followAction(ctx context.Context, c *app.RequestContext, follow bool) {
	userID, principal, err := userRequestIdentity(c)
	var result *service.FollowActionResult
	if err == nil && follow {
		result, err = h.service.Follow(ctx, userID, principal.User.ID)
	} else if err == nil {
		result, err = h.service.Unfollow(ctx, userID, principal.User.ID)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	message := "关注成功"
	if !follow {
		message = "取消关注成功"
	}
	c.JSON(consts.StatusOK, envelope.OK(message, result))
}

func (h *User) followList(ctx context.Context, c *app.RequestContext, following bool) {
	query, err := bindQuery[paginationQuery](c)
	var userID uint64
	var principal *service.Principal
	var params pagination.Params
	if err == nil {
		userID, principal, params, err = userListIdentity(c, query)
	}
	var result *service.UserFollowList
	if err == nil && following {
		result, err = h.service.Following(ctx, userID, params, principal.User.ID)
	} else if err == nil {
		result, err = h.service.Followers(ctx, userID, params, principal.User.ID)
	}
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK("请求成功", result))
}

func userRequestIdentity(c *app.RequestContext) (uint64, *service.Principal, error) {
	userID, err := strconv.ParseUint(c.Param("user_id"), 10, 64)
	if err != nil || userID == 0 {
		return 0, nil, apierr.InvalidField(
			"user_id", apierr.FieldInvalidFormat, "user_id 必须是正整数",
		)
	}
	principal, err := httpx.CurrentPrincipal(c)
	return userID, principal, err
}

func userListIdentity(
	c *app.RequestContext,
	query paginationQuery,
) (uint64, *service.Principal, pagination.Params, error) {
	userID, principal, err := userRequestIdentity(c)
	if err != nil {
		return 0, nil, pagination.Params{}, err
	}
	params, err := query.params()
	return userID, principal, params, err
}
