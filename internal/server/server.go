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
	coupons *handler.CouponHandler,
	loans *handler.LoanSwitchHandler,
	scoreBuilder *handler.ScoreBuilderHandler,
	bankStmt *handler.BankStatementHandler,
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

	// Digitap transaction-complete webhook (public; authenticated by the
	// x-digitap-callback-type header and an optional ?secret= guard, not a
	// bearer token). Registered before requireAuth for the same reason.
	api.Post("/bank-statements/digitap/callback", bankStmt.DigitapCallback)

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

	// ---- Coupons ---------------------------------------------------------
	//
	// Issuance is capability-gated (agents and admins); the quote endpoint is
	// open to any signed-in customer, since it is what the payment screen calls
	// when a code is typed in.
	cp := api.Group("/coupons")
	cp.Get("/quote", requireAuth, coupons.QuoteCoupon)
	// Every account has a referral code, not just agents, so this is plain
	// RequireAuth. Registered before "/:code" so it is not swallowed by it.
	cp.Get("/referral", requireAuth, coupons.MyReferralCode)
	cp.Post("/", middleware.RequirePermission(tokens, models.PermCouponCreate), coupons.CreateCoupon)
	cp.Get("/", middleware.RequirePermission(tokens, models.PermCouponManage), coupons.ListCoupons)
	cp.Delete("/:code", middleware.RequirePermission(tokens, models.PermCouponManage), coupons.RevokeCoupon)

	// ---- Credit analytics (Digitap proxy) -------------------------------
	ca := api.Group("/credit-analytics", requireAuth)
	ca.Post("/request", analytics.Request)
	ca.Get("/reports", analytics.ListReports)
	ca.Get("/reports/:id<int>", analytics.GetReport)
	ca.Get("/reports/:id<int>/raw", analytics.GetReportRaw)
	ca.Get("/latest-insights", analytics.GetLatestInsights)
	// What-if simulator: any signed-in user (S29). Reads the caller's own
	// report, so RequireAuth is sufficient.
	ca.Get("/score-simulator", scoreBuilder.Simulate)

	// ---- Bank statement analysis (PDF → salary/EMI/spending) -----------
	//
	// Two initiation paths: /analyze (client uploads a PDF; local parsing) and
	// /digitap/initiate (redirect to Digitap's UI; we store their report). Both
	// stay available regardless of statement.provider — the client picks.
	// GET /:id is what a client polls until status flips to 'completed'/'failed'.
	bs := api.Group("/bank-statements", requireAuth)
	bs.Post("/analyze", bankStmt.Analyze)
	bs.Post("/digitap/initiate", bankStmt.InitiateDigitap)
	bs.Get("/", bankStmt.List)
	bs.Get("/:id<int>", bankStmt.Get)
	bs.Get("/:id<int>/raw", bankStmt.GetRaw)
	bs.Get("/latest", bankStmt.GetLatest)

	// ---- KYC (PAN submission) -------------------------------------------
	k := api.Group("/kyc", requireAuth)
	k.Post("/pan", kyc.SubmitPAN)

	// ---- Loan switch (interest optimizer) -------------------------------
	//
	// The savings view is for any signed-in user; provider CRUD and the switch
	// settings are admin-only, gated on the 'loan-provider:manage' permission.
	api.Get("/loan-switch/opportunities", requireAuth, loans.GetOpportunities)

	lp := api.Group("/admin/loan-providers", middleware.RequirePermission(tokens, models.PermLoanProviderManage))
	lp.Post("/", loans.CreateProvider)
	lp.Get("/", loans.ListProviders)
	lp.Get("/:id<int>", loans.GetProvider)
	lp.Put("/:id<int>", loans.UpdateProvider)
	lp.Delete("/:id<int>", loans.DeleteProvider)

	ls := api.Group("/admin/loan-switch", middleware.RequirePermission(tokens, models.PermLoanProviderManage))
	ls.Get("/settings", loans.GetSettings)
	ls.Put("/settings", loans.UpdateSettings)

	// ---- Score builder (S28 bank offerings) -----------------------------
	//
	// The toolkit view rides on the insights response (no separate user route);
	// bank-offering CRUD is admin-only, gated on 'bank-offering:manage'.
	bo := api.Group("/admin/bank-offerings", middleware.RequirePermission(tokens, models.PermBankOfferingManage))
	bo.Post("/", scoreBuilder.CreateOffering)
	bo.Get("/", scoreBuilder.ListOfferings)
	bo.Get("/:id<int>", scoreBuilder.GetOffering)
	bo.Put("/:id<int>", scoreBuilder.UpdateOffering)
	bo.Delete("/:id<int>", scoreBuilder.DeleteOffering)

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
