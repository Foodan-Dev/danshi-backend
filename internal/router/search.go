package router

import (
	"github.com/cloudwego/hertz/pkg/route"

	"github.com/Foodan-Dev/danshi-backend/internal/handler"
	"github.com/Foodan-Dev/danshi-backend/internal/middleware"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

func registerSearch(api *route.RouterGroup, deps Deps) {
	searchHandler := handler.NewSearch(service.NewSearchService())
	authService := service.NewAuthService(deps.Config, service.UnavailableVerificationEmailSender{})
	requireAuth := middleware.RequireAuth(authService)
	search := api.Group("/search")

	search.GET("/posts", requireAuth, searchHandler.Posts)
	search.GET("/users", requireAuth, searchHandler.Users)
}
