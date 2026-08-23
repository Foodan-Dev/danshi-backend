package router

import (
	"github.com/cloudwego/hertz/pkg/route"

	"github.com/jingyijun/danshi_backend_go/internal/handler"
	"github.com/jingyijun/danshi_backend_go/internal/middleware"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

func registerSearch(api *route.RouterGroup, deps Deps) {
	searchHandler := handler.NewSearch(service.NewSearchService())
	authService := service.NewAuthService(deps.Config, service.UnavailableVerificationEmailSender{})
	requireAuth := middleware.RequireAuth(authService)
	search := api.Group("/search")

	search.GET("/posts", requireAuth, searchHandler.Posts)
	search.GET("/users", requireAuth, searchHandler.Users)
}
