package router

import (
	"github.com/cloudwego/hertz/pkg/route"

	"github.com/Foodan-Dev/danshi-backend/internal/authz"
	"github.com/Foodan-Dev/danshi-backend/internal/handler"
	"github.com/Foodan-Dev/danshi-backend/internal/middleware"
	"github.com/Foodan-Dev/danshi-backend/internal/service"
)

func registerDictionary(api *route.RouterGroup, deps Deps) {
	dictionaryHandler := handler.NewDictionary(
		service.NewDictionaryServiceWithCursorSecret(deps.Config.JWTSecretKey),
	)
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

	admin.GET("/flavors", requireAuth, requireDictionaryManage, dictionaryHandler.Flavors)
	admin.POST("/flavors", requireAuth, requireDictionaryManage, dictionaryHandler.CreateFlavor)
	admin.PATCH("/flavors/:flavor_id", requireAuth, requireDictionaryManage, dictionaryHandler.UpdateFlavor)
	admin.POST("/flavors/:flavor_id/enable", requireAuth, requireDictionaryManage, dictionaryHandler.EnableFlavor)
	admin.DELETE("/flavors/:flavor_id", requireAuth, requireDictionaryManage, dictionaryHandler.DeleteFlavor)

	admin.GET("/cuisines", requireAuth, requireDictionaryManage, dictionaryHandler.Cuisines)
	admin.POST("/cuisines", requireAuth, requireDictionaryManage, dictionaryHandler.CreateCuisine)
	admin.PATCH("/cuisines/:cuisine_id", requireAuth, requireDictionaryManage, dictionaryHandler.UpdateCuisine)
	admin.POST("/cuisines/:cuisine_id/enable", requireAuth, requireDictionaryManage, dictionaryHandler.EnableCuisine)
	admin.DELETE("/cuisines/:cuisine_id", requireAuth, requireDictionaryManage, dictionaryHandler.DeleteCuisine)

	admin.GET("/canteens", requireAuth, requireDictionaryManage, dictionaryHandler.Canteens)
	admin.POST("/canteens", requireAuth, requireDictionaryManage, dictionaryHandler.CreateCanteen)
	admin.PATCH("/canteens/:canteen_id", requireAuth, requireDictionaryManage, dictionaryHandler.UpdateCanteen)
	admin.POST("/canteens/:canteen_id/enable", requireAuth, requireDictionaryManage, dictionaryHandler.EnableCanteen)
	admin.DELETE("/canteens/:canteen_id", requireAuth, requireDictionaryManage, dictionaryHandler.DeleteCanteen)
	admin.POST("/canteens/:canteen_id/windows", requireAuth, requireDictionaryManage, dictionaryHandler.CreateWindow)
	admin.GET("/canteen-windows", requireAuth, requireDictionaryManage, dictionaryHandler.Windows)
	admin.PATCH(
		"/canteen-windows/:window_id", requireAuth, requireDictionaryManage, dictionaryHandler.UpdateWindow,
	)
	admin.POST(
		"/canteen-windows/:window_id/enable", requireAuth, requireDictionaryManage, dictionaryHandler.EnableWindow,
	)
	admin.DELETE(
		"/canteen-windows/:window_id", requireAuth, requireDictionaryManage, dictionaryHandler.DeleteWindow,
	)
}
