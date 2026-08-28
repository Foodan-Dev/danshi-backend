package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/pagination"
	"github.com/Foodan-Dev/danshi-backend/internal/pkg/ptime"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
)

// CreateSuggestionInput 是用户词条提议。
type CreateSuggestionInput struct {
	Kind               string
	ProposedName       string
	PostID             *uint64
	FlavorStance       *string
	ParentCanteenCode  *string
	ParentSuggestionID *uint64
}

// ApproveSuggestionInput 是管理员建条目或复用现有条目所需的补充信息。
type ApproveSuggestionInput struct {
	ExistingItemID *uint64
	Code           *string
	Campus         *string
	Floor          *string
	SortOrder      int32
	ReviewNote     *string
}

// SuggestionProposer 是审核列表中的提议人快照展示。
type SuggestionProposer struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
}

// SuggestionView 是词条提议及不可变审核结论。
type SuggestionView struct {
	ID                 uint64                 `json:"id"`
	Kind               model.SuggestionKind   `json:"kind"`
	ProposedName       string                 `json:"proposed_name"`
	Proposer           SuggestionProposer     `json:"proposer"`
	PostID             *uint64                `json:"post_id"`
	FlavorStance       *model.FlavorStance    `json:"flavor_stance"`
	ParentCanteenID    *uint64                `json:"parent_canteen_id"`
	ParentSuggestionID *uint64                `json:"parent_suggestion_id"`
	Status             model.SuggestionStatus `json:"status"`
	ReviewerID         *uint64                `json:"reviewer_id"`
	ReviewedAt         *ptime.Time            `json:"reviewed_at"`
	ReviewNote         *string                `json:"review_note"`
	ResultingFlavorID  *uint64                `json:"resulting_flavor_id"`
	ResultingCuisineID *uint64                `json:"resulting_cuisine_id"`
	ResultingCanteenID *uint64                `json:"resulting_canteen_id"`
	ResultingWindowID  *uint64                `json:"resulting_window_id"`
	CreatedAt          ptime.Time             `json:"created_at"`
	UpdatedAt          ptime.Time             `json:"updated_at"`
}

// SuggestionList 是词条提议分页响应。
type SuggestionList struct {
	Suggestions []SuggestionView `json:"suggestions"`
	Pagination  pagination.Meta  `json:"pagination"`
}

// DictionaryService 实现人工词条提议审核与词表维护。
type DictionaryService struct {
	dictionaries  repository.DictionaryRepository
	posts         repository.PostRepository
	users         repository.UserRepository
	flavorCursor  *pagination.CursorCodec
	cuisineCursor *pagination.CursorCodec
	canteenCursor *pagination.CursorCodec
	windowCursor  *pagination.CursorCodec
}

// NewDictionaryService 创建词表服务。
func NewDictionaryService() *DictionaryService {
	return &DictionaryService{
		flavorCursor:  pagination.NewEphemeralCursorCodec("admin-flavors"),
		cuisineCursor: pagination.NewEphemeralCursorCodec("admin-cuisines"),
		canteenCursor: pagination.NewEphemeralCursorCodec("admin-canteens"),
		windowCursor:  pagination.NewEphemeralCursorCodec("admin-canteen-windows"),
	}
}

// NewDictionaryServiceWithCursorSecret 创建跨实例可互认管理游标的词表服务。
func NewDictionaryServiceWithCursorSecret(cursorSecret string) *DictionaryService {
	return &DictionaryService{
		flavorCursor:  pagination.NewCursorCodec(cursorSecret, "admin-flavors"),
		cuisineCursor: pagination.NewCursorCodec(cursorSecret, "admin-cuisines"),
		canteenCursor: pagination.NewCursorCodec(cursorSecret, "admin-canteens"),
		windowCursor:  pagination.NewCursorCodec(cursorSecret, "admin-canteen-windows"),
	}
}

// CreateSuggestion 创建 pending 提议；封闭词表不调用内容机审。
func (s *DictionaryService) CreateSuggestion(
	ctx context.Context,
	input CreateSuggestionInput,
	proposerID uint64,
) (*SuggestionView, error) {
	normalized, err := s.normalizeSuggestion(ctx, input, proposerID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	suggestion := &model.DictionarySuggestion{
		Kind: normalized.Kind, ProposedName: normalized.ProposedName, ProposerID: proposerID,
		PostID: normalized.PostID, FlavorStance: normalized.FlavorStance,
		ParentCanteenID:    normalized.ParentCanteenID,
		ParentSuggestionID: normalized.ParentSuggestionID,
		Status:             model.SuggestionStatusPending, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.dictionaries.CreateSuggestion(ctx, suggestion); err != nil {
		return nil, dictionaryWriteError(err)
	}
	user, err := s.users.FindByID(ctx, proposerID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	record := repository.SuggestionRecord{DictionarySuggestion: *suggestion, ProposerName: user.Name}
	view := suggestionView(record)
	return &view, nil
}

// Mine 返回当前用户自己的全部提议状态。
func (s *DictionaryService) Mine(
	ctx context.Context,
	proposerID uint64,
	params pagination.Params,
) (*SuggestionList, error) {
	rows, meta, err := s.dictionaries.FindMinePage(ctx, proposerID, params)
	return suggestionList(rows, meta, err)
}

// Pending 返回管理员待审提议，可按 kind 筛选。
func (s *DictionaryService) Pending(
	ctx context.Context,
	kind string,
	params pagination.Params,
) (*SuggestionList, error) {
	parsed, err := parseSuggestionKind(kind, true)
	if err != nil {
		return nil, err
	}
	rows, meta, err := s.dictionaries.FindPendingPage(ctx, parsed, params)
	return suggestionList(rows, meta, err)
}

// Approve 在当前请求 UoW 的单事务内创建/复用词条、冻结审核结论并回绑来源帖子。
func (s *DictionaryService) Approve(
	ctx context.Context,
	suggestionID uint64,
	reviewerID uint64,
	input ApproveSuggestionInput,
) (*SuggestionView, error) {
	suggestion, err := s.dictionaries.LockSuggestionByID(ctx, suggestionID)
	if err != nil {
		return nil, suggestionRepositoryError(err)
	}
	if suggestion.Status != model.SuggestionStatusPending {
		return nil, apierr.Conflict(apierr.BizSuggestionClosed, "提议已处理，不可重复审核")
	}
	input, err = normalizeApproveSuggestion(input)
	if err != nil {
		return nil, err
	}
	parentCanteenID, err := s.resolveApprovalParent(ctx, suggestion)
	if err != nil {
		return nil, err
	}
	post, err := s.lockSourcePost(ctx, suggestion)
	if err != nil {
		return nil, err
	}
	resultID, resultField, err := s.approveDictionaryItem(ctx, suggestion, parentCanteenID, input)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	fields := map[string]any{
		"status": model.SuggestionStatusApproved, "reviewer_id": reviewerID,
		"reviewed_at": now, "review_note": input.ReviewNote, resultField: resultID,
	}
	if suggestion.Kind == model.SuggestionKindCanteenWindow {
		fields["parent_canteen_id"] = parentCanteenID
		suggestion.ParentCanteenID = parentCanteenID
	}
	if err := s.dictionaries.ReviewSuggestion(ctx, suggestion.ID, fields); err != nil {
		return nil, suggestionReviewError(err)
	}
	if post != nil {
		if err := s.bindSuggestionToPost(
			ctx, suggestion, post, resultID, parentCanteenID, reviewerID, now,
		); err != nil {
			return nil, err
		}
	}
	suggestion.Status = model.SuggestionStatusApproved
	suggestion.ReviewerID, suggestion.ReviewedAt, suggestion.ReviewNote = &reviewerID, &now, input.ReviewNote
	suggestion.UpdatedAt = now
	setSuggestionResult(suggestion, resultField, resultID)
	record, err := s.suggestionRecord(ctx, suggestion)
	if err != nil {
		return nil, err
	}
	view := suggestionView(*record)
	return &view, nil
}

// Reject 将 pending 提议一次性写入不可变驳回终态。
func (s *DictionaryService) Reject(
	ctx context.Context,
	suggestionID uint64,
	reviewerID uint64,
	reviewNote string,
) (*SuggestionView, error) {
	suggestion, err := s.dictionaries.LockSuggestionByID(ctx, suggestionID)
	if err != nil {
		return nil, suggestionRepositoryError(err)
	}
	if suggestion.Status != model.SuggestionStatusPending {
		return nil, apierr.Conflict(apierr.BizSuggestionClosed, "提议已处理，不可重复审核")
	}
	reviewNote = strings.TrimSpace(reviewNote)
	if reviewNote == "" {
		return nil, apierr.InvalidField("review_note", apierr.FieldRequired, "review_note 不能为空")
	}
	now := time.Now().UTC()
	if err := s.dictionaries.ReviewSuggestion(ctx, suggestionID, map[string]any{
		"status": model.SuggestionStatusRejected, "reviewer_id": reviewerID,
		"reviewed_at": now, "review_note": reviewNote,
	}); err != nil {
		return nil, suggestionReviewError(err)
	}
	suggestion.Status = model.SuggestionStatusRejected
	suggestion.ReviewerID = &reviewerID
	suggestion.ReviewedAt = &now
	suggestion.ReviewNote, suggestion.UpdatedAt = &reviewNote, now
	record, err := s.suggestionRecord(ctx, suggestion)
	if err != nil {
		return nil, err
	}
	view := suggestionView(*record)
	return &view, nil
}

func (s *DictionaryService) normalizeSuggestion(
	ctx context.Context,
	input CreateSuggestionInput,
	proposerID uint64,
) (model.DictionarySuggestion, error) {
	kind, err := parseSuggestionKind(input.Kind, false)
	if err != nil {
		return model.DictionarySuggestion{}, err
	}
	name, err := requiredDictionaryText(input.ProposedName, "proposed_name", 100)
	if err != nil {
		return model.DictionarySuggestion{}, err
	}
	result := model.DictionarySuggestion{
		Kind: *kind, ProposedName: name, PostID: input.PostID,
		ParentSuggestionID: input.ParentSuggestionID,
	}
	post, err := s.validateSuggestionPost(ctx, input.PostID, proposerID)
	if err != nil {
		return result, err
	}
	if result.Kind == model.SuggestionKindFlavor {
		if input.FlavorStance == nil {
			return result, apierr.InvalidField("flavor_stance", apierr.FieldRequired, "口味提议必须指定 flavor_stance")
		}
		stance := model.FlavorStance(strings.TrimSpace(*input.FlavorStance))
		if !validFlavorStance(stance) {
			return result, apierr.InvalidField("flavor_stance", apierr.FieldInvalidEnum, "flavor_stance 取值不合法")
		}
		result.FlavorStance = &stance
	} else if input.FlavorStance != nil {
		return result, apierr.InvalidField("flavor_stance", apierr.FieldConflict, "仅口味提议可指定 flavor_stance")
	}
	if post != nil && result.FlavorStance != nil && !stanceMatchesPostType(*result.FlavorStance, post.PostType) {
		return result, apierr.InvalidField(
			"flavor_stance", apierr.FieldConflict, "flavor_stance 与来源帖子类型不匹配",
		)
	}
	if result.Kind != model.SuggestionKindCanteenWindow {
		if input.ParentCanteenCode != nil || input.ParentSuggestionID != nil {
			return result, apierr.InvalidField(
				"parent_canteen_code", apierr.FieldConflict, "仅窗口提议可指定父餐厅",
			)
		}
		return result, nil
	}
	if input.ParentCanteenCode == nil && input.ParentSuggestionID == nil {
		return result, apierr.InvalidField(
			"parent_canteen_code", apierr.FieldRequired, "窗口提议必须指定父餐厅编码或餐厅提议",
		)
	}
	parentCanteenID, err := s.resolveSubmittedCanteen(ctx, input.ParentCanteenCode)
	if err != nil {
		return result, err
	}
	result.ParentCanteenID = parentCanteenID
	if err := s.applySubmittedParentSuggestion(ctx, input, proposerID, &result); err != nil {
		return result, err
	}
	return result, nil
}

func (s *DictionaryService) validateSuggestionPost(
	ctx context.Context,
	postID *uint64,
	proposerID uint64,
) (*repository.PostRecord, error) {
	if postID == nil {
		return nil, nil
	}
	if *postID == 0 {
		return nil, apierr.InvalidField("post_id", apierr.FieldInvalidFormat, "post_id 必须是正整数")
	}
	post, err := s.posts.FindByID(ctx, *postID, repository.QueryOptions{IncludeDeleted: true})
	if err != nil || post.DeletedAt != nil {
		return nil, apierr.NotFound(apierr.BizPostNotFound, "来源帖子")
	}
	if post.AuthorID != proposerID {
		return nil, apierr.Forbidden(apierr.BizNotOwner, "只能为自己的帖子提交词条建议")
	}
	return post, nil
}

func (s *DictionaryService) resolveSubmittedCanteen(
	ctx context.Context,
	canteenCode *string,
) (*uint64, error) {
	if canteenCode == nil {
		return nil, nil
	}
	code := strings.TrimSpace(*canteenCode)
	if code == "" {
		return nil, apierr.InvalidField(
			"parent_canteen_code", apierr.FieldRequired, "parent_canteen_code 不能为空",
		)
	}
	canteen, err := s.posts.FindActiveCanteenByCode(ctx, code)
	if err != nil {
		return nil, dictionaryError(err, "父餐厅编码")
	}
	return &canteen.ID, nil
}

func (s *DictionaryService) applySubmittedParentSuggestion(
	ctx context.Context,
	input CreateSuggestionInput,
	proposerID uint64,
	result *model.DictionarySuggestion,
) error {
	if input.ParentSuggestionID == nil {
		return nil
	}
	if *input.ParentSuggestionID == 0 {
		return apierr.InvalidField(
			"parent_suggestion_id", apierr.FieldInvalidFormat, "parent_suggestion_id 必须是正整数",
		)
	}
	parent, err := s.dictionaries.FindSuggestionByID(ctx, *input.ParentSuggestionID)
	if err != nil || parent.Kind != model.SuggestionKindCanteen || parent.ProposerID != proposerID {
		return apierr.NotFound(apierr.BizSuggestionNotFound, "父餐厅提议")
	}
	switch parent.Status {
	case model.SuggestionStatusRejected:
		return apierr.Conflict(apierr.BizSuggestionClosed, "父餐厅提议已被驳回")
	case model.SuggestionStatusPending:
		if result.ParentCanteenID != nil {
			return apierr.InvalidField(
				"parent_canteen_code", apierr.FieldConflict,
				"父餐厅提议待审时不能提前指定 parent_canteen_code",
			)
		}
		return nil
	case model.SuggestionStatusApproved:
		return applyApprovedSubmittedParent(result.ParentCanteenID, parent, result)
	default:
		return apierr.Internal(errors.New("未知父餐厅提议状态"))
	}
}

func applyApprovedSubmittedParent(
	requestedCanteenID *uint64,
	parent *model.DictionarySuggestion,
	result *model.DictionarySuggestion,
) error {
	if parent.ResultingCanteenID == nil {
		return apierr.Internal(errors.New("已批准餐厅提议缺少 resulting_canteen_id"))
	}
	if requestedCanteenID != nil && *requestedCanteenID != *parent.ResultingCanteenID {
		return apierr.InvalidField(
			"parent_canteen_code", apierr.FieldConflict, "parent_canteen_code 与父提议产出不一致",
		)
	}
	result.ParentCanteenID = parent.ResultingCanteenID
	return nil
}

func (s *DictionaryService) resolveApprovalParent(
	ctx context.Context,
	suggestion *model.DictionarySuggestion,
) (*uint64, error) {
	if suggestion.Kind != model.SuggestionKindCanteenWindow {
		return nil, nil
	}
	parentID := suggestion.ParentCanteenID
	if suggestion.ParentSuggestionID != nil {
		resolved, err := s.resolveApprovedParentSuggestion(ctx, *suggestion.ParentSuggestionID, parentID)
		if err != nil {
			return nil, err
		}
		parentID = resolved
	}
	if parentID == nil {
		return nil, apierr.Conflict(apierr.BizSuggestionParentPending, "窗口提议尚未落实父餐厅")
	}
	canteen, err := repository.DictionaryByID[model.Canteen](ctx, *parentID)
	if err != nil || !canteen.IsActive {
		return nil, apierr.NotFound(apierr.BizDictItemNotFound, "父餐厅")
	}
	return parentID, nil
}

func (s *DictionaryService) resolveApprovedParentSuggestion(
	ctx context.Context,
	parentSuggestionID uint64,
	currentCanteenID *uint64,
) (*uint64, error) {
	parent, err := s.dictionaries.LockSuggestionByID(ctx, parentSuggestionID)
	if err != nil || parent.Kind != model.SuggestionKindCanteen {
		return nil, apierr.NotFound(apierr.BizSuggestionNotFound, "父餐厅提议")
	}
	if parent.Status == model.SuggestionStatusPending {
		return nil, apierr.Conflict(apierr.BizSuggestionParentPending, "必须先批准父餐厅提议")
	}
	if parent.Status != model.SuggestionStatusApproved || parent.ResultingCanteenID == nil {
		return nil, apierr.Conflict(apierr.BizSuggestionClosed, "父餐厅提议未获批准")
	}
	if currentCanteenID != nil && *currentCanteenID != *parent.ResultingCanteenID {
		return nil, apierr.Conflict(apierr.BizConflict, "窗口提议的父餐厅与父提议产出不一致")
	}
	return parent.ResultingCanteenID, nil
}

func (s *DictionaryService) lockSourcePost(
	ctx context.Context,
	suggestion *model.DictionarySuggestion,
) (*model.Post, error) {
	if suggestion.PostID == nil {
		return nil, nil
	}
	post, err := s.posts.LockByID(ctx, *suggestion.PostID, repository.QueryOptions{IncludeDeleted: true})
	if err != nil {
		return nil, postRepositoryError(err)
	}
	if post.DeletedAt != nil {
		return nil, nil
	}
	return post, nil
}

func (s *DictionaryService) approveDictionaryItem(
	ctx context.Context,
	suggestion *model.DictionarySuggestion,
	parentCanteenID *uint64,
	input ApproveSuggestionInput,
) (uint64, string, error) {
	if input.ExistingItemID != nil {
		return s.approveExistingItem(ctx, suggestion, parentCanteenID, *input.ExistingItemID)
	}
	switch suggestion.Kind {
	case model.SuggestionKindFlavor:
		item, err := s.dictionaries.FindOrCreateFlavor(ctx, suggestion.ProposedName, input.SortOrder)
		return activeFlavorResult(item, err)
	case model.SuggestionKindCuisine:
		item, err := s.dictionaries.FindOrCreateCuisine(ctx, suggestion.ProposedName, input.SortOrder)
		return activeCuisineResult(item, err)
	case model.SuggestionKindCanteen:
		if existing, err := s.dictionaries.FindCanteenByName(ctx, suggestion.ProposedName); err == nil {
			return activeCanteenResult(existing, nil)
		} else if !errors.Is(err, repository.ErrNotFound) {
			return 0, "", apierr.Internal(err)
		}
		if input.Code == nil {
			return 0, "", apierr.InvalidField("code", apierr.FieldRequired, "新餐厅必须指定 code")
		}
		if input.Campus == nil {
			return 0, "", apierr.InvalidField("campus", apierr.FieldRequired, "新餐厅必须指定 campus")
		}
		item, err := s.dictionaries.FindOrCreateCanteen(
			ctx, suggestion.ProposedName, *input.Code, *input.Campus, input.SortOrder,
		)
		return activeCanteenResult(item, err)
	case model.SuggestionKindCanteenWindow:
		if parentCanteenID == nil {
			return 0, "", apierr.Conflict(apierr.BizSuggestionParentPending, "窗口提议尚未落实父餐厅")
		}
		item, err := s.dictionaries.FindOrCreateWindow(
			ctx, *parentCanteenID, suggestion.ProposedName, input.Floor, input.SortOrder,
		)
		return activeWindowResult(item, err)
	default:
		return 0, "", apierr.Internal(errors.New("未知词条提议 kind"))
	}
}

func (s *DictionaryService) approveExistingItem(
	ctx context.Context,
	suggestion *model.DictionarySuggestion,
	parentCanteenID *uint64,
	itemID uint64,
) (uint64, string, error) {
	if itemID == 0 {
		return 0, "", apierr.InvalidField("existing_item_id", apierr.FieldInvalidFormat, "existing_item_id 必须是正整数")
	}
	switch suggestion.Kind {
	case model.SuggestionKindFlavor:
		item, err := repository.DictionaryByID[model.Flavor](ctx, itemID)
		if err != nil || !item.IsActive || item.Name != suggestion.ProposedName {
			return 0, "", apierr.NotFound(apierr.BizDictItemNotFound, "口味")
		}
		return item.ID, "resulting_flavor_id", nil
	case model.SuggestionKindCuisine:
		item, err := repository.DictionaryByID[model.Cuisine](ctx, itemID)
		if err != nil || !item.IsActive || item.Name != suggestion.ProposedName {
			return 0, "", apierr.NotFound(apierr.BizDictItemNotFound, "菜系")
		}
		return item.ID, "resulting_cuisine_id", nil
	case model.SuggestionKindCanteen:
		item, err := repository.DictionaryByID[model.Canteen](ctx, itemID)
		if err != nil || !item.IsActive || item.Name != suggestion.ProposedName {
			return 0, "", apierr.NotFound(apierr.BizDictItemNotFound, "餐厅")
		}
		return item.ID, "resulting_canteen_id", nil
	case model.SuggestionKindCanteenWindow:
		item, err := repository.DictionaryByID[model.CanteenWindow](ctx, itemID)
		if err != nil || !item.IsActive || item.Name != suggestion.ProposedName ||
			parentCanteenID == nil || item.CanteenID != *parentCanteenID {
			return 0, "", apierr.NotFound(apierr.BizDictItemNotFound, "窗口")
		}
		return item.ID, "resulting_window_id", nil
	default:
		return 0, "", apierr.Internal(errors.New("未知词条提议 kind"))
	}
}

func (s *DictionaryService) bindSuggestionToPost(
	ctx context.Context,
	suggestion *model.DictionarySuggestion,
	post *model.Post,
	resultID uint64,
	parentCanteenID *uint64,
	reviewerID uint64,
	now time.Time,
) error {
	revision, err := s.posts.NextHistoryRevision(ctx, post.ID)
	if err != nil {
		return apierr.Internal(err)
	}
	fields := map[string]any{"current_revision": revision, "updated_at": now}
	switch suggestion.Kind {
	case model.SuggestionKindFlavor:
		if suggestion.FlavorStance == nil || !stanceMatchesPostType(*suggestion.FlavorStance, post.PostType) {
			return apierr.Conflict(apierr.BizConflict, "口味立场与来源帖子类型不匹配")
		}
		if err := s.dictionaries.UpsertPostFlavor(
			ctx, post.ID, post.PostType, resultID, *suggestion.FlavorStance,
		); err != nil {
			return dictionaryWriteError(err)
		}
	case model.SuggestionKindCuisine:
		post.CuisineID = &resultID
		fields["cuisine_id"] = resultID
	case model.SuggestionKindCanteen:
		post.CanteenID, post.CanteenWindowID = &resultID, nil
		fields["canteen_id"], fields["canteen_window_id"] = resultID, nil
	case model.SuggestionKindCanteenWindow:
		if parentCanteenID == nil {
			return apierr.Internal(errors.New("批准窗口提议缺少父餐厅"))
		}
		post.CanteenID, post.CanteenWindowID = parentCanteenID, &resultID
		fields["canteen_id"], fields["canteen_window_id"] = *parentCanteenID, resultID
	}
	if err := s.posts.UpdateContent(ctx, post.ID, fields); err != nil {
		return postRepositoryError(err)
	}
	post.CurrentRevision = revision
	post.UpdatedAt = now
	relations, err := s.posts.LoadSnapshotRelations(ctx, post.ID)
	if err != nil {
		return apierr.Internal(err)
	}
	snapshot, err := json.Marshal(snapshotFromCurrent(post, relations))
	if err != nil {
		return apierr.Internal(err)
	}
	reason := fmt.Sprintf("词条提议 #%d 审批回绑", suggestion.ID)
	history := &model.PostHistory{
		PostID: post.ID, Revision: revision, EditedBy: reviewerID,
		EditedAt: now, Snapshot: snapshot, EditReason: &reason,
	}
	if err := s.posts.CreateHistory(ctx, history); err != nil {
		return historyWriteError(err)
	}
	return nil
}

func normalizeApproveSuggestion(input ApproveSuggestionInput) (ApproveSuggestionInput, error) {
	if input.Code != nil {
		value, valueErr := requiredDictionaryText(*input.Code, "code", 64)
		if valueErr != nil {
			return input, valueErr
		}
		input.Code = &value
	}
	if input.Campus != nil {
		value, valueErr := requiredDictionaryText(*input.Campus, "campus", 50)
		if valueErr != nil {
			return input, valueErr
		}
		input.Campus = &value
	}
	if input.Floor != nil {
		value := strings.TrimSpace(*input.Floor)
		switch {
		case value == "":
			input.Floor = nil
		case utf8.RuneCountInString(value) > 20:
			return input, apierr.InvalidField("floor", apierr.FieldTooLong, "floor 不能超过 20 个字符")
		default:
			input.Floor = &value
		}
	}
	if input.ReviewNote != nil {
		value := strings.TrimSpace(*input.ReviewNote)
		if value == "" {
			input.ReviewNote = nil
		} else {
			input.ReviewNote = &value
		}
	}
	return input, nil
}

func parseSuggestionKind(raw string, optional bool) (*model.SuggestionKind, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" && optional {
		return nil, nil
	}
	value := model.SuggestionKind(raw)
	if value != model.SuggestionKindFlavor && value != model.SuggestionKindCuisine &&
		value != model.SuggestionKindCanteen && value != model.SuggestionKindCanteenWindow {
		return nil, apierr.InvalidField("kind", apierr.FieldInvalidEnum, "kind 取值不合法")
	}
	return &value, nil
}

func requiredDictionaryText(raw, field string, limit int) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", apierr.InvalidField(field, apierr.FieldRequired, "%s 不能为空", field)
	}
	if utf8.RuneCountInString(value) > limit {
		return "", apierr.InvalidField(field, apierr.FieldTooLong, "%s 不能超过 %d 个字符", field, limit)
	}
	return value, nil
}

func validFlavorStance(value model.FlavorStance) bool {
	return value == model.FlavorStanceHas || value == model.FlavorStancePrefer || value == model.FlavorStanceAvoid
}

func stanceMatchesPostType(stance model.FlavorStance, postType model.PostType) bool {
	return (postType == model.PostTypeShare && stance == model.FlavorStanceHas) ||
		(postType == model.PostTypeSeeking &&
			(stance == model.FlavorStancePrefer || stance == model.FlavorStanceAvoid))
}

func suggestionList(
	rows []repository.SuggestionRecord,
	meta pagination.Meta,
	err error,
) (*SuggestionList, error) {
	if err != nil {
		return nil, apierr.Internal(err)
	}
	items := make([]SuggestionView, 0, len(rows))
	for _, row := range rows {
		items = append(items, suggestionView(row))
	}
	return &SuggestionList{Suggestions: items, Pagination: meta}, nil
}

func suggestionView(row repository.SuggestionRecord) SuggestionView {
	name := row.ProposerName
	if row.ProposerDeletedAt != nil {
		name = "已注销用户"
	}
	return SuggestionView{
		ID: row.ID, Kind: row.Kind, ProposedName: row.ProposedName,
		Proposer: SuggestionProposer{ID: row.ProposerID, Name: name},
		PostID:   row.PostID, FlavorStance: row.FlavorStance,
		ParentCanteenID: row.ParentCanteenID, ParentSuggestionID: row.ParentSuggestionID,
		Status: row.Status, ReviewerID: row.ReviewerID, ReviewedAt: ptimePointer(row.ReviewedAt),
		ReviewNote: row.ReviewNote, ResultingFlavorID: row.ResultingFlavorID,
		ResultingCuisineID: row.ResultingCuisineID, ResultingCanteenID: row.ResultingCanteenID,
		ResultingWindowID: row.ResultingWindowID,
		CreatedAt:         ptime.Time(row.CreatedAt), UpdatedAt: ptime.Time(row.UpdatedAt),
	}
}

func ptimePointer(value *time.Time) *ptime.Time {
	if value == nil {
		return nil
	}
	converted := ptime.Time(*value)
	return &converted
}

func (s *DictionaryService) suggestionRecord(
	ctx context.Context,
	suggestion *model.DictionarySuggestion,
) (*repository.SuggestionRecord, error) {
	record, err := s.dictionaries.FindSuggestionRecordByID(ctx, suggestion.ID)
	if err != nil {
		return nil, suggestionRepositoryError(err)
	}
	return record, nil
}

func setSuggestionResult(suggestion *model.DictionarySuggestion, field string, resultID uint64) {
	switch field {
	case "resulting_flavor_id":
		suggestion.ResultingFlavorID = &resultID
	case "resulting_cuisine_id":
		suggestion.ResultingCuisineID = &resultID
	case "resulting_canteen_id":
		suggestion.ResultingCanteenID = &resultID
	case "resulting_window_id":
		suggestion.ResultingWindowID = &resultID
	}
}

func activeFlavorResult(item *model.Flavor, err error) (uint64, string, error) {
	if err != nil {
		return 0, "", dictionaryWriteError(err)
	}
	if !item.IsActive {
		return 0, "", apierr.Conflict(apierr.BizDictItemNotFound, "同名口味已停用，请先启用")
	}
	return item.ID, "resulting_flavor_id", nil
}

func activeCuisineResult(item *model.Cuisine, err error) (uint64, string, error) {
	if err != nil {
		return 0, "", dictionaryWriteError(err)
	}
	if !item.IsActive {
		return 0, "", apierr.Conflict(apierr.BizDictItemNotFound, "同名菜系已停用，请先启用")
	}
	return item.ID, "resulting_cuisine_id", nil
}

func activeCanteenResult(item *model.Canteen, err error) (uint64, string, error) {
	if err != nil {
		return 0, "", dictionaryWriteError(err)
	}
	if !item.IsActive {
		return 0, "", apierr.Conflict(apierr.BizDictItemNotFound, "同名餐厅已停用，请先启用")
	}
	return item.ID, "resulting_canteen_id", nil
}

func activeWindowResult(item *model.CanteenWindow, err error) (uint64, string, error) {
	if err != nil {
		return 0, "", dictionaryWriteError(err)
	}
	if !item.IsActive {
		return 0, "", apierr.Conflict(apierr.BizDictItemNotFound, "同名窗口已停用，请先启用")
	}
	return item.ID, "resulting_window_id", nil
}

func suggestionRepositoryError(err error) error {
	if errors.Is(err, repository.ErrNotFound) {
		return apierr.NotFound(apierr.BizSuggestionNotFound, "词条提议")
	}
	return apierr.Internal(err)
}

func suggestionReviewError(err error) error {
	if errors.Is(err, repository.ErrNotFound) || repository.IsSQLState(err, "23001") {
		return apierr.Conflict(apierr.BizSuggestionClosed, "提议已是终态，不可再变更")
	}
	return dictionaryWriteError(err)
}

func dictionaryWriteError(err error) error {
	if errors.Is(err, repository.ErrAlreadyExists) || repository.IsSQLState(err, "23505") {
		return apierr.Conflict(apierr.BizAlreadyExists, "同名或同标识词表项已存在")
	}
	return apierr.Internal(err)
}
