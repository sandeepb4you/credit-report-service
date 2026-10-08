package middleware

import (
	"context"
	"log/slog"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/service"
)

// ImpersonationSource answers whether a "View as user" token is still live.
type ImpersonationSource interface {
	ImpersonationLive(ctx context.Context, id int64) (bool, error)
}

// TokenStateSource is what the auth gates read from the database: the token
// epoch, and whether a view token has been ended.
type TokenStateSource interface {
	EpochSource
	ImpersonationSource
}

// ReadOnlyImpersonation makes an admin's "View as user" token read-only, for
// every route at once.
//
// It runs ahead of the routes rather than inside RequireAuth so that no route
// can forget it: a view token on any request other than GET/HEAD/OPTIONS is
// 403, whichever handler it was aimed at — paying, pulling a report, submitting
// a PAN, editing the profile, linking a contact, deleting the account, logging
// out. A view that has been ended or has expired is 401 on every method, so
// Exit in the console revokes the token rather than merely dropping it.
//
// Requests without a view token pass straight through: an ordinary or invalid
// token is left for the route's own gate to judge.
//
// What this does not cover: a handful of GETs do work as a side effect of
// reading — minting the referral code on first read, rendering the advanced
// report PDF on first request, asking Digitap for a statement's status. Each
// produces only what the customer's own read would have, and none spends money.
func ReadOnlyImpersonation(tokens *service.TokenService, live ImpersonationSource) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if c.Get("Authorization") == "" {
			return c.Next()
		}
		tok, err := parseBearer(c, tokens)
		if err != nil || tok.ImpersonationID == 0 {
			return c.Next()
		}
		ok, err := live.ImpersonationLive(c.Context(), tok.ImpersonationID)
		if err != nil {
			slog.Warn("impersonation lookup failed",
				"impersonation_id", tok.ImpersonationID, "error", err)
			return apperr.NewUnauthorized("Could not verify this view; open it again")
		}
		if !ok {
			return apperr.NewUnauthorized("This view has ended")
		}
		switch c.Method() {
		case fiber.MethodGet, fiber.MethodHead, fiber.MethodOptions:
			return c.Next()
		}
		slog.Info("view-as write refused",
			"impersonation_id", tok.ImpersonationID, "admin_id", tok.Impersonator,
			"method", c.Method(), "path", c.Path())
		return apperr.NewForbidden("This is a read-only view of the customer's account")
	}
}
