package service

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/money"
)

const (
	maxPostTitleRunes   = 200
	maxPostContentRunes = 5000
	maxPostTags         = 10
	maxTagRunes         = 10
	maxPostImages       = 9
	maxEditReasonRunes  = 200
)

type normalizedPostPayload struct {
	PostType        model.PostType
	ShareType       *model.ShareType
	Category        model.PostCategory
	Title           string
	Content         string
	CanteenCode     *string
	CanteenWindowID *uint64
	Cuisine         *string
	Price           *money.Amount
	FlavorNames     []string
	FlavorStances   map[string]model.FlavorStance
	Tags            []string
	Images          []string
	BudgetRange     *BudgetRangeInput
	Preferences     *PreferencesInput
	Publish         bool
	EditReason      *string
}

func normalizeCreatePost(input CreatePostInput) (normalizedPostPayload, error) {
	postType := model.PostType(input.PostType)
	if !validPostType(postType) {
		return normalizedPostPayload{}, apierr.InvalidField(
			"post_type", apierr.FieldInvalidEnum, "post_type 取值不合法",
		)
	}
	publish, err := publishIntent(input.Status, model.PostStatusPending)
	if err != nil {
		return normalizedPostPayload{}, err
	}
	return normalizePostPayload(input.PostPayload, postType, publish, nil)
}

func normalizeUpdatePost(
	input UpdatePostInput,
	current *model.Post,
) (normalizedPostPayload, error) {
	if input.PostType != nil {
		requested := model.PostType(*input.PostType)
		if !validPostType(requested) {
			return normalizedPostPayload{}, apierr.InvalidField(
				"post_type", apierr.FieldInvalidEnum, "post_type 取值不合法",
			)
		}
		if requested != current.PostType {
			return normalizedPostPayload{}, apierr.InvalidField(
				"post_type", apierr.FieldConflict, "帖子类型创建后不可修改",
			)
		}
	}
	publish, err := publishIntent(input.Status, current.Status)
	if err != nil {
		return normalizedPostPayload{}, err
	}
	reason, err := normalizeEditReason(input.EditReason)
	if err != nil {
		return normalizedPostPayload{}, err
	}
	return normalizePostPayload(input.PostPayload, current.PostType, publish, reason)
}

func normalizePostPayload(
	payload PostPayload,
	postType model.PostType,
	publish bool,
	editReason *string,
) (normalizedPostPayload, error) {
	value := normalizedPostPayload{
		PostType: postType, Title: strings.TrimSpace(payload.Title),
		Content: strings.TrimSpace(payload.Content), Category: model.PostCategory(payload.Category),
		CanteenCode: normalizeOptional(payload.CanteenCode), CanteenWindowID: payload.CanteenWindowID,
		Cuisine: normalizeOptional(payload.Cuisine), Price: payload.Price,
		Tags: normalizeTags(payload.Tags), Images: normalizeStrings(payload.Images),
		BudgetRange: payload.BudgetRange, Preferences: normalizePreferences(payload.Preferences),
		Publish: publish, EditReason: editReason,
	}
	if err := validateBasicPostFields(&value, payload.Tags, payload.Images); err != nil {
		return normalizedPostPayload{}, err
	}
	shareType, err := normalizeShareType(payload.ShareType)
	if err != nil {
		return normalizedPostPayload{}, err
	}
	value.ShareType = shareType
	if err := validateLocation(&value); err != nil {
		return normalizedPostPayload{}, err
	}
	if err := normalizeFlavorSemantics(&value, payload.Flavors); err != nil {
		return normalizedPostPayload{}, err
	}
	if err := validatePostTypeFields(&value); err != nil {
		return normalizedPostPayload{}, err
	}
	return value, nil
}

func validateBasicPostFields(
	value *normalizedPostPayload,
	rawTags []string,
	rawImages []string,
) error {
	if value.Title == "" {
		return apierr.InvalidField("title", apierr.FieldRequired, "title 不能为空")
	}
	if utf8.RuneCountInString(value.Title) > maxPostTitleRunes {
		return apierr.InvalidField("title", apierr.FieldTooLong, "title 不能超过 200 个字符")
	}
	if value.Content == "" {
		return apierr.InvalidField("content", apierr.FieldRequired, "content 不能为空")
	}
	if utf8.RuneCountInString(value.Content) > maxPostContentRunes {
		return apierr.InvalidField("content", apierr.FieldTooLong, "content 不能超过 5000 个字符")
	}
	if !validCategory(value.Category) {
		return apierr.InvalidField("category", apierr.FieldInvalidEnum, "category 取值不合法")
	}
	if len(value.Tags) > maxPostTags || len(rawTags) > maxPostTags {
		return apierr.BadRequest(apierr.BizTagLimitExceeded, "单帖标签数量不能超过 10 个")
	}
	for index, tag := range value.Tags {
		if tag == "" {
			return apierr.InvalidField("tags", apierr.FieldInvalidFormat, "标签不能为空")
		}
		if utf8.RuneCountInString(tag) > maxTagRunes {
			return apierr.InvalidField("tags", apierr.FieldTooLong, "第 %d 个标签不能超过 10 个字符", index+1)
		}
	}
	if len(value.Images) > maxPostImages || len(rawImages) > maxPostImages {
		return apierr.InvalidField("images", apierr.FieldOutOfRange, "单帖图片数量不能超过 9 张")
	}
	cleanedRawImages := make([]string, 0, len(rawImages))
	for _, image := range rawImages {
		trimmed := strings.TrimSpace(image)
		if trimmed == "" {
			return apierr.InvalidField("images", apierr.FieldInvalidFormat, "图片 URL 不能为空")
		}
		cleanedRawImages = append(cleanedRawImages, trimmed)
	}
	if hasDuplicates(cleanedRawImages, false) {
		return apierr.InvalidField("images", apierr.FieldConflict, "同一张图片不能重复引用")
	}
	return nil
}

func validateLocation(value *normalizedPostPayload) error {
	if value.CanteenWindowID != nil && *value.CanteenWindowID == 0 {
		return apierr.InvalidField(
			"canteen_window_id", apierr.FieldInvalidFormat, "canteen_window_id 必须是正整数",
		)
	}
	if value.CanteenWindowID != nil && value.CanteenCode == nil {
		return apierr.InvalidField(
			"canteen_window_id", apierr.FieldConflict, "选择窗口时必须同时选择餐厅",
		)
	}
	return nil
}

func validatePostTypeFields(value *normalizedPostPayload) error {
	if value.BudgetRange != nil {
		if value.BudgetRange.Min < 0 || value.BudgetRange.Max < 0 {
			return apierr.InvalidField("budget_range", apierr.FieldOutOfRange, "预算不能为负数")
		}
		if value.BudgetRange.Max < value.BudgetRange.Min {
			return apierr.InvalidField(
				"budget_range.max", apierr.FieldConflict, "最大预算不能小于最小预算",
			)
		}
	}
	if value.PostType == model.PostTypeShare {
		if value.Publish && value.ShareType == nil {
			return apierr.InvalidField("share_type", apierr.FieldRequired, "分享帖发布时必须选择推荐或避雷")
		}
		if value.BudgetRange != nil || value.Preferences != nil {
			return apierr.InvalidField(
				"budget_range", apierr.FieldConflict, "分享帖不能填写预算或口味偏好",
			)
		}
		return nil
	}
	if value.ShareType != nil {
		return apierr.InvalidField("share_type", apierr.FieldConflict, "提问帖不能包含 share_type")
	}
	if value.Price != nil {
		return apierr.InvalidField("price", apierr.FieldConflict, "提问帖不能填写 price")
	}
	return nil
}

func normalizeFlavorSemantics(value *normalizedPostPayload, rawFlavors []string) error {
	value.FlavorStances = make(map[string]model.FlavorStance)
	if value.PostType == model.PostTypeShare {
		value.FlavorNames = normalizeStrings(rawFlavors)
		for _, name := range value.FlavorNames {
			value.FlavorStances[name] = model.FlavorStanceHas
		}
		return nil
	}
	if len(normalizeStrings(rawFlavors)) > 0 {
		return apierr.InvalidField("flavors", apierr.FieldConflict, "提问帖请使用 preferences 表达口味偏好")
	}
	if value.Preferences == nil {
		value.FlavorNames = []string{}
		return nil
	}
	for _, name := range value.Preferences.PreferFlavors {
		value.FlavorStances[name] = model.FlavorStancePrefer
		value.FlavorNames = append(value.FlavorNames, name)
	}
	for _, name := range value.Preferences.AvoidFlavors {
		if _, exists := value.FlavorStances[name]; exists {
			return apierr.InvalidField(
				"preferences", apierr.FieldConflict, "同一口味不能既希望有又希望避开",
			)
		}
		value.FlavorStances[name] = model.FlavorStanceAvoid
		value.FlavorNames = append(value.FlavorNames, name)
	}
	return nil
}

func publishIntent(requested *string, current model.PostStatus) (bool, error) {
	if requested == nil {
		return current != model.PostStatusDraft, nil
	}
	switch model.PostStatus(*requested) {
	case model.PostStatusDraft:
		return false, nil
	case model.PostStatusPending, model.PostStatusApproved:
		return true, nil
	default:
		return false, apierr.InvalidField("status", apierr.FieldInvalidEnum, "status 只能是 draft 或 pending")
	}
}

func normalizeShareType(value *string) (*model.ShareType, error) {
	if value == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*value)
	shareType := model.ShareType(trimmed)
	if !validShareType(shareType) {
		return nil, apierr.InvalidField("share_type", apierr.FieldInvalidEnum, "share_type 取值不合法")
	}
	return &shareType, nil
}

func normalizeEditReason(value *string) (*string, error) {
	value = normalizeOptional(value)
	if value != nil && utf8.RuneCountInString(*value) > maxEditReasonRunes {
		return nil, apierr.InvalidField("edit_reason", apierr.FieldTooLong, "edit_reason 不能超过 200 个字符")
	}
	return value, nil
}

func normalizeOptional(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func normalizeTags(values []string) []string {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		normalized = append(normalized, strings.TrimSpace(norm.NFKC.String(value)))
	}
	return deduplicateStrings(normalized, true)
}

func normalizeStrings(values []string) []string {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			normalized = append(normalized, trimmed)
		}
	}
	return deduplicateStrings(normalized, false)
}

func normalizePreferences(value *PreferencesInput) *PreferencesInput {
	if value == nil {
		return nil
	}
	return &PreferencesInput{
		AvoidFlavors: normalizeStrings(value.AvoidFlavors), PreferFlavors: normalizeStrings(value.PreferFlavors),
	}
}

func cleanNames(values []string) []string { return normalizeStrings(values) }

func deduplicateStrings(values []string, foldCase bool) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		key := value
		if foldCase {
			key = strings.ToLower(value)
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func hasDuplicates(values []string, foldCase bool) bool {
	return len(deduplicateStrings(values, foldCase)) != len(values)
}

func validPostType(value model.PostType) bool {
	return value == model.PostTypeShare || value == model.PostTypeSeeking
}

func validShareType(value model.ShareType) bool {
	return value == model.ShareTypeRecommend || value == model.ShareTypeWarning
}

func validCategory(value model.PostCategory) bool {
	return value == model.PostCategoryFood || value == model.PostCategoryRecipe
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}
