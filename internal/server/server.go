// Package server wires the Fiber app, its routes, and middleware.
package server

import (
	"html/template"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/swagger"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/config"
	"credit-report-service/internal/handler"
	"credit-report-service/internal/models"
	"credit-report-service/internal/server/middleware"
	"credit-report-service/internal/service"
)

// New assembles the Fiber app. It is configured with the central error handler
// and the multipart body limits from config; all routes are mounted under /api.
func New(
	cfg *config.Config,
	health *handler.HealthHandler,
	auth *handler.AuthHandler,
	analytics *handler.CreditAnalyticsHandler,
	kyc *handler.KycHandler,
	agents *handler.AgentHandler,
	orders *handler.OrderHandler,
	tokens *service.TokenService,
) *fiber.App {
	app := fiber.New(fiber.Config{
		ErrorHandler:          apperr.ErrorHandler,
		ServerHeader:          "credit-report-service",
		DisableStartupMessage: false,
		BodyLimit:             bodyLimitBytes(cfg.Multipart.MaxRequestSize),
	})

	// Request logger runs first so it can time every handler. It logs method,
	// route pattern, status, latency, and account_id — never the body, headers,
	// or query string. See middleware.RequestLogger.
	app.Use(middleware.RequestLogger())

	// CORS: the browser frontend preflights cross-origin requests (OPTIONS);
	// without this every POST from the UI fails with 405.
	app.Use(cors.New(cors.Config{
		AllowOrigins: cfg.Server.CORSOrigins,
		AllowMethods: "GET,POST,PUT,DELETE,OPTIONS",
		AllowHeaders: "Origin, Content-Type, Accept, Authorization",
	}))

	api := app.Group("/api")
	api.Get("/ping", health.Ping)

	// Swagger UI (served from the generated docs/ package). Public so the
	// docs/Authorize button can be reached without a session.
	//
	// The BearerAuth scheme is declared as Swagger 2.0 apiKey (the only option
	// swaggo can emit), so Swagger UI stores whatever is pasted into Authorize
	// verbatim under the Authorization header. RequireAuth expects a "Bearer "
	// prefix; this request interceptor prepends it for the user so they can
	// paste the bare JWT.
	app.Get("/swagger/*", swagger.New(swagger.Config{
		RequestInterceptor: template.JS(`function (req) {
			var v = (req.headers && (req.headers.Authorization || req.headers.authorization));
			if (v && v.indexOf("Bearer ") !== 0 && v.indexOf("bearer ") !== 0) {
				req.headers.Authorization = "Bearer " + v;
			}
			return req;
		}`),
	}))

	// ---- Auth (public) ---------------------------------------------------
	a := api.Group("/auth")
	a.Post("/signup", auth.Signup)
	a.Post("/verify-email", auth.VerifyEmail)
	a.Post("/otp/resend", auth.ResendOTP)
	a.Post("/login", auth.Login)
	a.Post("/google", auth.GoogleLogin)

	// Cashfree server-to-server webhook (public; authenticated by HMAC
	// signature over the raw body, not by a bearer token).
	api.Post("/payments/cashfree/webhook", orders.Webhook)

	// ---- Protected -------------------------------------------------------
	requireAuth := middleware.RequireAuth(tokens)

	profile := api.Group("/profile", requireAuth)
	profile.Get("/", auth.GetProfile)
	profile.Put("/", auth.UpdateProfile)

	// ---- Products & orders (Cashfree) -----------------------------------
	api.Get("/products", requireAuth, orders.ListProducts)

	o := api.Group("/orders", requireAuth)
	o.Post("/", orders.Create)
	o.Get("/", orders.List)
	o.Get("/:orderId", orders.Get)

	// ---- Credit analytics (Digitap proxy) -------------------------------
	ca := api.Group("/credit-analytics", requireAuth)
	ca.Post("/request", analytics.Request)
	ca.Get("/reports", analytics.ListReports)
	ca.Get("/reports/:id<int>", analytics.GetReport)
	ca.Get("/latest-insights", analytics.GetLatestInsights)

	// ---- KYC (PAN submission) -------------------------------------------
	k := api.Group("/kyc", requireAuth)
	k.Post("/pan", kyc.SubmitPAN)

	// ---- Admin (role-gated) ---------------------------------------------
	admin := api.Group("/admin", middleware.RequireRole(tokens, models.RoleAdmin))
	admin.Post("/kyc/pan/:accountId<int>/verify", kyc.VerifyPAN)

	// ---- Admin agent management ----------------------------------------
	adminAgents := admin.Group("/agents")
	adminAgents.Post("/", agents.CreateAgent)
	adminAgents.Put("/:id<int>", agents.UpdateAgent)
	adminAgents.Patch("/:id<int>/status", agents.SetAgentStatus)
	adminAgents.Get("/", agents.ListActiveAgents)
	adminAgents.Get("/:id<int>", agents.GetAgent)
	adminAgents.Put("/account/:accountId<int>/agent-code", agents.UpdateAccountAgentCode)

	// ---- User agent code update -----------------------------------------
	profile.Put("/agent-code", requireAuth, agents.UpdateAgentCode)

	return app
}
