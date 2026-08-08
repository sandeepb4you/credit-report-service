// Package middleware holds Fiber middleware for the HTTP layer.
package middleware

import (
	"context"
	"log/slog"
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

// EpochSource reads an account's current token epoch. The permission gates use
// it to reject an access token whose role is out of date.
//
// It is an interface rather than the concrete repository so this package keeps
// depending only on what it uses, and so tests can supply a stub.
type EpochSource interface {
	TokenEpoch(ctx context.Context, accountID int64) (int, error)
}

// RequireAuth returns middleware that validates the Bearer JWT and stores the
// account id and role in c.Locals for downstream handlers. Missing/invalid
// tokens yield a 401 via the central error handler. The role is normalized, so
// a legacy token with no role claim reads back as models.RoleUser.
//
// This gate deliberately does not check the token epoch: it makes no
// authorization decision from the role, so a stale role cannot grant anything
// here, and every request would otherwise pay for a database read. Routes that
// do decide on role use RequireRole/RequirePermission below.
func RequireAuth(tokens *service.TokenService) fiber.Handler {
	return func(c *fiber.Ctx) error {
		tok, err := parseBearer(c, tokens)
		if err != nil {
			return err
		}
		storeIdentity(c, tok)
		return c.Next()
	}
}

// RequireRole is RequireAuth plus a minimum-role check. The check is
// hierarchical, not equality: any role ranked at or above `role` passes, so a
// route gated on models.RoleUser also admits admins. An under-privileged or
// unknown role yields 403; a missing/invalid token yields 401 (from
// parseBearer); a token whose role is out of date yields 401 (see
// checkEpoch).
func RequireRole(tokens *service.TokenService, epochs EpochSource, role string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		tok, err := parseBearer(c, tokens)
		if err != nil {
			return err
		}
		if err := checkEpoch(c, epochs, tok); err != nil {
			return err
		}
		if !models.RoleSatisfies(tok.Role, role) {
			return apperr.NewForbidden("This resource requires the '" + role + "' role or higher")
		}
		storeIdentity(c, tok)
		return c.Next()
	}
}

// RequirePermission is RequireAuth plus a capability check. This is the
// preferred gate: routes declare what they need done rather than who may do
// it, so re-scoping a role or adding a new one never touches a route.
// A token whose role lacks the permission yields 403; a missing/invalid token
// yields 401 (from parseBearer); a token whose role is out of date yields 401
// (see checkEpoch).
func RequirePermission(tokens *service.TokenService, epochs EpochSource, perm string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		tok, err := parseBearer(c, tokens)
		if err != nil {
			return err
		}
		if err := checkEpoch(c, epochs, tok); err != nil {
			return err
		}
		if !models.HasPermission(tok.Role, perm) {
			return apperr.NewForbidden("This resource requires the '" + perm + "' permission")
		}
		storeIdentity(c, tok)
		return c.Next()
	}
}

// checkEpoch rejects an access token whose role is no longer current.
//
// The role rides in the JWT, so with a long auth.access-ttl a promotion or
// demotion would otherwise not take effect until the token expired. SetRole
// bumps the account's token_epoch; a token stamped with an older epoch is
// refused here.
//
// The status is 401, not 403, and that choice is load-bearing: clients already
// treat 401 as "refresh and retry", so the account picks up its new role on the
// next refresh rather than being made to sign in again.
//
// A nil EpochSource disables the check, which keeps the middleware usable in
// tests that have no database.
func checkEpoch(c *fiber.Ctx, epochs EpochSource, tok *service.Parsed) error {
	if epochs == nil {
		return nil
	}
	current, err := epochs.TokenEpoch(c.Context(), tok.AccountID)
	if err != nil {
		// The account is gone, or the database is unreachable. Either way this
		// request cannot be authorized, and failing closed is the only safe
		// answer on a permission gate.
		slog.Warn("token epoch lookup failed",
			"account_id", tok.AccountID, "error", err)
		return apperr.NewUnauthorized("Could not verify session; please sign in again")
	}
	if tok.Epoch != current {
		slog.Info("stale access token refused",
			"account_id", tok.AccountID,
			"token_epoch", tok.Epoch,
			"current_epoch", current,
		)
		return apperr.NewUnauthorized("Your permissions changed; refresh your session and retry")
	}
	return nil
}

// storeIdentity publishes the validated token's identity to c.Locals for
// downstream handlers.
func storeIdentity(c *fiber.Ctx, tok *service.Parsed) {
	c.Locals(accountIDKey, tok.AccountID)
	c.Locals(accountRoleKey, models.NormalizeRole(tok.Role))
	c.Locals(sessionIDKey, tok.SessionID)
}

// parseBearer extracts and validates the Bearer JWT from the Authorization
// header.
func parseBearer(c *fiber.Ctx, tokens *service.TokenService) (*service.Parsed, error) {
	h := c.Get("Authorization")
	if h == "" {
		return nil, apperr.NewUnauthorized("Authorization header required")
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		return nil, apperr.NewUnauthorized("Authorization header must be 'Bearer <token>'")
	}
	return tokens.Parse(strings.TrimSpace(parts[1]))
}

// AccountID reads the authenticated account id set by RequireAuth/RequireRole.
// The bool is false when the request did not pass through either middleware.
func AccountID(c *fiber.Ctx) (int64, bool) {
	v, ok := c.Locals(accountIDKey).(int64)
	return v, ok
}

// AccountRole reads the authenticated account role set by RequireAuth/RequireRole.
// The bool is false when the request did not pass through either middleware.
//
// On a RequireAuth-only route this is whatever the token carried and may be out
// of date; only the permission gates verify it. Do not make an authorization
// decision from it — declare a permission on the route instead.
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
