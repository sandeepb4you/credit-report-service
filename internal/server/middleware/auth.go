// Package middleware holds Fiber middleware for the HTTP layer.
package middleware

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/service"
)

// accountIDKey is the c.Locals key holding the authenticated account id.
const accountIDKey = "accountID"

// RequireAuth returns middleware that validates the Bearer JWT and stores the
// account id in c.Locals for downstream handlers. Missing/invalid tokens yield
// a 401 via the central error handler.
func RequireAuth(tokens *service.TokenService) fiber.Handler {
	return func(c *fiber.Ctx) error {
		h := c.Get("Authorization")
		if h == "" {
			return apperr.NewUnauthorized("Authorization header required")
		}
		parts := strings.SplitN(h, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
			return apperr.NewUnauthorized("Authorization header must be 'Bearer <token>'")
		}
		accountID, err := tokens.Parse(strings.TrimSpace(parts[1]))
		if err != nil {
			return err
		}
		c.Locals(accountIDKey, accountID)
		return c.Next()
	}
}

// AccountID reads the authenticated account id set by RequireAuth. The bool is
// false when the request did not pass through RequireAuth.
func AccountID(c *fiber.Ctx) (int64, bool) {
	v, ok := c.Locals(accountIDKey).(int64)
	return v, ok
}
