package handler

import (
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/server/middleware"
	"credit-report-service/internal/service"
)

// refreshCookieName is the httpOnly cookie carrying the refresh token for web
// clients. Its Path is scoped to the auth group so it is never attached to
// ordinary API calls — the only endpoint that needs it is /auth/refresh.
const (
	refreshCookieName = "refresh_token"
	refreshCookiePath = "/api/auth"
)

// ---- POST /api/auth/refresh ----------------------------------------------

type refreshReq struct {
	RefreshToken string `json:"refreshToken" example:"rt_8Kx3..."`
}

// Refresh godoc
//
// @Summary      Exchange a refresh token for a new token pair
// @Description  Rotates the refresh token and issues a fresh access token. Mobile clients send the token in the JSON body; web clients send nothing — the httpOnly `refresh_token` cookie is read automatically. The old refresh token is invalidated on every call, so replaying one revokes the whole session as a theft signal. Public endpoint: the refresh token itself is the credential.
// @Tags         auth
// @Accept       json
// @Produce      json
// @Param        X-Device-Id        header  string      false  "Stable per-device UUID"
// @Param        X-Device-Platform  header  string      false  "ios | android | web"
// @Param        X-Device-Info      header  string  false  "JSON device description, e.g. {\"manufacturer\":\"Apple\",\"model\":\"iPhone15,3\",\"osVersion\":\"17.4\"}"
// @Param        request            body    refreshReq  false  "Refresh token (omit for web cookie clients)"
// @Success      200  {object}  service.AuthResult
// @Failure      401  {object}  apperr.ErrorBody  "Missing, expired, replayed, or unknown refresh token"
// @Router       /auth/refresh [post]
func (h *AuthHandler) Refresh(c *fiber.Ctx) error {
	dev := middleware.Device(c)

	// Cookie first: a web client's body is empty by design, and preferring the
	// cookie stops a stale body value from shadowing the real credential.
	token := c.Cookies(refreshCookieName)
	fromCookie := token != ""
	if token == "" {
		var req refreshReq
		if err := c.BodyParser(&req); err == nil {
			token = strings.TrimSpace(req.RefreshToken)
		}
	}
	if token == "" {
		return apperr.NewUnauthorized("Refresh token required")
	}

	res, err := h.svc.Refresh(c.Context(), token, dev)
	if err != nil {
		// A failed refresh means this browser's cookie is worthless; drop it so
		// the client stops retrying with a dead credential.
		if fromCookie {
			clearRefreshCookie(c, h.cookieSecure)
		}
		return err
	}
	// A request that arrived by cookie must leave by cookie even if it forgot
	// the platform header — otherwise the rotated token goes out in the body
	// while the browser keeps the old one, and the next refresh fails.
	return h.respondAuth(c, res, fromCookie || dev.IsWeb())
}

// ---- POST /api/auth/logout -----------------------------------------------

// Logout godoc
//
// @Summary      Sign out of the current device
// @Description  Revokes the session the access token was issued for and clears the web refresh cookie. The access token itself stays valid until it expires (minutes), so clients should discard it too.
// @Tags         auth
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  map[string]string  "{\"message\": \"Signed out\"}"
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Router       /auth/logout [post]
func (h *AuthHandler) Logout(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	clearRefreshCookie(c, h.cookieSecure)

	sessionID := middleware.SessionID(c)
	if sessionID == 0 {
		// Token predates session tracking; there is no row to revoke. Treat as
		// success so the client can complete its sign-out either way.
		return c.JSON(fiber.Map{"message": "Signed out"})
	}
	if err := h.sessions.Revoke(c.Context(), accountID, sessionID, models.RevokeLogout); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"message": "Signed out"})
}

// ---- GET /api/auth/sessions ----------------------------------------------

// ListSessions godoc
//
// @Summary      List signed-in devices
// @Description  Returns the account's active sessions, most recently used first. The entry matching the caller's own access token is flagged with `current: true`.
// @Tags         auth
// @Produce      json
// @Security     BearerAuth
// @Success      200  {array}   service.SessionView
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Router       /auth/sessions [get]
func (h *AuthHandler) ListSessions(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	out, err := h.sessions.List(c.Context(), accountID, middleware.SessionID(c))
	if err != nil {
		return err
	}
	return c.JSON(out)
}

// ---- DELETE /api/auth/sessions/:id ---------------------------------------

// RevokeSession godoc
//
// @Summary      Sign out one device
// @Description  Revokes a single session by id. Only the caller's own sessions are reachable — an id belonging to another account returns 404, not 403, so session ids cannot be probed.
// @Tags         auth
// @Produce      json
// @Security     BearerAuth
// @Param        id   path      int  true  "Session id from GET /auth/sessions"
// @Success      200  {object}  map[string]string  "{\"message\": \"Device signed out\"}"
// @Failure      400  {object}  apperr.ErrorBody  "id must be an integer"
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Failure      404  {object}  apperr.ErrorBody  "No such active session"
// @Router       /auth/sessions/{id} [delete]
func (h *AuthHandler) RevokeSession(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	sessionID, err := strconv.ParseInt(c.Params("id"), 10, 64)
	if err != nil {
		return apperr.NewValidation("id must be an integer")
	}
	if err := h.sessions.Revoke(c.Context(), accountID, sessionID, models.RevokeByUser); err != nil {
		return err
	}
	// Revoking your own device is a logout; take the cookie with it.
	if sessionID == middleware.SessionID(c) {
		clearRefreshCookie(c, h.cookieSecure)
	}
	return c.JSON(fiber.Map{"message": "Device signed out"})
}

// ---- DELETE /api/auth/sessions -------------------------------------------

// RevokeOtherSessions godoc
//
// @Summary      Sign out every other device
// @Description  Revokes all of the account's sessions except the caller's own. Use after a password change or when a device is lost.
// @Tags         auth
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  map[string]int  "{\"revoked\": 3}"
// @Failure      401  {object}  apperr.ErrorBody  "Not authenticated"
// @Router       /auth/sessions [delete]
func (h *AuthHandler) RevokeOtherSessions(c *fiber.Ctx) error {
	accountID, ok := middleware.AccountID(c)
	if !ok {
		return apperr.NewUnauthorized("Not authenticated")
	}
	n, err := h.sessions.RevokeOthers(c.Context(), accountID, middleware.SessionID(c))
	if err != nil {
		return err
	}
	return c.JSON(fiber.Map{"revoked": n})
}

// ---- shared response helper ----------------------------------------------

// respondAuth writes an auth result, routing the refresh token by client type.
//
// When useCookie is set the token goes out as an httpOnly cookie and never
// appears in the body: a refresh token is a weeks-long credential, and keeping
// it out of JS means an XSS on the frontend cannot exfiltrate one. Mobile
// clients get it in the body instead — there is no cookie jar to rely on, so
// they store it in the Keychain / EncryptedSharedPreferences.
func (h *AuthHandler) respondAuth(c *fiber.Ctx, res *service.AuthResult, useCookie bool) error {
	if useCookie {
		setRefreshCookie(c, res.RefreshToken, res.RefreshExpiresAt, h.cookieSecure)
		res.RefreshToken = "" // omitempty keeps it out of the JSON entirely
	}
	return c.JSON(res)
}

// setRefreshCookie writes the web refresh cookie.
//
// SameSite=Strict is what stands in for a CSRF token here: /auth/refresh is
// the only cookie-authenticated endpoint, and a cross-site request will not
// carry a Strict cookie at all, so there is nothing for an attacker to ride.
// Secure comes from config so local http dev still works — it must be true
// anywhere real.
func setRefreshCookie(c *fiber.Ctx, token string, expires time.Time, secure bool) {
	c.Cookie(&fiber.Cookie{
		Name:     refreshCookieName,
		Value:    token,
		Path:     refreshCookiePath,
		Expires:  expires,
		HTTPOnly: true,
		Secure:   secure,
		SameSite: fiber.CookieSameSiteStrictMode,
	})
}

// clearRefreshCookie expires the cookie. Every attribute except Value and
// Expires must match setRefreshCookie or the browser keeps the original.
func clearRefreshCookie(c *fiber.Ctx, secure bool) {
	c.Cookie(&fiber.Cookie{
		Name:     refreshCookieName,
		Value:    "",
		Path:     refreshCookiePath,
		Expires:  time.Unix(0, 0),
		HTTPOnly: true,
		Secure:   secure,
		SameSite: fiber.CookieSameSiteStrictMode,
	})
}
