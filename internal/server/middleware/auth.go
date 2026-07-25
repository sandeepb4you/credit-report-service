// Package middleware holds Fiber middleware for the HTTP layer.
package middleware

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/service"
)

// Locals keys for the authenticated account id and role set by RequireAuth.
const (
	accountIDKey   = "accountID"
	accountRoleKey = "accountRole"
)

// RequireAuth returns middleware that validates the Bearer JWT and stores the
// account id and role in c.Locals for downstream handlers. Missing/invalid
// tokens yield a 401 via the central error handler.
func RequireAuth(tokens *service.TokenService) fiber.Handler {
	return func(c *fiber.Ctx) error {
		accountID, role, err := parseBearer(c, tokens)
		if err != nil {
			return err
		}
		c.Locals(accountIDKey, accountID)
		c.Locals(accountRoleKey, role)
		return c.Next()
	}
}

// RequireRole is RequireAuth plus a role check. A valid but wrong-role token
// yields 403; a missing/invalid token yields 401 (from parseBearer).
func RequireRole(tokens *service.TokenService, role string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		accountID, got, err := parseBearer(c, tokens)
		if err != nil {
			return err
		}
		if got != role {
			return apperr.NewForbidden("This resource requires the '" + role + "' role")
		}
		c.Locals(accountIDKey, accountID)
		c.Locals(accountRoleKey, got)
		return c.Next()
	}
}

// parseBearer extracts and validates the Bearer JWT from the Authorization
// header, returning the account id and role.
func parseBearer(c *fiber.Ctx, tokens *service.TokenService) (int64, string, error) {
	h := c.Get("Authorization")
	if h == "" {
		return 0, "", apperr.NewUnauthorized("Authorization header required")
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		return 0, "", apperr.NewUnauthorized("Authorization header must be 'Bearer <token>'")
	}
	id, role, err := tokens.Parse(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, "", err
	}
	return id, role, nil
}

// AccountID reads the authenticated account id set by RequireAuth/RequireRole.
// The bool is false when the request did not pass through either middleware.
func AccountID(c *fiber.Ctx) (int64, bool) {
	v, ok := c.Locals(accountIDKey).(int64)
	return v, ok
}

// AccountRole reads the authenticated account role set by RequireAuth/RequireRole.
// The bool is false when the request did not pass through either middleware.
func AccountRole(c *fiber.Ctx) (string, bool) {
	v, ok := c.Locals(accountRoleKey).(string)
	return v, ok
}
