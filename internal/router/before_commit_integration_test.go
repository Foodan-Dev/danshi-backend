package router_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/apierr"
	"github.com/Foodan-Dev/danshi-backend/internal/httpx"
	"github.com/Foodan-Dev/danshi-backend/internal/infra/db"
	"github.com/Foodan-Dev/danshi-backend/internal/middleware"
	"github.com/Foodan-Dev/danshi-backend/internal/model"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestBeforeCommitWritesShareHTTPTransaction(t *testing.T) {
	database := testutil.OpenPostgres(t)
	for _, outcome := range []string{"success", "handler_failure", "callback_failure"} {
		t.Run(outcome, func(t *testing.T) {
			engine := server.New()
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			engine.Use(middleware.ErrorHandler(log), middleware.UnitOfWork(database.DB, log))
			callbackRan, afterCommitRan := false, false
			email := outcome + "@fdueat.com"
			engine.POST("/before-commit", func(ctx context.Context, c *app.RequestContext) {
				user := model.User{Email: email, Username: outcome, PasswordHash: "test"}
				if err := db.FromContext(ctx).Create(&user).Error; err != nil {
					httpx.Fail(ctx, c, err)
					return
				}
				err := db.BeforeCommit(ctx, func(txCtx context.Context) error {
					callbackRan = true
					if err := db.FromContext(txCtx).Model(&model.User{}).Where("id = ?", user.ID).UpdateColumn("gender", "other").Error; err != nil {
						return err
					}
					if outcome == "callback_failure" {
						return errors.New("forced before-commit failure")
					}
					return nil
				})
				if err != nil {
					httpx.Fail(ctx, c, err)
					return
				}
				db.AfterCommit(ctx, func(context.Context) { afterCommitRan = true })
				if outcome == "handler_failure" {
					httpx.Fail(ctx, c, apierr.BadRequest(apierr.BizVerifyCodeInvalid, "forced handler failure"))
					return
				}
				c.JSON(http.StatusOK, map[string]any{"code": 0, "message": "ok"})
			})
			status, _, _ := performJSON(t, engine, http.MethodPost, "/before-commit", nil, "")
			var users []model.User
			require.NoError(t, database.GORM.Where("email = ?", email).Find(&users).Error)
			switch outcome {
			case "success":
				require.Equal(t, http.StatusOK, status)
				require.True(t, callbackRan)
				require.True(t, afterCommitRan)
				require.Len(t, users, 1)
				require.Equal(t, model.GenderOther, *users[0].Gender)
			case "handler_failure":
				require.Equal(t, http.StatusBadRequest, status)
				require.False(t, callbackRan)
				require.False(t, afterCommitRan)
				require.Empty(t, users)
			case "callback_failure":
				require.Equal(t, http.StatusInternalServerError, status)
				require.True(t, callbackRan)
				require.False(t, afterCommitRan)
				require.Empty(t, users)
			}
		})
	}
}
