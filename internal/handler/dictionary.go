package handler

import (
	"context"
	"encoding/json"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/jingyijun/danshi_backend_go/internal/pkg/envelope"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/pagination"
	"github.com/jingyijun/danshi_backend_go/internal/router/middleware"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

// Dictionary 处理用户提议、人工审批与词表维护。
type Dictionary struct {
	service *service.DictionaryService
}

// NewDictionary 创建词表 handler。
func NewDictionary(dictionaryService *service.DictionaryService) *Dictionary {
	return &Dictionary{service: dictionaryService}
}

type createSuggestionRequest struct {
	Kind               string  `json:"kind"`
	ProposedName       string  `json:"proposed_name"`
	PostID             *uint64 `json:"post_id"`
	FlavorStance       *string `json:"flavor_stance"`
	ParentCanteenID    *uint64 `json:"parent_canteen_id"`
	ParentSuggestionID *uint64 `json:"parent_suggestion_id"`
}

type approveSuggestionRequest struct {
	ExistingItemID *uint64 `json:"existing_item_id"`
	Code           *string `json:"code"`
	Campus         *string `json:"campus"`
	Floor          *string `json:"floor"`
	SortOrder      int32   `json:"sort_order"`
	ReviewNote     *string `json:"review_note"`
}

type rejectSuggestionRequest struct {
	ReviewNote string `json:"review_note"`
}

type createDictionaryItemRequest struct {
	Name      string `json:"name"`
	SortOrder int32  `json:"sort_order"`
	IsActive  *bool  `json:"is_active"`
}

type updateDictionaryItemRequest struct {
	Name      *string `json:"name"`
	SortOrder *int32  `json:"sort_order"`
	IsActive  *bool   `json:"is_active"`
}

type createCanteenRequest struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	Campus    string `json:"campus"`
	SortOrder int32  `json:"sort_order"`
	IsActive  *bool  `json:"is_active"`
}

type updateCanteenRequest struct {
	CodeSet   bool    `json:"-"`
	Name      *string `json:"name"`
	Campus    *string `json:"campus"`
	SortOrder *int32  `json:"sort_order"`
	IsActive  *bool   `json:"is_active"`
}

func (r *updateCanteenRequest) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	r.CodeSet = fields["code"] != nil
	return decodeDictionaryPatchFields(fields, &r.Name, &r.Campus, nil, &r.SortOrder, &r.IsActive)
}

type createWindowRequest struct {
	Name      string  `json:"name"`
	Floor     *string `json:"floor"`
	SortOrder int32   `json:"sort_order"`
	IsActive  *bool   `json:"is_active"`
}

type updateWindowRequest struct {
	CanteenIDSet bool    `json:"-"`
	Name         *string `json:"name"`
	Floor        *string `json:"floor"`
	FloorSet     bool    `json:"-"`
	SortOrder    *int32  `json:"sort_order"`
	IsActive     *bool   `json:"is_active"`
}

func (r *updateWindowRequest) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	r.CanteenIDSet = fields["canteen_id"] != nil
	if raw, exists := fields["floor"]; exists {
		r.FloorSet = true
		if err := json.Unmarshal(raw, &r.Floor); err != nil {
			return err
		}
	}
	return decodeDictionaryPatchFields(fields, &r.Name, nil, &r.Floor, &r.SortOrder, &r.IsActive)
}

// CreateSuggestion 提交封闭词表建议。
func (h *Dictionary) CreateSuggestion(ctx context.Context, c *app.RequestContext) {
	principal, err := middleware.CurrentPrincipal(c)
	var request createSuggestionRequest
	if err == nil {
		err = bindJSON(c, &request)
	}
	var result *service.SuggestionView
	if err == nil {
		result, err = h.service.CreateSuggestion(ctx, service.CreateSuggestionInput{
			Kind: request.Kind, ProposedName: request.ProposedName, PostID: request.PostID,
			FlavorStance: request.FlavorStance, ParentCanteenID: request.ParentCanteenID,
			ParentSuggestionID: request.ParentSuggestionID,
		}, principal.User.ID)
	}
	respondDictionary(ctx, c, result, err, "建议已提交")
}

// Mine 返回当前用户的提议历史。
func (h *Dictionary) Mine(ctx context.Context, c *app.RequestContext) {
	principal, err := middleware.CurrentPrincipal(c)
	params, paramsErr := pagination.Parse(c.Query("page"), c.Query("limit"))
	if err == nil {
		err = paramsErr
	}
	var result *service.SuggestionList
	if err == nil {
		result, err = h.service.Mine(ctx, principal.User.ID, params)
	}
	respondDictionary(ctx, c, result, err, "请求成功")
}

// Pending 返回管理员待审提议。
func (h *Dictionary) Pending(ctx context.Context, c *app.RequestContext) {
	params, err := pagination.Parse(c.Query("page"), c.Query("limit"))
	var result *service.SuggestionList
	if err == nil {
		result, err = h.service.Pending(ctx, c.Query("kind"), params)
	}
	respondDictionary(ctx, c, result, err, "请求成功")
}

// Approve 单事务批准提议。
func (h *Dictionary) Approve(ctx context.Context, c *app.RequestContext) {
	suggestionID, err := positivePathID(c.Param("suggestion_id"), "suggestion_id")
	principal, principalErr := middleware.CurrentPrincipal(c)
	if err == nil {
		err = principalErr
	}
	var request approveSuggestionRequest
	if err == nil {
		err = bindJSON(c, &request)
	}
	var result *service.SuggestionView
	if err == nil {
		result, err = h.service.Approve(ctx, suggestionID, principal.User.ID, service.ApproveSuggestionInput{
			ExistingItemID: request.ExistingItemID, Code: request.Code, Campus: request.Campus,
			Floor: request.Floor, SortOrder: request.SortOrder, ReviewNote: request.ReviewNote,
		})
	}
	respondDictionary(ctx, c, result, err, "建议已批准")
}

// Reject 驳回提议并保存不可变理由。
func (h *Dictionary) Reject(ctx context.Context, c *app.RequestContext) {
	suggestionID, err := positivePathID(c.Param("suggestion_id"), "suggestion_id")
	principal, principalErr := middleware.CurrentPrincipal(c)
	if err == nil {
		err = principalErr
	}
	var request rejectSuggestionRequest
	if err == nil {
		err = bindJSON(c, &request)
	}
	var result *service.SuggestionView
	if err == nil {
		result, err = h.service.Reject(ctx, suggestionID, principal.User.ID, request.ReviewNote)
	}
	respondDictionary(ctx, c, result, err, "建议已驳回")
}

// CreateFlavor 新建口味。
func (h *Dictionary) CreateFlavor(ctx context.Context, c *app.RequestContext) {
	var request createDictionaryItemRequest
	err := bindJSON(c, &request)
	var result *service.DictionaryItemView
	if err == nil {
		result, err = h.service.CreateFlavor(ctx, service.CreateDictionaryItemInput{
			Name: request.Name, SortOrder: request.SortOrder, IsActive: request.IsActive,
		})
	}
	respondDictionary(ctx, c, result, err, "口味已创建")
}

// UpdateFlavor 更新口味。
func (h *Dictionary) UpdateFlavor(ctx context.Context, c *app.RequestContext) {
	itemID, err := positivePathID(c.Param("flavor_id"), "flavor_id")
	var request updateDictionaryItemRequest
	if err == nil {
		err = bindJSON(c, &request)
	}
	var result *service.DictionaryItemView
	if err == nil {
		result, err = h.service.UpdateFlavor(ctx, itemID, service.UpdateDictionaryItemInput(request))
	}
	respondDictionary(ctx, c, result, err, "口味已更新")
}

// DeleteFlavor 物理删除未被使用的口味。
func (h *Dictionary) DeleteFlavor(ctx context.Context, c *app.RequestContext) {
	itemID, err := positivePathID(c.Param("flavor_id"), "flavor_id")
	var result *service.DictionaryDeleteResult
	if err == nil {
		result, err = h.service.DeleteFlavor(ctx, itemID)
	}
	respondDictionary(ctx, c, result, err, "口味已删除")
}

// CreateCuisine 新建菜系。
func (h *Dictionary) CreateCuisine(ctx context.Context, c *app.RequestContext) {
	var request createDictionaryItemRequest
	err := bindJSON(c, &request)
	var result *service.DictionaryItemView
	if err == nil {
		result, err = h.service.CreateCuisine(ctx, service.CreateDictionaryItemInput{
			Name: request.Name, SortOrder: request.SortOrder, IsActive: request.IsActive,
		})
	}
	respondDictionary(ctx, c, result, err, "菜系已创建")
}

// UpdateCuisine 更新菜系。
func (h *Dictionary) UpdateCuisine(ctx context.Context, c *app.RequestContext) {
	itemID, err := positivePathID(c.Param("cuisine_id"), "cuisine_id")
	var request updateDictionaryItemRequest
	if err == nil {
		err = bindJSON(c, &request)
	}
	var result *service.DictionaryItemView
	if err == nil {
		result, err = h.service.UpdateCuisine(ctx, itemID, service.UpdateDictionaryItemInput(request))
	}
	respondDictionary(ctx, c, result, err, "菜系已更新")
}

// DeleteCuisine 删除未被使用的菜系。
func (h *Dictionary) DeleteCuisine(ctx context.Context, c *app.RequestContext) {
	itemID, err := positivePathID(c.Param("cuisine_id"), "cuisine_id")
	var result *service.DictionaryDeleteResult
	if err == nil {
		result, err = h.service.DeleteCuisine(ctx, itemID)
	}
	respondDictionary(ctx, c, result, err, "菜系已删除")
}

// CreateCanteen 新建餐厅。
func (h *Dictionary) CreateCanteen(ctx context.Context, c *app.RequestContext) {
	var request createCanteenRequest
	err := bindJSON(c, &request)
	var result *service.DictionaryCanteenView
	if err == nil {
		result, err = h.service.CreateCanteen(ctx, service.CreateCanteenInput{
			Code: request.Code, Name: request.Name, Campus: request.Campus,
			SortOrder: request.SortOrder, IsActive: request.IsActive,
		})
	}
	respondDictionary(ctx, c, result, err, "餐厅已创建")
}

// UpdateCanteen 更新餐厅但禁止修改稳定 code。
func (h *Dictionary) UpdateCanteen(ctx context.Context, c *app.RequestContext) {
	itemID, err := positivePathID(c.Param("canteen_id"), "canteen_id")
	var request updateCanteenRequest
	if err == nil {
		err = bindJSON(c, &request)
	}
	var result *service.DictionaryCanteenView
	if err == nil {
		result, err = h.service.UpdateCanteen(ctx, itemID, service.UpdateCanteenInput{
			CodeSet: request.CodeSet, Name: request.Name, Campus: request.Campus,
			SortOrder: request.SortOrder, IsActive: request.IsActive,
		})
	}
	respondDictionary(ctx, c, result, err, "餐厅已更新")
}

// DeleteCanteen 删除未被使用的餐厅。
func (h *Dictionary) DeleteCanteen(ctx context.Context, c *app.RequestContext) {
	itemID, err := positivePathID(c.Param("canteen_id"), "canteen_id")
	var result *service.DictionaryDeleteResult
	if err == nil {
		result, err = h.service.DeleteCanteen(ctx, itemID)
	}
	respondDictionary(ctx, c, result, err, "餐厅已删除")
}

// CreateWindow 在餐厅下新建窗口。
func (h *Dictionary) CreateWindow(ctx context.Context, c *app.RequestContext) {
	canteenID, err := positivePathID(c.Param("canteen_id"), "canteen_id")
	var request createWindowRequest
	if err == nil {
		err = bindJSON(c, &request)
	}
	var result *service.DictionaryWindowView
	if err == nil {
		result, err = h.service.CreateWindow(ctx, canteenID, service.CreateCanteenWindowInput{
			Name: request.Name, Floor: request.Floor, SortOrder: request.SortOrder,
			IsActive: request.IsActive,
		})
	}
	respondDictionary(ctx, c, result, err, "窗口已创建")
}

// UpdateWindow 更新窗口但禁止移动所属餐厅。
func (h *Dictionary) UpdateWindow(ctx context.Context, c *app.RequestContext) {
	itemID, err := positivePathID(c.Param("window_id"), "window_id")
	var request updateWindowRequest
	if err == nil {
		err = bindJSON(c, &request)
	}
	var result *service.DictionaryWindowView
	if err == nil {
		result, err = h.service.UpdateWindow(ctx, itemID, service.UpdateCanteenWindowInput{
			CanteenIDSet: request.CanteenIDSet, Name: request.Name,
			Floor: request.Floor, FloorSet: request.FloorSet,
			SortOrder: request.SortOrder, IsActive: request.IsActive,
		})
	}
	respondDictionary(ctx, c, result, err, "窗口已更新")
}

// DeleteWindow 删除未被使用的窗口。
func (h *Dictionary) DeleteWindow(ctx context.Context, c *app.RequestContext) {
	itemID, err := positivePathID(c.Param("window_id"), "window_id")
	var result *service.DictionaryDeleteResult
	if err == nil {
		result, err = h.service.DeleteWindow(ctx, itemID)
	}
	respondDictionary(ctx, c, result, err, "窗口已删除")
}

func decodeDictionaryPatchFields(
	fields map[string]json.RawMessage,
	name **string,
	campus **string,
	floor **string,
	sortOrder **int32,
	isActive **bool,
) error {
	decode := func(key string, target any) error {
		raw, exists := fields[key]
		if !exists {
			return nil
		}
		return json.Unmarshal(raw, target)
	}
	if name != nil {
		if err := decode("name", name); err != nil {
			return err
		}
	}
	if campus != nil {
		if err := decode("campus", campus); err != nil {
			return err
		}
	}
	if floor != nil {
		if err := decode("floor", floor); err != nil {
			return err
		}
	}
	if err := decode("sort_order", sortOrder); err != nil {
		return err
	}
	return decode("is_active", isActive)
}

func respondDictionary[T any](
	ctx context.Context,
	c *app.RequestContext,
	result *T,
	err error,
	message string,
) {
	if err != nil {
		failService(ctx, c, err)
		return
	}
	c.JSON(consts.StatusOK, envelope.OK(message, result))
}
