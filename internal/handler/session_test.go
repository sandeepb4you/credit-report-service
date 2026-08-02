package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/server/middleware"
	"credit-report-service/internal/service"
)

// authResult is a stand-in for a freshly minted pair, used to exercise the
// delivery split without reaching the database.
func authResult() *service.AuthResult {
	return &service.AuthResult{
		Token:            "access-jwt",
		ExpiresAt:        time.Now().Add(15 * time.Minute),
		RefreshToken:     "rt_secret-value",
		SessionID:        11,
		RefreshExpiresAt: time.Now().Add(720 * time.Hour),
	}
}

// respondApp mounts respondAuth the way the login handlers call it.
func respondApp(cookieSecure bool) *fiber.App {
	h := NewAuthHandler(nil, nil, cookieSecure)
	app := fiber.New(fiber.Config{ErrorHandler: apperr.ErrorHandler})
	app.Post("/", func(c *fiber.Ctx) error {
		return h.respondAuth(c, authResult(), middleware.Device(c).IsWeb())
	})
	return app
}

func findCookie(resp *http.Response, name string) *http.Cookie {
	for _, ck := range resp.Cookies() {
		if ck.Name == name {
			return ck
		}
	}
	return nil
}

func decodeBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return out
}

// A mobile client has no cookie jar to rely on, so it must get the refresh
// token in the body.
func TestRespondAuth_MobileGetsTokenInBody(t *testing.T) {
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set(middleware.HeaderDevicePlatform, "ios")
	resp, err := respondApp(true).Test(req)
	if err != nil {
		t.Fatal(err)
	}
	body := decodeBody(t, resp)
	if body["refreshToken"] != "rt_secret-value" {
		t.Errorf("refreshToken = %v, want the token in the body", body["refreshToken"])
	}
	if findCookie(resp, refreshCookieName) != nil {
		t.Error("mobile response must not set a refresh cookie")
	}
}

// The whole point of the cookie path: a browser must never receive the
// refresh token anywhere JavaScript can read it.
func TestRespondAuth_WebGetsCookieAndNoTokenInBody(t *testing.T) {
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set(middleware.HeaderDevicePlatform, "web")
	resp, err := respondApp(true).Test(req)
	if err != nil {
		t.Fatal(err)
	}

	body := decodeBody(t, resp)
	if _, present := body["refreshToken"]; present {
		t.Error("web response body must omit refreshToken entirely")
	}
	if body["token"] != "access-jwt" {
		t.Errorf("access token missing from body: %v", body["token"])
	}

	ck := findCookie(resp, refreshCookieName)
	if ck == nil {
		t.Fatal("web response must set the refresh cookie")
	}
	if ck.Value != "rt_secret-value" {
		t.Errorf("cookie value = %q", ck.Value)
	}
	if !ck.HttpOnly {
		t.Error("refresh cookie must be HttpOnly — that is what blocks XSS exfiltration")
	}
	if !ck.Secure {
		t.Error("refresh cookie must be Secure when cookie-secure is on")
	}
	if ck.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict (this is the CSRF defence)", ck.SameSite)
	}
	if ck.Path != refreshCookiePath {
		t.Errorf("cookie Path = %q, want %q", ck.Path, refreshCookiePath)
	}
}

// Local http dev needs Secure off, and nothing else about the cookie changes.
func TestRespondAuth_CookieSecureIsConfigurable(t *testing.T) {
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set(middleware.HeaderDevicePlatform, "web")
	resp, err := respondApp(false).Test(req)
	if err != nil {
		t.Fatal(err)
	}
	ck := findCookie(resp, refreshCookieName)
	if ck == nil {
		t.Fatal("expected refresh cookie")
	}
	if ck.Secure {
		t.Error("Secure must follow config")
	}
	if !ck.HttpOnly {
		t.Error("HttpOnly must stay on regardless of the Secure setting")
	}
}

// A client sending no platform header is treated as an API client, not a
// browser — the token goes in the body so curl and Swagger UI still work.
func TestRespondAuth_UnknownPlatformGetsBody(t *testing.T) {
	resp, err := respondApp(true).Test(httptest.NewRequest("POST", "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	if decodeBody(t, resp)["refreshToken"] != "rt_secret-value" {
		t.Error("unknown platform should receive the token in the body")
	}
}

// ---- Refresh input handling ---------------------------------------------

// A refresh call with neither cookie nor body token is a 401, not a 500.
func TestRefresh_MissingTokenIsUnauthorized(t *testing.T) {
	h := NewAuthHandler(nil, nil, true)
	app := fiber.New(fiber.Config{ErrorHandler: apperr.ErrorHandler})
	app.Post("/refresh", h.Refresh)

	req := httptest.NewRequest("POST", "/refresh", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestRefresh_EmptyBodyIsUnauthorized(t *testing.T) {
	h := NewAuthHandler(nil, nil, true)
	app := fiber.New(fiber.Config{ErrorHandler: apperr.ErrorHandler})
	app.Post("/refresh", h.Refresh)

	resp, err := app.Test(httptest.NewRequest("POST", "/refresh", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// ---- Logout --------------------------------------------------------------

// A token predating session tracking has no session row to revoke; logout
// must still succeed and still clear the cookie so the client can sign out.
func TestLogout_LegacyTokenStillClearsCookie(t *testing.T) {
	h := NewAuthHandler(nil, nil, true)
	app := fiber.New(fiber.Config{ErrorHandler: apperr.ErrorHandler})
	app.Post("/logout", func(c *fiber.Ctx) error {
		c.Locals("accountID", int64(7))
		c.Locals("sessionID", int64(0)) // legacy token: no sid claim
		return h.Logout(c)
	})

	resp, err := app.Test(httptest.NewRequest("POST", "/logout", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	ck := findCookie(resp, refreshCookieName)
	if ck == nil {
		t.Fatal("logout must emit a cookie-clearing Set-Cookie")
	}
	if ck.Value != "" {
		t.Errorf("cleared cookie value = %q, want empty", ck.Value)
	}
}

func TestLogout_RequiresAuth(t *testing.T) {
	h := NewAuthHandler(nil, nil, true)
	app := fiber.New(fiber.Config{ErrorHandler: apperr.ErrorHandler})
	app.Post("/logout", h.Logout) // no auth middleware -> no accountID in Locals

	resp, err := app.Test(httptest.NewRequest("POST", "/logout", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// ---- Session id path parsing --------------------------------------------

func TestRevokeSession_NonIntegerID(t *testing.T) {
	h := NewAuthHandler(nil, nil, true)
	app := fiber.New(fiber.Config{ErrorHandler: apperr.ErrorHandler})
	app.Delete("/sessions/:id", func(c *fiber.Ctx) error {
		c.Locals("accountID", int64(1))
		c.Locals("sessionID", int64(2))
		return h.RevokeSession(c)
	})

	resp, err := app.Test(httptest.NewRequest("DELETE", "/sessions/abc", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}
