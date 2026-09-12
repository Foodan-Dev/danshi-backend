package router_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestUsernameCooldownAgainstPostgres(t *testing.T) {
	gdb, database := openAuthPostgres(t)
	cfg := authTestConfig()
	fixtures := testutil.NewFixtures(t, gdb)
	moderator := testutil.NewMockModeration()
	engine := newUserTestEngine(t, cfg, database, newCaptureEmailSender(), moderator)
	actor := fixtures.CreateActor(cfg)
	var count int64
	require.NoError(t, gdb.Model(&model.UsernameChangeRecord{}).Where("user_id = ?", actor.User.ID).Count(&count).Error)
	require.Zero(t, count, "注册不计入改名额度")
	status, response, _ := performJSON(t, engine, http.MethodPut, userPath(actor.User.ID), map[string]any{"username": "cooldown_first"}, actor.Token)
	require.Equal(t, http.StatusOK, status, response.Message)
	status, response, _ = performJSON(t, engine, http.MethodPut, userPath(actor.User.ID), map[string]any{"username": "cooldown_first", "bio": "仍能更新简介"}, actor.Token)
	require.Equal(t, http.StatusOK, status, response.Message)
	status, response, _ = performJSON(t, engine, http.MethodPut, userPath(actor.User.ID), map[string]any{"username": "cooldown_second", "bio": "不应保存"}, actor.Token)
	require.Equal(t, http.StatusTooManyRequests, status, response.Message)
	require.Equal(t, apierr.BizUsernameChangeLimited, response.ErrorCode)
	var saved model.User
	require.NoError(t, gdb.First(&saved, actor.User.ID).Error)
	require.Equal(t, "cooldown_first", saved.Username)
	require.NotNil(t, saved.Bio)
	require.Equal(t, "仍能更新简介", *saved.Bio)
	require.NoError(t, gdb.Model(&model.UsernameChangeRecord{}).Where("user_id = ?", actor.User.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)

	for _, test := range []struct {
		name, age string
		status    int
	}{
		{"before_30_days", "719 hours", http.StatusTooManyRequests},
		{"after_30_days", "720 hours", http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			previous := fixtures.CreateActor(cfg)
			require.NoError(t, gdb.Exec(`INSERT INTO user_name_change_records (user_id,old_name,new_name,changed_at)
    VALUES (?, 'before_previous', ?, now() - ?::interval)`, previous.User.ID, previous.User.Username, test.age).Error)
			status, response, _ := performJSON(t, engine, http.MethodPut, userPath(previous.User.ID), map[string]any{"username": "after_cooldown"}, previous.Token)
			require.Equal(t, test.status, status, response.Message)
			if test.status == http.StatusTooManyRequests {
				require.Equal(t, apierr.BizUsernameChangeLimited, response.ErrorCode)
			}
		})
	}
	t.Run("database allows exactly 720 hours and rejects less", func(t *testing.T) {
		previous := fixtures.CreateActor(cfg)
		// 同一 SQL 语句中的 now() 相同，精确检验 720 小时开区间边界。
		require.NoError(t, gdb.Exec(`INSERT INTO user_name_change_records (user_id,old_name,new_name,changed_at)
    VALUES (?, 'boundary_old', 'boundary_mid', now() - interval '720 hours'),
           (?, 'boundary_mid', 'boundary_new', now())`, previous.User.ID, previous.User.ID).Error)
		err := gdb.Exec(`INSERT INTO user_name_change_records (user_id,old_name,new_name,changed_at)
    SELECT user_id,'boundary_new','boundary_fail',max(changed_at) + interval '720 hours' - interval '1 microsecond'
    FROM user_name_change_records WHERE user_id=? GROUP BY user_id`, previous.User.ID).Error
		require.Error(t, err)
	})
	t.Run("failed review and conflict do not consume quota", func(t *testing.T) {
		fresh := fixtures.CreateActor(cfg)
		moderator.SetDefaultContent(testutil.ContentVerdict(model.ModerationVerdictReview, nil, nil))
		status, _, _ := performJSON(t, engine, http.MethodPut, userPath(fresh.User.ID), map[string]any{"username": "review_candidate"}, fresh.Token)
		require.NotEqual(t, http.StatusOK, status)
		moderator.SetDefaultContent(testutil.ContentVerdict(model.ModerationVerdictPass, nil, nil))
		status, response, _ := performJSON(t, engine, http.MethodPut, userPath(fresh.User.ID), map[string]any{"username": "cooldown_first"}, fresh.Token)
		require.Equal(t, http.StatusConflict, status, response.Message)
		require.Equal(t, apierr.BizUsernameTaken, response.ErrorCode)
		status, response, _ = performJSON(t, engine, http.MethodPut, userPath(fresh.User.ID), map[string]any{"username": "after_failures"}, fresh.Token)
		require.Equal(t, http.StatusOK, status, response.Message)
	})
	t.Run("concurrent renames have exactly one winner", func(t *testing.T) {
		fresh := fixtures.CreateActor(cfg)
		release := make(chan struct{})
		outcome := testutil.ContentVerdict(model.ModerationVerdictPass, nil, nil)
		outcome.Release = release
		blocked := testutil.NewMockModeration()
		blocked.SetDefaultContent(outcome)
		concurrent := newUserTestEngine(t, cfg, database, newCaptureEmailSender(), blocked)
		results := make(chan asyncRequestResult, 2)
		for _, name := range []string{"concurrent_one", "concurrent_two"} {
			go func() {
				status, response, raw, err := performJSONRequest(concurrent, http.MethodPut, userPath(fresh.User.ID), map[string]any{"username": name}, fresh.Token)
				results <- asyncRequestResult{status: status, response: response, raw: raw, err: err}
			}()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		entered := blocked.WaitForContentCalls(ctx, 1)
		close(release)
		require.True(t, entered)
		counts := map[int]int{}
		for range 2 {
			select {
			case r := <-results:
				require.NoError(t, r.err)
				counts[r.status]++
				if r.status == http.StatusTooManyRequests {
					require.Equal(t, apierr.BizUsernameChangeLimited, r.response.ErrorCode)
				}
			case <-ctx.Done():
				t.Fatal("并发改名未完成")
			}
		}
		require.Equal(t, map[int]int{http.StatusOK: 1, http.StatusTooManyRequests: 1}, counts)
		blocked.RequireContentCalls(t, 1)
		require.NoError(t, gdb.Model(&model.UsernameChangeRecord{}).Where("user_id = ?", fresh.User.ID).Count(&count).Error)
		require.EqualValues(t, 1, count)
	})
}
