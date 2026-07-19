// Package handler holds Fiber HTTP handlers. Each handler is a thin adapter:
// it parses the request, calls a service method, and writes the response —
// no business logic lives here.
package handler

import "github.com/gofiber/fiber/v2"

type HealthHandler struct{}

func NewHealthHandler() *HealthHandler { return &HealthHandler{} }

func (h *HealthHandler) Ping(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"status":  "UP",
		"service": "credit-report-service",
	})
}
