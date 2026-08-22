package router

import (
	"github.com/cloudwego/hertz/pkg/route"

	"github.com/jingyijun/danshi_backend_go/internal/handler"
	"github.com/jingyijun/danshi_backend_go/internal/router/middleware"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

func registerDictionary(api *route.RouterGroup, deps Deps) {
	dictionaryHandler := handler.NewDictionary(service.NewDictionaryService())
	authService := service.NewAuthService(deps.Config, service.UnavailableVerificationEmailSender{})
	requireAuth := middleware.RequireAuth(authService)
	requireAdmin := middleware.RequireAdmin()

	suggestions := api.Group("/dictionary-suggestions")
	suggestions.POST("", requireAuth, dictionaryHandler.CreateSuggestion)
	suggestions.GET("/mine", requireAuth, dictionaryHandler.Mine)

	admin := api.Group("/admin")
	admin.GET("/dictionary-suggestions", requireAuth, requireAdmin, dictionaryHandler.Pending)
	admin.POST(
		"/dictionary-suggestions/:suggestion_id/approve",
		requireAuth,
		requireAdmin,
		dictionaryHandler.Approve,
	)
	admin.POST(
		"/dictionary-suggestions/:suggestion_id/reject",
		requireAuth,
		requireAdmin,
		dictionaryHandler.Reject,
	)

	admin.POST("/flavors", requireAuth, requireAdmin, dictionaryHandler.CreateFlavor)
	admin.PATCH("/flavors/:flavor_id", requireAuth, requireAdmin, dictionaryHandler.UpdateFlavor)
	admin.DELETE("/flavors/:flavor_id", requireAuth, requireAdmin, dictionaryHandler.DeleteFlavor)

	admin.POST("/cuisines", requireAuth, requireAdmin, dictionaryHandler.CreateCuisine)
	admin.PATCH("/cuisines/:cuisine_id", requireAuth, requireAdmin, dictionaryHandler.UpdateCuisine)
	admin.DELETE("/cuisines/:cuisine_id", requireAuth, requireAdmin, dictionaryHandler.DeleteCuisine)

	admin.POST("/canteens", requireAuth, requireAdmin, dictionaryHandler.CreateCanteen)
	admin.PATCH("/canteens/:canteen_id", requireAuth, requireAdmin, dictionaryHandler.UpdateCanteen)
	admin.DELETE("/canteens/:canteen_id", requireAuth, requireAdmin, dictionaryHandler.DeleteCanteen)
	admin.POST("/canteens/:canteen_id/windows", requireAuth, requireAdmin, dictionaryHandler.CreateWindow)
	admin.PATCH(
		"/canteen-windows/:window_id", requireAuth, requireAdmin, dictionaryHandler.UpdateWindow,
	)
	admin.DELETE(
		"/canteen-windows/:window_id", requireAuth, requireAdmin, dictionaryHandler.DeleteWindow,
	)
}
