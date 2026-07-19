// Package server wires the Fiber app, its routes, and middleware.
package server

import (
	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/config"
	"credit-report-service/internal/handler"
)

// New assembles the Fiber app. It is configured with the central error handler
// and the multipart body limits from config; all routes are mounted under /api.
func New(
	cfg *config.Config,
	health *handler.HealthHandler,
	credit *handler.CreditReportHandler,
	registration *handler.RegistrationHandler,
) *fiber.App {
	app := fiber.New(fiber.Config{
		ErrorHandler:          apperr.ErrorHandler,
		ServerHeader:          "credit-report-service",
		DisableStartupMessage: false,
		BodyLimit:             bodyLimitBytes(cfg.Multipart.MaxRequestSize),
	})

	api := app.Group("/api")
	api.Get("/ping", health.Ping)

	cr := api.Group("/credit-reports")
	cr.Get("/", credit.List)
	cr.Get("/:id<int>", credit.Get)
	cr.Get("/by-subject/:subjectId", credit.GetBySubject)
	cr.Post("/", credit.Create)
	cr.Delete("/:id<int>", credit.Delete)

	reg := api.Group("/registration")
	reg.Post("/otp/send", registration.SendOTP)
	reg.Post("/otp/verify", registration.VerifyOTP)
	reg.Post("/pan", registration.SubmitPAN)

	return app
}
