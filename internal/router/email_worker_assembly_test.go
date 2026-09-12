package router_test

import (
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/stretchr/testify/require"

	"github.com/Foodan-Dev/danshi-backend/internal/router"
	"github.com/Foodan-Dev/danshi-backend/internal/testutil"
)

func TestMissingEmailWorkerFailsClosed(t *testing.T) {
	h := testutil.NewHarness(t)
	engine := server.New()
	router.Register(engine, router.Deps{Config: h.Config, DB: h.Database.DB, EmailSender: h.Email, ContentModerator: h.Moderation, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	status, response, _ := performJSON(t, engine, http.MethodPost, "/api/v2/auth/email-verification-codes", map[string]any{"email": "no_worker@fdueat.com"}, "")
	require.Equal(t, http.StatusServiceUnavailable, status, response.Message)
	var deliveries int64
	require.NoError(t, h.Database.GORM.Table("verification_email_deliveries").Count(&deliveries).Error)
	require.Zero(t, deliveries)
}
