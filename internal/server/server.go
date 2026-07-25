// Package server wires the Fiber app, its routes, and middleware.
package server

import (
	"html/template"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/swagger"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/config"
	"credit-report-service/internal/handler"
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
	tokens *service.TokenService,
) *fiber.App {
	app := fiber.New(fiber.Config{
		ErrorHandler:          apperr.ErrorHandler,
		ServerHeader:          "credit-report-service",
		DisableStartupMessage: false,
		BodyLimit:             bodyLimitBytes(cfg.Multipart.MaxRequestSize),
	})

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

	// ---- Protected -------------------------------------------------------
	requireAuth := middleware.RequireAuth(tokens)

	profile := api.Group("/profile", requireAuth)
	profile.Get("/", auth.GetProfile)
	profile.Put("/", auth.UpdateProfile)

	// ---- Credit analytics (Digitap proxy) -------------------------------
	ca := api.Group("/credit-analytics", requireAuth)
	ca.Post("/request", analytics.Request)

	// ---- KYC (PAN submission) -------------------------------------------
	k := api.Group("/kyc", requireAuth)
	k.Post("/pan", kyc.SubmitPAN)

	return app
}
