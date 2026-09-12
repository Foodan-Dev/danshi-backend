package router_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/repository"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestUsernameReviewCandidateApplied(t *testing.T) {
	h := testutil.NewHarness(t)
	actor := h.Fixtures.CreateActor(h.Config)
	admin := h.Fixtures.CreateActor(h.Config, testutil.WithUserRole(model.UserRoleModerator))
	t.Run("pending_username_must_show_candidate", func(t *testing.T) {
		h.Moderation.SetDefaultContent(testutil.ContentVerdict(model.ModerationVerdictReview, nil, nil))
		candidate := "reviewed_candidate"
		status, response, _ := performJSON(t, h.Engine, http.MethodPut, userPath(actor.User.ID), map[string]any{"username": candidate}, actor.Token)
		require.Equal(t, http.StatusConflict, status, response.Message)
		status, response, _ = performJSON(t, h.Engine, http.MethodGet, "/api/v2/admin/moderation-records/pending", nil, admin.Token)
		require.Equal(t, http.StatusOK, status, response.Message)
		var queue service.AdminModerationList
		decodeData(t, response, &queue)
		require.Len(t, queue.Records, 1)
		require.NotNil(t, queue.Records[0].Content)
		t.Logf("candidate=%s current=%s queue_content=%s", candidate, actor.User.Username, *queue.Records[0].Content)
		status, response, _ = performJSON(t, h.Engine, http.MethodPut, fmt.Sprintf("/api/v2/admin/moderation-records/%d/review", queue.Records[0].ID), map[string]any{"verdict": "pass"}, admin.Token)
		require.Equal(t, http.StatusOK, status, response.Message)
		var saved model.User
		require.NoError(t, h.Database.GORM.First(&saved, actor.User.ID).Error)
		require.Equal(t, candidate, saved.Username)
		require.Equal(t, candidate, *queue.Records[0].Content, "manual review must display the text actually submitted")
	})
}

func TestUsernameManualReviewPreservesStateOnFailure(t *testing.T) {
	h := testutil.NewHarness(t)
	admin := h.Fixtures.CreateActor(h.Config, testutil.WithUserRole(model.UserRoleModerator))
	for _, scenario := range []string{"taken", "stale", "deleted", "block", "duplicate", "snapshot_mismatch"} {
		t.Run(scenario, func(t *testing.T) {
			actor := h.Fixtures.CreateActor(h.Config)
			candidate := "try_" + scenario
			h.Moderation.SetDefaultContent(testutil.ContentVerdict(model.ModerationVerdictReview, nil, nil))
			status, response, _ := performJSON(t, h.Engine, http.MethodPut, userPath(actor.User.ID), map[string]any{"username": candidate}, actor.Token)
			require.Equal(t, http.StatusConflict, status, response.Message)
			var machine model.ModerationRecord
			require.NoError(t, h.Database.GORM.Where("user_id = ? AND field = 'name'", actor.User.ID).First(&machine).Error)
			require.Equal(t, candidate, *machine.UsernameCandidate)
			require.EqualValues(t, 0, *machine.UsernameRevision)
			reviewPath := fmt.Sprintf("/api/v2/admin/moderation-records/%d/review", machine.ID)
			expectedName := actor.User.Username
			verdict := "pass"
			expectedStatus := http.StatusConflict
			switch scenario {
			case "taken":
				other := h.Fixtures.CreateActor(h.Config)
				require.NoError(t, h.Database.GORM.Model(&model.User{}).Where("id = ?", other.User.ID).Update("name", candidate).Error)
			case "stale":
				expectedName = "newer_username"
				require.NoError(t, h.Database.GORM.Model(&model.User{}).Where("id = ?", actor.User.ID).Update("name", expectedName).Error)
			case "deleted":
				require.NoError(t, h.Database.GORM.Exec("UPDATE users SET deleted_at = now() WHERE id = ?", actor.User.ID).Error)
				expectedStatus = http.StatusNotFound
			case "block":
				verdict = "block"
				expectedStatus = http.StatusOK
			case "duplicate":
				expectedStatus = http.StatusOK
				expectedName = candidate
			case "snapshot_mismatch":
				changed := "different_candidate"
				forged := machine
				forged.ID = 0
				forged.Provider = model.ModerationProviderManual
				forged.Verdict = model.ModerationVerdictPass
				forged.SupersedesID = &machine.ID
				forged.ReviewerID = &admin.User.ID
				now := time.Now().UTC()
				forged.ReviewedAt = &now
				forged.UsernameCandidate = &changed
				require.True(t, repository.IsCheckViolation(h.Database.GORM.Create(&forged).Error, "mr_username_snapshot_match"))
				// 正确快照仍能通过，约束失败没有留下伪造审核。
				expectedStatus = http.StatusOK
				expectedName = candidate
			}
			status, response, _ = performJSON(t, h.Engine, http.MethodPut, reviewPath, map[string]any{"verdict": verdict}, admin.Token)
			require.Equal(t, expectedStatus, status, response.Message)
			var saved model.User
			require.NoError(t, h.Database.GORM.First(&saved, actor.User.ID).Error)
			require.Equal(t, expectedName, saved.Username)
			var manuals int64
			require.NoError(t, h.Database.GORM.Model(&model.ModerationRecord{}).Where("supersedes_id = ?", machine.ID).Count(&manuals).Error)
			if expectedStatus == http.StatusOK {
				require.EqualValues(t, 1, manuals)
			} else {
				require.Zero(t, manuals)
			}
			if scenario == "duplicate" {
				status, response, _ = performJSON(t, h.Engine, http.MethodPut, reviewPath, map[string]any{"verdict": "pass"}, admin.Token)
				require.Equal(t, http.StatusConflict, status, response.Message)
				var changes int64
				require.NoError(t, h.Database.GORM.Table("user_name_change_records").Where("user_id = ?", actor.User.ID).Count(&changes).Error)
				require.EqualValues(t, 1, changes)
			}
		})
	}
}

func TestUsernameReviewLegacyAndCooldown(t *testing.T) {
	h := testutil.NewHarness(t)
	admin := h.Fixtures.CreateActor(h.Config, testutil.WithUserRole(model.UserRoleModerator))
	for _, legacy := range []bool{true, false} {
		t.Run(fmt.Sprintf("legacy_%t", legacy), func(t *testing.T) {
			actor := h.Fixtures.CreateActor(h.Config)
			current := "cooldown_current"
			revision := uint64(0)
			candidate := "pending_candidate"
			if !legacy {
				require.NoError(t, h.Database.GORM.Model(&model.User{}).Where("id = ?", actor.User.ID).Update("name", current).Error)
				require.NoError(t, h.Database.GORM.Table("user_name_change_records").Select("id").Where("user_id = ?", actor.User.ID).Scan(&revision).Error)
			}
			field := model.ModerationFieldName
			machine := model.ModerationRecord{UserID: &actor.User.ID, Field: &field, Scene: model.ModerationSceneText, Provider: model.ModerationProviderTencentCI, Verdict: model.ModerationVerdictReview, Labels: pq.StringArray{}, CreatedAt: time.Now().UTC()}
			if !legacy {
				machine.UsernameCandidate = &candidate
				machine.UsernameRevision = &revision
			}
			require.NoError(t, h.Database.GORM.Create(&machine).Error)
			path := fmt.Sprintf("/api/v2/admin/moderation-records/%d/review", machine.ID)
			status, response, _ := performJSON(t, h.Engine, http.MethodPut, path, map[string]any{"verdict": "pass"}, admin.Token)
			if legacy {
				require.Equal(t, http.StatusConflict, status, response.Message)
			} else {
				require.Equal(t, http.StatusTooManyRequests, status, response.Message)
			}
			var count int64
			require.NoError(t, h.Database.GORM.Model(&model.ModerationRecord{}).Where("supersedes_id = ?", machine.ID).Count(&count).Error)
			require.Zero(t, count)
			status, response, _ = performJSON(t, h.Engine, http.MethodPut, path, map[string]any{"verdict": "block"}, admin.Token)
			require.Equal(t, http.StatusOK, status, response.Message)
		})
	}
}
