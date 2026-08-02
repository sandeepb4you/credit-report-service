// Package middleware holds Fiber middleware for the HTTP layer.
package middleware

import (
	"strings"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/service"
)

// Locals keys for the authenticated account id, role, and session (device) id
// set by RequireAuth.
const (
	accountIDKey   = "accountID"
	accountRoleKey = "accountRole"
	sessionIDKey   = "sessionID"
)

// RequireAuth returns middleware that validates the Bearer JWT and stores the
// account id and role in c.Locals for downstream handlers. Missing/invalid
// tokens yield a 401 via the central error handler. The role is normalized, so
// a legacy token with no role claim reads back as models.RoleUser.
func RequireAuth(tokens *service.TokenService) fiber.Handler {
	return func(c *fiber.Ctx) error {
		accountID, role, sessionID, err := parseBearer(c, tokens)
		if err != nil {
			return err
		}
		c.Locals(accountIDKey, accountID)
		c.Locals(accountRoleKey, models.NormalizeRole(role))
		c.Locals(sessionIDKey, sessionID)
		return c.Next()
	}
}

// RequireRole is RequireAuth plus a minimum-role check. The check is
// hierarchical, not equality: any role ranked at or above `role` passes, so a
// route gated on models.RoleUser also admits admins. An under-privileged or
// unknown role yields 403; a missing/invalid token yields 401 (from
// parseBearer).
func RequireRole(tokens *service.TokenService, role string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		accountID, got, sessionID, err := parseBearer(c, tokens)
		if err != nil {
			return err
		}
		if !models.RoleSatisfies(got, role) {
			return apperr.NewForbidden("This resource requires the '" + role + "' role or higher")
		}
		c.Locals(accountIDKey, accountID)
		c.Locals(accountRoleKey, models.NormalizeRole(got))
		c.Locals(sessionIDKey, sessionID)
		return c.Next()
	}
}

// parseBearer extracts and validates the Bearer JWT from the Authorization
// header, returning the account id, role, and session id.
func parseBearer(c *fiber.Ctx, tokens *service.TokenService) (int64, string, int64, error) {
	h := c.Get("Authorization")
	if h == "" {
		return 0, "", 0, apperr.NewUnauthorized("Authorization header required")
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		return 0, "", 0, apperr.NewUnauthorized("Authorization header must be 'Bearer <token>'")
	}
	id, role, sid, err := tokens.Parse(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, "", 0, err
	}
	return id, role, sid, nil
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

// SessionID reads the id of the session (device) the request's access token
// was issued for. It is 0 for tokens minted before session tracking existed,
// so callers must treat 0 as "unknown device" rather than a real session.
func SessionID(c *fiber.Ctx) int64 {
	v, _ := c.Locals(sessionIDKey).(int64)
	return v
}
