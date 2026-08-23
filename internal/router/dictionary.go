package router

import (
	"github.com/cloudwego/hertz/pkg/route"

	"github.com/Foodan-Dev/danshi-backend/internal/authz"
	"github.com/Foodan-Dev/danshi-backend/internal/handler"
	"github.com/Foodan-Dev/danshi-backend/internal/middleware"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

func registerDictionary(api *route.RouterGroup, deps Deps) {
	dictionaryHandler := handler.NewDictionary(service.NewDictionaryService())
	authService := service.NewAuthService(deps.Config, service.UnavailableVerificationEmailSender{})
	requireAuth := middleware.RequireAuth(authService)
	requireSuggestionReview := middleware.RequireCapability(authz.CapReviewDictionarySuggestion)
	requireDictionaryManage := middleware.RequireCapability(authz.CapManageDictionary)

	suggestions := api.Group("/dictionary-suggestions")
	suggestions.POST("", requireAuth, dictionaryHandler.CreateSuggestion)
	suggestions.GET("/mine", requireAuth, dictionaryHandler.Mine)

	admin := api.Group("/admin")
	admin.GET("/dictionary-suggestions", requireAuth, requireSuggestionReview, dictionaryHandler.Pending)
	admin.POST(
		"/dictionary-suggestions/:suggestion_id/approve",
		requireAuth,
		requireSuggestionReview,
		dictionaryHandler.Approve,
	)
	admin.POST(
		"/dictionary-suggestions/:suggestion_id/reject",
		requireAuth,
		requireSuggestionReview,
		dictionaryHandler.Reject,
	)

	admin.POST("/flavors", requireAuth, requireDictionaryManage, dictionaryHandler.CreateFlavor)
	admin.PATCH("/flavors/:flavor_id", requireAuth, requireDictionaryManage, dictionaryHandler.UpdateFlavor)
	admin.DELETE("/flavors/:flavor_id", requireAuth, requireDictionaryManage, dictionaryHandler.DeleteFlavor)

	admin.POST("/cuisines", requireAuth, requireDictionaryManage, dictionaryHandler.CreateCuisine)
	admin.PATCH("/cuisines/:cuisine_id", requireAuth, requireDictionaryManage, dictionaryHandler.UpdateCuisine)
	admin.DELETE("/cuisines/:cuisine_id", requireAuth, requireDictionaryManage, dictionaryHandler.DeleteCuisine)

	admin.POST("/canteens", requireAuth, requireDictionaryManage, dictionaryHandler.CreateCanteen)
	admin.PATCH("/canteens/:canteen_id", requireAuth, requireDictionaryManage, dictionaryHandler.UpdateCanteen)
	admin.DELETE("/canteens/:canteen_id", requireAuth, requireDictionaryManage, dictionaryHandler.DeleteCanteen)
	admin.POST("/canteens/:canteen_id/windows", requireAuth, requireDictionaryManage, dictionaryHandler.CreateWindow)
	admin.PATCH(
		"/canteen-windows/:window_id", requireAuth, requireDictionaryManage, dictionaryHandler.UpdateWindow,
	)
	admin.DELETE(
		"/canteen-windows/:window_id", requireAuth, requireDictionaryManage, dictionaryHandler.DeleteWindow,
	)
}
