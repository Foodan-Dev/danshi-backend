package router_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/model"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

func TestDictionaryDomainAgainstPostgres(t *testing.T) {
	gdb, database := openAuthPostgres(t)
	cfg := authTestConfig()
	sender := newCaptureEmailSender()
	engine := authTestEngine(cfg, database, sender)
	proposer := registerPostTestUser(t, engine, sender, "dict-proposer@fdueat.com", "词条提议人")
	ordinary := registerPostTestUser(t, engine, sender, "dict-ordinary@fdueat.com", "普通用户")
	admin := registerPostTestUser(t, engine, sender, "dict-admin@fdueat.com", "词条管理员")
	require.NoError(t, gdb.Model(&model.User{}).Where("id = ?", admin.User.ID).
		UpdateColumn("role", model.UserRoleAdmin).Error)
	fixture := loadPostFixture(t, gdb)
	post := createPost(t, engine, proposer.Token,
		sharePostPayload(fixture, "词条提议回绑帖子", []string{"词条回绑"}))

	t.Run("dictionary route inventory and role guard", func(t *testing.T) {
		testDictionaryRouteInventory(t, engine)
		status, response, _ := performJSON(t, engine, http.MethodPost,
			"/api/v2/admin/flavors", map[string]any{"name": "越权口味"}, ordinary.Token)
		require.Equal(t, http.StatusForbidden, status)
		require.Equal(t, apierr.BizPermissionDenied, response.ErrorCode)
	})

	t.Run("joint suggestion order approval binding and terminal freeze", func(t *testing.T) {
		testJointDictionarySuggestions(t, engine, gdb, proposer, admin, post.ID)
	})

	t.Run("duplicate suggestions reuse result and rejection is immutable", func(t *testing.T) {
		testDuplicateAndRejectedSuggestions(t, engine, gdb, proposer, ordinary, admin)
	})

	t.Run("suggestion validation ownership visibility and existing item reuse", func(t *testing.T) {
		testDictionarySuggestionEdges(t, engine, gdb, proposer, ordinary, admin, fixture, post.ID)
	})

	t.Run("approve rollback is atomic", func(t *testing.T) {
		testSuggestionApprovalRollback(t, engine, gdb, proposer, admin, post.ID)
	})

	t.Run("dictionary crud and in-use delete semantics", func(t *testing.T) {
		testDictionaryCRUD(t, engine, gdb, ordinary, admin, fixture)
	})
}

func testDictionaryRouteInventory(t *testing.T, engine *server.Hertz) {
	t.Helper()
	operations := make([]string, 0)
	for _, route := range engine.Routes() {
		path := route.Path
		if strings.HasPrefix(path, "/api/v2/dictionary-suggestions") ||
			strings.HasPrefix(path, "/api/v2/admin/dictionary-suggestions") ||
			strings.HasPrefix(path, "/api/v2/admin/flavors") ||
			strings.HasPrefix(path, "/api/v2/admin/cuisines") ||
			strings.HasPrefix(path, "/api/v2/admin/canteens") ||
			strings.HasPrefix(path, "/api/v2/admin/canteen-windows") {
			operations = append(operations, route.Method+" "+path)
		}
	}
	require.ElementsMatch(t, []string{
		"POST /api/v2/dictionary-suggestions",
		"GET /api/v2/dictionary-suggestions/mine",
		"GET /api/v2/admin/dictionary-suggestions",
		"POST /api/v2/admin/dictionary-suggestions/:suggestion_id/approve",
		"POST /api/v2/admin/dictionary-suggestions/:suggestion_id/reject",
		"POST /api/v2/admin/flavors",
		"PATCH /api/v2/admin/flavors/:flavor_id",
		"DELETE /api/v2/admin/flavors/:flavor_id",
		"POST /api/v2/admin/cuisines",
		"PATCH /api/v2/admin/cuisines/:cuisine_id",
		"DELETE /api/v2/admin/cuisines/:cuisine_id",
		"POST /api/v2/admin/canteens",
		"PATCH /api/v2/admin/canteens/:canteen_id",
		"DELETE /api/v2/admin/canteens/:canteen_id",
		"POST /api/v2/admin/canteens/:canteen_id/windows",
		"PATCH /api/v2/admin/canteen-windows/:window_id",
		"DELETE /api/v2/admin/canteen-windows/:window_id",
	}, operations)
}

func testJointDictionarySuggestions(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	proposer service.AuthResult,
	admin service.AuthResult,
	postID uint64,
) {
	t.Helper()
	var moderationBefore int64
	require.NoError(t, gdb.Model(&model.ModerationRecord{}).Count(&moderationBefore).Error)
	parent := createSuggestion(t, engine, proposer.Token, map[string]any{
		"kind": model.SuggestionKindCanteen, "proposed_name": "联合测试餐厅", "post_id": postID,
	})
	child := createSuggestion(t, engine, proposer.Token, map[string]any{
		"kind": model.SuggestionKindCanteenWindow, "proposed_name": "联合测试窗口",
		"post_id": postID, "parent_suggestion_id": parent.ID,
	})

	status, response, _ := performJSON(t, engine, http.MethodGet,
		"/api/v2/admin/dictionary-suggestions?kind=canteen_window", nil, admin.Token)
	require.Equal(t, http.StatusOK, status)
	var pending service.SuggestionList
	decodeData(t, response, &pending)
	require.Len(t, pending.Suggestions, 1)
	require.Equal(t, child.ID, pending.Suggestions[0].ID)

	status, response, _ = performJSON(t, engine, http.MethodPost,
		suggestionApprovePath(child.ID), map[string]any{"floor": "1F"}, admin.Token)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, apierr.BizSuggestionParentPending, response.ErrorCode)
	var storedChild model.DictionarySuggestion
	require.NoError(t, gdb.First(&storedChild, child.ID).Error)
	require.Equal(t, model.SuggestionStatusPending, storedChild.Status)
	var prematureWindows int64
	require.NoError(t, gdb.Model(&model.CanteenWindow{}).
		Where("name = ?", "联合测试窗口").Count(&prematureWindows).Error)
	require.Zero(t, prematureWindows, "服务层必须在创建窗口前拒绝父提议未批准")

	status, response, _ = performJSON(t, engine, http.MethodPost,
		suggestionApprovePath(parent.ID), map[string]any{
			"code": "canteen-joint-test", "campus": "测试校区", "sort_order": -10,
		}, admin.Token)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
	var approvedParent service.SuggestionView
	decodeData(t, response, &approvedParent)
	require.NotNil(t, approvedParent.ResultingCanteenID)

	status, response, _ = performJSON(t, engine, http.MethodPost,
		suggestionApprovePath(child.ID), map[string]any{"floor": "1F", "sort_order": -10}, admin.Token)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
	var approvedChild service.SuggestionView
	decodeData(t, response, &approvedChild)
	require.Equal(t, approvedParent.ResultingCanteenID, approvedChild.ParentCanteenID)
	require.NotNil(t, approvedChild.ResultingWindowID)
	var window model.CanteenWindow
	require.NoError(t, gdb.First(&window, *approvedChild.ResultingWindowID).Error)
	require.Equal(t, *approvedParent.ResultingCanteenID, window.CanteenID)

	flavor := createSuggestion(t, engine, proposer.Token, map[string]any{
		"kind": model.SuggestionKindFlavor, "proposed_name": "联合测试口味",
		"post_id": postID, "flavor_stance": model.FlavorStanceHas,
	})
	status, response, _ = performJSON(t, engine, http.MethodPost,
		suggestionApprovePath(flavor.ID), map[string]any{"sort_order": -10}, admin.Token)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
	var approvedFlavor service.SuggestionView
	decodeData(t, response, &approvedFlavor)
	require.NotNil(t, approvedFlavor.ResultingFlavorID)

	var storedPost model.Post
	require.NoError(t, gdb.First(&storedPost, postID).Error)
	require.Equal(t, approvedParent.ResultingCanteenID, storedPost.CanteenID)
	require.Equal(t, approvedChild.ResultingWindowID, storedPost.CanteenWindowID)
	var boundFlavor model.PostFlavor
	require.NoError(t, gdb.Where(
		"post_id = ? AND flavor_id = ?", postID, *approvedFlavor.ResultingFlavorID,
	).First(&boundFlavor).Error)
	require.Equal(t, model.FlavorStanceHas, boundFlavor.Stance)
	var histories []model.PostHistory
	require.NoError(t, gdb.Where("post_id = ?", postID).Order("revision").Find(&histories).Error)
	require.Len(t, histories, 4, "初始版本 + 餐厅/窗口/口味三次审批回绑")
	for revision, history := range histories {
		require.EqualValues(t, revision+1, history.Revision)
	}
	var moderationAfter int64
	require.NoError(t, gdb.Model(&model.ModerationRecord{}).Count(&moderationAfter).Error)
	require.Equal(t, moderationBefore, moderationAfter, "封闭词表及其回绑不应进入机审")

	status, response, _ = performJSON(t, engine, http.MethodPost,
		suggestionApprovePath(flavor.ID), map[string]any{}, admin.Token)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, apierr.BizSuggestionClosed, response.ErrorCode)
	require.Error(t, gdb.Model(&model.DictionarySuggestion{}).Where("id = ?", flavor.ID).
		UpdateColumn("proposed_name", "篡改终态").Error, "schema 触发器必须冻结终态内容")
}

func testDuplicateAndRejectedSuggestions(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	proposer service.AuthResult,
	ordinary service.AuthResult,
	admin service.AuthResult,
) {
	t.Helper()
	first := createSuggestion(t, engine, proposer.Token, map[string]any{
		"kind": model.SuggestionKindCuisine, "proposed_name": "重复提议菜系",
	})
	second := createSuggestion(t, engine, ordinary.Token, map[string]any{
		"kind": model.SuggestionKindCuisine, "proposed_name": "重复提议菜系",
	})
	status, response, _ := performJSON(t, engine, http.MethodPost,
		suggestionApprovePath(first.ID), map[string]any{}, admin.Token)
	require.Equal(t, http.StatusOK, status)
	var approvedFirst service.SuggestionView
	decodeData(t, response, &approvedFirst)
	require.NotNil(t, approvedFirst.ResultingCuisineID)
	status, response, _ = performJSON(t, engine, http.MethodPost,
		suggestionApprovePath(second.ID), map[string]any{
			"existing_item_id": *approvedFirst.ResultingCuisineID,
		}, admin.Token)
	require.Equal(t, http.StatusOK, status)
	var approvedSecond service.SuggestionView
	decodeData(t, response, &approvedSecond)
	require.Equal(t, approvedFirst.ResultingCuisineID, approvedSecond.ResultingCuisineID,
		"同名提议必须能复用同一产出词条")
	var count int64
	require.NoError(t, gdb.Model(&model.Cuisine{}).
		Where("name = ?", "重复提议菜系").Count(&count).Error)
	require.EqualValues(t, 1, count)

	rejected := createSuggestion(t, engine, proposer.Token, map[string]any{
		"kind": model.SuggestionKindCanteen, "proposed_name": "应被驳回餐厅",
	})
	status, response, _ = performJSON(t, engine, http.MethodPost,
		suggestionRejectPath(rejected.ID), map[string]any{"review_note": "信息不足"}, admin.Token)
	require.Equal(t, http.StatusOK, status)
	var rejectedView service.SuggestionView
	decodeData(t, response, &rejectedView)
	require.Equal(t, model.SuggestionStatusRejected, rejectedView.Status)
	require.Equal(t, "信息不足", *rejectedView.ReviewNote)
	status, response, _ = performJSON(t, engine, http.MethodPost,
		suggestionRejectPath(rejected.ID), map[string]any{"review_note": "再次驳回"}, admin.Token)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, apierr.BizSuggestionClosed, response.ErrorCode)
	status, response, _ = performJSON(t, engine, http.MethodPost,
		suggestionApprovePath(rejected.ID), map[string]any{}, admin.Token)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, apierr.BizSuggestionClosed, response.ErrorCode)

	status, response, _ = performJSON(t, engine, http.MethodGet,
		"/api/v2/dictionary-suggestions/mine?limit=100", nil, proposer.Token)
	require.Equal(t, http.StatusOK, status)
	var mine service.SuggestionList
	decodeData(t, response, &mine)
	require.GreaterOrEqual(t, len(mine.Suggestions), 2)
}

func testDictionarySuggestionEdges(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	proposer service.AuthResult,
	ordinary service.AuthResult,
	admin service.AuthResult,
	fixture postFixture,
	postID uint64,
) {
	t.Helper()
	status, response, _ := performJSON(t, engine, http.MethodPost,
		"/api/v2/dictionary-suggestions", map[string]any{
			"kind": "invalid", "proposed_name": "非法类型",
		}, proposer.Token)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, apierr.BizValidation, response.ErrorCode)
	requireDictionaryFieldError(t, response, "kind", apierr.FieldInvalidEnum)

	status, response, _ = performJSON(t, engine, http.MethodPost,
		"/api/v2/dictionary-suggestions", map[string]any{
			"kind": model.SuggestionKindCanteenWindow, "proposed_name": "无父窗口",
		}, proposer.Token)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, apierr.BizValidation, response.ErrorCode)
	requireDictionaryFieldError(t, response, "parent_canteen_id", apierr.FieldRequired)

	foreignParent := createSuggestion(t, engine, ordinary.Token, map[string]any{
		"kind": model.SuggestionKindCanteen, "proposed_name": "他人的父餐厅提议",
	})
	status, response, _ = performJSON(t, engine, http.MethodPost,
		"/api/v2/dictionary-suggestions", map[string]any{
			"kind": model.SuggestionKindCanteenWindow, "proposed_name": "越权父提议窗口",
			"parent_suggestion_id": foreignParent.ID,
		}, proposer.Token)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, apierr.BizSuggestionNotFound, response.ErrorCode)

	status, response, _ = performJSON(t, engine, http.MethodPost,
		"/api/v2/dictionary-suggestions", map[string]any{
			"kind": model.SuggestionKindCuisine, "proposed_name": "越权来源帖", "post_id": postID,
		}, ordinary.Token)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, apierr.BizNotOwner, response.ErrorCode)

	first := createSuggestion(t, engine, proposer.Token, map[string]any{
		"kind": model.SuggestionKindCuisine, "proposed_name": "同一用户重复提议",
	})
	second := createSuggestion(t, engine, proposer.Token, map[string]any{
		"kind": model.SuggestionKindCuisine, "proposed_name": "同一用户重复提议",
	})
	require.NotEqual(t, first.ID, second.ID, "重复提议必须各自保留审核与来源语义")

	status, response, _ = performJSON(t, engine, http.MethodGet,
		"/api/v2/dictionary-suggestions/mine?limit=100", nil, ordinary.Token)
	require.Equal(t, http.StatusOK, status)
	var ordinaryMine service.SuggestionList
	decodeData(t, response, &ordinaryMine)
	require.False(t, suggestionPresent(ordinaryMine.Suggestions, first.ID))
	require.False(t, suggestionPresent(ordinaryMine.Suggestions, second.ID))

	existing := createSuggestion(t, engine, proposer.Token, map[string]any{
		"kind": model.SuggestionKindCuisine, "proposed_name": fixture.Cuisine.Name,
	})
	status, response, _ = performJSON(t, engine, http.MethodPost,
		suggestionApprovePath(existing.ID), map[string]any{}, ordinary.Token)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, apierr.BizPermissionDenied, response.ErrorCode)
	var stillPending model.DictionarySuggestion
	require.NoError(t, gdb.First(&stillPending, existing.ID).Error)
	require.Equal(t, model.SuggestionStatusPending, stillPending.Status)

	status, response, _ = performJSON(t, engine, http.MethodPost,
		suggestionApprovePath(existing.ID), map[string]any{}, admin.Token)
	require.Equal(t, http.StatusOK, status)
	var reused service.SuggestionView
	decodeData(t, response, &reused)
	require.Equal(t, &fixture.Cuisine.ID, reused.ResultingCuisineID)
	var cuisineCount int64
	require.NoError(t, gdb.Model(&model.Cuisine{}).
		Where("name = ?", fixture.Cuisine.Name).Count(&cuisineCount).Error)
	require.EqualValues(t, 1, cuisineCount)

	status, response, _ = performJSON(t, engine, http.MethodGet,
		"/api/v2/admin/dictionary-suggestions?kind=invalid", nil, admin.Token)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	require.Equal(t, apierr.BizValidation, response.ErrorCode)
	requireDictionaryFieldError(t, response, "kind", apierr.FieldInvalidEnum)
	status, response, _ = performJSON(t, engine, http.MethodGet,
		"/api/v2/dictionary-suggestions/mine?page=999&limit=100", nil, proposer.Token)
	require.Equal(t, http.StatusOK, status)
	var beyond service.SuggestionList
	decodeData(t, response, &beyond)
	require.Empty(t, beyond.Suggestions)
}

func testSuggestionApprovalRollback(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	proposer service.AuthResult,
	admin service.AuthResult,
	postID uint64,
) {
	t.Helper()
	suggestion := createSuggestion(t, engine, proposer.Token, map[string]any{
		"kind": model.SuggestionKindFlavor, "proposed_name": "审批事务回滚口味",
		"post_id": postID, "flavor_stance": model.FlavorStanceHas,
	})
	require.NoError(t, gdb.Exec(`
		CREATE FUNCTION dictionary_test_reject_history() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
		  IF NEW.edit_reason LIKE '词条提议 %' THEN
		    RAISE EXCEPTION 'forced dictionary history failure';
		  END IF;
		  RETURN NEW;
		END $$;
		CREATE TRIGGER dictionary_test_reject_history
		BEFORE INSERT ON post_histories
		FOR EACH ROW EXECUTE FUNCTION dictionary_test_reject_history();
	`).Error)
	status, _, _ := performJSON(t, engine, http.MethodPost,
		suggestionApprovePath(suggestion.ID), map[string]any{}, admin.Token)
	require.Equal(t, http.StatusInternalServerError, status)
	require.NoError(t, gdb.Exec(`
		DROP TRIGGER dictionary_test_reject_history ON post_histories;
		DROP FUNCTION dictionary_test_reject_history();
	`).Error)
	var stored model.DictionarySuggestion
	require.NoError(t, gdb.First(&stored, suggestion.ID).Error)
	require.Equal(t, model.SuggestionStatusPending, stored.Status,
		"终态更新必须随历史写入失败一起回滚")
	var flavorCount, bindingCount int64
	require.NoError(t, gdb.Model(&model.Flavor{}).
		Where("name = ?", "审批事务回滚口味").Count(&flavorCount).Error)
	require.Zero(t, flavorCount, "审批创建的词条必须回滚")
	require.NoError(t, gdb.Table("post_flavors AS pf").
		Joins("JOIN flavors AS f ON f.id = pf.flavor_id").
		Where("pf.post_id = ? AND f.name = ?", postID, "审批事务回滚口味").
		Count(&bindingCount).Error)
	require.Zero(t, bindingCount, "来源帖子回绑必须回滚")
}

func testDictionaryCRUD(
	t *testing.T,
	engine *server.Hertz,
	gdb *gorm.DB,
	ordinary service.AuthResult,
	admin service.AuthResult,
	fixture postFixture,
) {
	t.Helper()
	flavor := createAdminFlavor(t, engine, admin.Token, "CRUD 临时口味")
	status, response, _ := performJSON(t, engine, http.MethodPost,
		"/api/v2/admin/flavors", map[string]any{"name": flavor.Name}, admin.Token)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, apierr.BizAlreadyExists, response.ErrorCode)
	status, response, _ = performJSON(t, engine, http.MethodPatch,
		fmt.Sprintf("/api/v2/admin/flavors/%d", flavor.ID),
		map[string]any{"name": "CRUD 已改口味", "is_active": false}, admin.Token)
	require.Equal(t, http.StatusOK, status)
	var updatedFlavor service.DictionaryItemView
	decodeData(t, response, &updatedFlavor)
	require.Equal(t, "CRUD 已改口味", updatedFlavor.Name)
	require.False(t, updatedFlavor.IsActive)
	status, _, _ = performJSON(t, engine, http.MethodDelete,
		fmt.Sprintf("/api/v2/admin/flavors/%d", flavor.ID), nil, admin.Token)
	require.Equal(t, http.StatusOK, status)

	status, response, _ = performJSON(t, engine, http.MethodPost,
		"/api/v2/admin/cuisines", map[string]any{"name": "CRUD 临时菜系"}, admin.Token)
	require.Equal(t, http.StatusOK, status)
	var cuisine service.DictionaryItemView
	decodeData(t, response, &cuisine)
	status, response, _ = performJSON(t, engine, http.MethodPost,
		"/api/v2/admin/cuisines", map[string]any{"name": cuisine.Name}, admin.Token)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, apierr.BizAlreadyExists, response.ErrorCode)
	status, _, _ = performJSON(t, engine, http.MethodPatch,
		fmt.Sprintf("/api/v2/admin/cuisines/%d", cuisine.ID),
		map[string]any{"sort_order": 88}, admin.Token)
	require.Equal(t, http.StatusOK, status)
	status, _, _ = performJSON(t, engine, http.MethodDelete,
		fmt.Sprintf("/api/v2/admin/cuisines/%d", cuisine.ID), nil, admin.Token)
	require.Equal(t, http.StatusOK, status)

	status, response, _ = performJSON(t, engine, http.MethodPost,
		"/api/v2/admin/canteens", map[string]any{
			"code": "canteen-crud-temp", "name": "CRUD 临时餐厅", "campus": "测试校区",
		}, admin.Token)
	require.Equal(t, http.StatusOK, status)
	var canteen service.DictionaryCanteenView
	decodeData(t, response, &canteen)
	status, response, _ = performJSON(t, engine, http.MethodPost,
		"/api/v2/admin/canteens", map[string]any{
			"code": canteen.Code, "name": "CRUD 重复 code 餐厅", "campus": "测试校区",
		}, admin.Token)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, apierr.BizAlreadyExists, response.ErrorCode)
	status, _, _ = performJSON(t, engine, http.MethodPatch,
		fmt.Sprintf("/api/v2/admin/canteens/%d", canteen.ID),
		map[string]any{"code": "changed-code"}, admin.Token)
	require.Equal(t, http.StatusUnprocessableEntity, status)
	status, response, _ = performJSON(t, engine, http.MethodPost,
		fmt.Sprintf("/api/v2/admin/canteens/%d/windows", canteen.ID),
		map[string]any{"name": "CRUD 临时窗口", "floor": "2F"}, admin.Token)
	require.Equal(t, http.StatusOK, status)
	var window service.DictionaryWindowView
	decodeData(t, response, &window)
	status, response, _ = performJSON(t, engine, http.MethodPost,
		fmt.Sprintf("/api/v2/admin/canteens/%d/windows", canteen.ID),
		map[string]any{"name": window.Name, "floor": "2F"}, admin.Token)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, apierr.BizAlreadyExists, response.ErrorCode)
	status, response, _ = performJSON(t, engine, http.MethodPatch,
		fmt.Sprintf("/api/v2/admin/canteen-windows/%d", window.ID),
		map[string]any{"floor": nil, "is_active": false}, admin.Token)
	require.Equal(t, http.StatusOK, status)
	decodeData(t, response, &window)
	require.Nil(t, window.Floor)
	require.False(t, window.IsActive)
	status, _, _ = performJSON(t, engine, http.MethodDelete,
		fmt.Sprintf("/api/v2/admin/canteen-windows/%d", window.ID), nil, admin.Token)
	require.Equal(t, http.StatusOK, status)
	status, _, _ = performJSON(t, engine, http.MethodDelete,
		fmt.Sprintf("/api/v2/admin/canteens/%d", canteen.ID), nil, admin.Token)
	require.Equal(t, http.StatusOK, status)

	var approvedFlavor model.Flavor
	require.NoError(t, gdb.Where("name = ?", "联合测试口味").First(&approvedFlavor).Error)
	status, response, _ = performJSON(t, engine, http.MethodPatch,
		fmt.Sprintf("/api/v2/admin/flavors/%d", approvedFlavor.ID),
		map[string]any{"is_active": false}, admin.Token)
	require.Equal(t, http.StatusOK, status)
	var inactiveFlavor service.DictionaryItemView
	decodeData(t, response, &inactiveFlavor)
	require.False(t, inactiveFlavor.IsActive, "被帖子引用的词条仍必须允许停用")
	status, response, _ = performJSON(t, engine, http.MethodDelete,
		fmt.Sprintf("/api/v2/admin/flavors/%d", approvedFlavor.ID), nil, admin.Token)
	require.Equal(t, http.StatusConflict, status)
	require.Equal(t, apierr.BizDictItemInUse, response.ErrorCode)

	createPost(t, engine, ordinary.Token,
		sharePostPayload(fixture, "词表引用删除保护", []string{"词表保护"}))
	for _, target := range []struct {
		path string
		name string
	}{
		{fmt.Sprintf("/api/v2/admin/cuisines/%d", fixture.Cuisine.ID), "被引用菜系"},
		{fmt.Sprintf("/api/v2/admin/canteen-windows/%d", fixture.Window.ID), "被引用窗口"},
		{fmt.Sprintf("/api/v2/admin/canteens/%d", fixture.Canteen.ID), "被引用餐厅"},
	} {
		status, response, _ = performJSON(t, engine, http.MethodDelete, target.path, nil, admin.Token)
		require.Equal(t, http.StatusConflict, status, target.name)
		require.Equal(t, apierr.BizDictItemInUse, response.ErrorCode, target.name)
	}
	status, response, _ = performJSON(t, engine, http.MethodPatch,
		fmt.Sprintf("/api/v2/admin/canteens/%d", fixture.Canteen.ID),
		map[string]any{"is_active": false}, admin.Token)
	require.Equal(t, http.StatusOK, status)
	status, response, _ = performJSON(t, engine, http.MethodGet, "/api/v2/config", nil, "")
	require.Equal(t, http.StatusOK, status)
	var config service.ExploreConfig
	decodeData(t, response, &config)
	for _, item := range config.Canteens {
		require.NotEqual(t, fixture.Canteen.Code, item.ID,
			"父餐厅停用后自身及其仍启用窗口都不能出现在配置中")
	}

	require.NoError(t, gdb.Model(&model.User{}).Where("id = ?", ordinary.User.ID).
		UpdateColumn("role", model.UserRoleSuperAdmin).Error)
	superFlavor := createAdminFlavor(t, engine, ordinary.Token, "超级管理员口味")
	status, _, _ = performJSON(t, engine, http.MethodDelete,
		fmt.Sprintf("/api/v2/admin/flavors/%d", superFlavor.ID), nil, ordinary.Token)
	require.Equal(t, http.StatusOK, status, "super_admin 应拥有普通管理员词表权限")
}

func requireDictionaryFieldError(
	t *testing.T,
	response testAPIResponse,
	field string,
	code apierr.FieldCode,
) {
	t.Helper()
	var data struct {
		Errors []apierr.FieldError `json:"errors"`
	}
	decodeData(t, response, &data)
	for _, item := range data.Errors {
		if item.Field == field && item.Code == code {
			return
		}
	}
	t.Fatalf("未找到字段错误 field=%s code=%s，实际=%+v", field, code, data.Errors)
}

func suggestionPresent(suggestions []service.SuggestionView, id uint64) bool {
	for _, suggestion := range suggestions {
		if suggestion.ID == id {
			return true
		}
	}
	return false
}

func createSuggestion(
	t *testing.T,
	engine *server.Hertz,
	token string,
	body map[string]any,
) service.SuggestionView {
	t.Helper()
	status, response, _ := performJSON(t, engine, http.MethodPost,
		"/api/v2/dictionary-suggestions", body, token)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
	var result service.SuggestionView
	decodeData(t, response, &result)
	require.NotZero(t, result.ID)
	require.Equal(t, model.SuggestionStatusPending, result.Status)
	return result
}

func createAdminFlavor(
	t *testing.T,
	engine *server.Hertz,
	token string,
	name string,
) service.DictionaryItemView {
	t.Helper()
	status, response, _ := performJSON(t, engine, http.MethodPost,
		"/api/v2/admin/flavors", map[string]any{"name": name}, token)
	require.Equal(t, http.StatusOK, status, "error_code=%s message=%s", response.ErrorCode, response.Message)
	var result service.DictionaryItemView
	decodeData(t, response, &result)
	return result
}

func suggestionApprovePath(suggestionID uint64) string {
	return fmt.Sprintf("/api/v2/admin/dictionary-suggestions/%d/approve", suggestionID)
}

func suggestionRejectPath(suggestionID uint64) string {
	return fmt.Sprintf("/api/v2/admin/dictionary-suggestions/%d/reject", suggestionID)
}
