// Package server wires the Fiber app, its routes, and middleware.
package server

import (
	"github.com/gofiber/fiber/v2"

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
	credit *handler.CreditReportHandler,
	auth *handler.AuthHandler,
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

	cr := api.Group("/credit-reports", requireAuth)
	cr.Get("/", credit.List)
	cr.Get("/:id<int>", credit.Get)
	cr.Get("/by-subject/:subjectId", credit.GetBySubject)
	cr.Post("/", credit.Create)
	cr.Delete("/:id<int>", credit.Delete)

	return app
}
