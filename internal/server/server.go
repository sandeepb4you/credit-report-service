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
	orders *handler.OrderHandler,
	tokens *service.TokenService,
) *fiber.App {
	// Client IP resolution. X-Forwarded-For is only believed when the immediate
	// peer is a configured trusted proxy — otherwise any caller could forge the
	// IP recorded against their session. With no proxies configured the header
	// is ignored entirely and c.IP() stays the socket peer.
	trustProxies := len(cfg.Server.TrustedProxies) > 0
	proxyHeader := ""
	if trustProxies {
		proxyHeader = fiber.HeaderXForwardedFor
	}

	app := fiber.New(fiber.Config{
		ErrorHandler:            apperr.ErrorHandler,
		ServerHeader:            "credit-report-service",
		DisableStartupMessage:   false,
		BodyLimit:               bodyLimitBytes(cfg.Multipart.MaxRequestSize),
		EnableTrustedProxyCheck: trustProxies,
		TrustedProxies:          cfg.Server.TrustedProxies,
		ProxyHeader:             proxyHeader,
	})

	// Request logger runs first so it can time every handler. It logs method,
	// route pattern, status, latency, and account_id — never the body, headers,
	// or query string. See middleware.RequestLogger.
	app.Use(middleware.RequestLogger())

	// CORS: the browser frontend preflights cross-origin requests (OPTIONS);
	// without this every POST from the UI fails with 405.
	//
	// AllowCredentials is required for the web refresh cookie to be sent and
	// stored cross-origin, but the spec forbids pairing it with a wildcard
	// origin — so it is only enabled once cors-origins names real origins.
	// Leaving cors-origins at "*" silently disables cookie-based refresh for
	// browsers; set it explicitly in any environment with a web frontend.
	allowCredentials := cfg.Server.CORSOrigins != "*"
	app.Use(cors.New(cors.Config{
		AllowOrigins:     cfg.Server.CORSOrigins,
		AllowMethods:     "GET,POST,PUT,PATCH,DELETE,OPTIONS",
		AllowHeaders:     "Origin, Content-Type, Accept, Authorization, X-Device-Id, X-Device-Name, X-Device-Platform, X-Device-Info",
		AllowCredentials: allowCredentials,
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
	// Refresh is public: the refresh token (body for mobile, httpOnly cookie
	// for web) is itself the credential, and the expired access token that
	// prompted the call would fail RequireAuth.
	a.Post("/refresh", auth.Refresh)

	// Cashfree server-to-server webhook (public; authenticated by HMAC
	// signature over the raw body, not by a bearer token).
	api.Post("/payments/cashfree/webhook", orders.Webhook)

	// ---- Protected -------------------------------------------------------
	requireAuth := middleware.RequireAuth(tokens)

	// Sessions / signed-in devices. Mounted on the same /auth group but behind
	// RequireAuth, unlike the public flows above.
	a.Post("/logout", requireAuth, auth.Logout)
	a.Get("/sessions", requireAuth, auth.ListSessions)
	a.Delete("/sessions", requireAuth, auth.RevokeOtherSessions)
	a.Delete("/sessions/:id<int>", requireAuth, auth.RevokeSession)

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

	// ---- Admin (permission-gated) ----------------------------------------
	//
	// Each route declares the capability it needs rather than a role name, so
	// re-scoping a role or adding one never touches this block. The group
	// itself is gated on the weakest permission any member needs; individual
	// routes tighten it where they need more.
	admin := api.Group("/admin", middleware.RequirePermission(tokens, models.PermKycVerify))
	admin.Post("/kyc/pan/:accountId<int>/verify", kyc.VerifyPAN)
	admin.Put("/accounts/:accountId<int>/role",
		middleware.RequirePermission(tokens, models.PermAccountSetRole), auth.SetAccountRole)

	return app
}
