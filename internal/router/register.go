// Package router 装配路由与中间件。
//
// 分组形态借鉴了 hz 脚手架（评估结论见 docs/go-rewrite-plan.md §2：
// 借它的路由分层形态，但不用它的代码生成）：
//
//	全局中间件 → /api/v2 分组中间件 → 各业务域分组 → 单路由鉴权
//
// **路径前缀是 /api/v2**（D29）。/api/v1 由旧 Python 服务继续提供，
// 网关按路径分流，两套服务并行，旧库置只读。
package router

import (
	"context"
	"log/slog"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/hertz-contrib/cors"

	"github.com/jingyijun/danshi_backend_go/internal/apierr"
	"github.com/jingyijun/danshi_backend_go/internal/config"
	"github.com/jingyijun/danshi_backend_go/internal/infra/db"
	"github.com/jingyijun/danshi_backend_go/internal/pkg/envelope"
	"github.com/jingyijun/danshi_backend_go/internal/router/middleware"
	"github.com/jingyijun/danshi_backend_go/internal/service"
)

// APIPrefix 是本服务唯一的路由前缀。
const APIPrefix = "/api/v2"

// Deps 是路由装配需要的依赖。业务域的 handler 在这里注入。
type Deps struct {
	Config config.Config
	DB     *db.DB
	Log    *slog.Logger
	// EmailSender 可由测试或生产适配器替换；nil 时使用 dev 日志实现。
	EmailSender service.VerificationEmailSender
	// ContentModerator 可由测试或生产适配器替换；dev 默认直接放行，prod 未配置时 fail-closed。
	ContentModerator service.ContentModerator
}

// Register 装配全部路由。
//
// 中间件顺序是有讲究的，改之前先想清楚：
//  1. Recovery     最外层，要能兜住后面所有中间件里的 panic
//  2. RequestID    尽早分配，后续日志才带得上
//  3. ErrorHandler 在 UoW 之外——UoW 要能看到 abort 状态决定回滚
//  4. CORS         在业务之前，预检请求不该进到 UoW
//  5. UnitOfWork   只包业务路由，不包探针（探针不该开事务）
func Register(h *server.Hertz, d Deps) {
	h.Use(middleware.Recovery(d.Log))
	h.Use(middleware.RequestID())
	h.Use(middleware.ErrorHandler(d.Log))

	registerProbes(h, d)

	api := h.Group(APIPrefix)
	api.Use(corsMiddleware(d.Config))
	api.Use(middleware.UnitOfWork(d.DB, d.Log))

	// —— 业务域在此挂载 ——
	// 每个域一个 register 函数，签名统一为 func(*route.RouterGroup, Deps)。
	// 由 P2 逐域补全，参见 docs/go-rewrite-plan.md §12 的 P2 表。
	//
	registerAuth(api, d)
	//	registerUser(api, d)
	registerPost(api, d)
	registerComment(api, d)
	//	registerNotification(api, d)
	//	registerSearch(api, d)
	//	registerUpload(api, d)
	//	registerConfig(api, d)
	//	registerDictionary(api, d)
	//	registerModeration(api, d)
	//	registerAdmin(api, d)

	registerFallbacks(h)
}

func corsMiddleware(cfg config.Config) app.HandlerFunc {
	origins := cfg.CORSOrigins()
	if len(origins) == 0 {
		origins = []string{"*"}
	}
	return cors.New(cors.Config{
		AllowOrigins:     origins,
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", middleware.HeaderRequestID},
		ExposeHeaders:    []string{middleware.HeaderRequestID},
		AllowCredentials: cfg.CORSAllowCredentials,
	})
}

// registerFallbacks 让 404 / 405 也走统一错误体。
// Hertz 默认返回纯文本，那会让前端的错误处理出现一个不走 envelope 的分支。
func registerFallbacks(h *server.Hertz) {
	h.NoRoute(func(_ context.Context, c *app.RequestContext) {
		status, body := envelope.FromError(apierr.NotFound(apierr.BizNotFound, "接口"))
		c.JSON(status, body)
	})
	h.NoMethod(func(_ context.Context, c *app.RequestContext) {
		e := &apierr.Error{
			Status:  consts.StatusMethodNotAllowed,
			Code:    apierr.BizMethodNotAllowed,
			Message: "请求方法不被允许",
		}
		status, body := envelope.FromError(e)
		c.JSON(status, body)
	})
}
