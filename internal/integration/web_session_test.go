// Browser sessions renew from the httpOnly refresh cookie and slide forward on
// every renewal, so a desktop session lasts as long as it keeps being used —
// with its own, longer idle limit (auth.web-refresh-ttl) than a phone's. Two
// tabs renewing at once present the same cookie; the second is a race, not a
// theft, and must not revoke the session. An old replay still does.
package integration

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// webPost sends a POST the way the web app does — X-Device-Platform: web, and
// the refresh cookie when one is given — and returns the status, body and the
// refresh cookie the response set ("" when it set none).
func (h *harness) webPost(path, body, cookie string) (status int, raw, setCookie string) {
	h.t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Device-Platform", "web")
	req.Header.Set("X-Device-Id", "0123456789abcdef0123456789abcdef")
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "refresh_token", Value: cookie})
	}
	res, err := h.app.Test(req, -1)
	if err != nil {
		h.t.Fatalf("POST %s: %v", path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	for _, c := range res.Cookies() {
		if c.Name == "refresh_token" {
			setCookie = c.Value
		}
	}
	return res.StatusCode, string(b), setCookie
}

// webSignIn signs a number in from the browser and returns its refresh cookie.
func (h *harness) webSignIn(phone string) (cookie string, accountID int64) {
	h.t.Helper()
	if r := h.post("/api/auth/otp/phone/send", "", map[string]string{"phone": phone}); r.Status != http.StatusOK {
		h.t.Fatalf("send: %d %s", r.Status, r.Raw)
	}
	status, raw, cookie := h.webPost("/api/auth/otp/phone/verify",
		`{"phone":"`+phone+`","otp":"`+testMasterOTP+`"}`, "")
	if status != http.StatusOK || cookie == "" {
		h.t.Fatalf("web sign-in: %d, cookie %q, %s", status, cookie, raw)
	}
	if strings.Contains(raw, "refreshToken") {
		h.t.Fatalf("a browser's refresh token must stay out of the JSON: %s", raw)
	}
	err := h.pool.QueryRow(h.baseCtx,
		`SELECT id FROM accounts WHERE primary_phone = $1`, phone).Scan(&accountID)
	if err != nil {
		h.t.Fatalf("account for %s: %v", phone, err)
	}
	return cookie, accountID
}

func (h *harness) sessionExpiry(accountID int64, platform string) time.Time {
	h.t.Helper()
	var at time.Time
	err := h.pool.QueryRow(h.baseCtx,
		`SELECT expires_at FROM sessions
		  WHERE account_id = $1 AND COALESCE(device_platform, '') = $2 AND revoked_at IS NULL`,
		accountID, platform).Scan(&at)
	if err != nil {
		h.t.Fatalf("session expiry: %v", err)
	}
	return at
}

func assertAbout(t *testing.T, what string, got, want time.Time) {
	t.Helper()
	if d := got.Sub(want); d < -time.Minute || d > time.Minute {
		t.Fatalf("%s: got %s, want about %s", what, got, want)
	}
}

func TestWebSession_RenewsFromTheCookieAndSlides(t *testing.T) {
	h := newHarness(t)
	cookie, accountID := h.webSignIn("+919700000101")
	assertAbout(t, "web session expiry", h.sessionExpiry(accountID, "web"), time.Now().Add(48*time.Hour))

	// Push the expiry close so the renewal visibly slides it back out.
	if _, err := h.pool.Exec(h.baseCtx,
		`UPDATE sessions SET expires_at = now() + interval '1 hour' WHERE account_id = $1`, accountID); err != nil {
		t.Fatal(err)
	}

	status, raw, next := h.webPost("/api/auth/refresh", "", cookie)
	if status != http.StatusOK || next == "" || next == cookie {
		t.Fatalf("cookie renewal: %d, new cookie %q, %s", status, next, raw)
	}
	if strings.Contains(raw, "refreshToken") {
		t.Fatalf("the rotated token goes back in the cookie, not the JSON: %s", raw)
	}
	assertAbout(t, "renewed expiry", h.sessionExpiry(accountID, "web"), time.Now().Add(48*time.Hour))

	// And again with the rotated cookie: renewals chain indefinitely.
	if status, raw, _ := h.webPost("/api/auth/refresh", "", next); status != http.StatusOK {
		t.Fatalf("second renewal: %d %s", status, raw)
	}
}

func TestWebSession_APhoneKeepsTheShorterLimit(t *testing.T) {
	h := newHarness(t)
	_, accountID := h.signInByPhone("+919700000102", "")
	assertAbout(t, "phone session expiry", h.sessionExpiry(accountID, ""), time.Now().Add(24*time.Hour))
}

func TestWebSession_TwoTabsRenewingAtOnceDoNotRevoke(t *testing.T) {
	h := newHarness(t)
	c1, accountID := h.webSignIn("+919700000103")

	_, _, c2 := h.webPost("/api/auth/refresh", "", c1)
	if c2 == "" {
		t.Fatal("first tab's renewal set no cookie")
	}
	// The second tab, a moment behind, presents the cookie the first just rotated.
	if status, _, _ := h.webPost("/api/auth/refresh", "", c1); status != http.StatusUnauthorized {
		t.Fatalf("a stale cookie still gets 401, got %d", status)
	}
	// …but the session survives: the jar's current cookie still renews.
	status, raw, c3 := h.webPost("/api/auth/refresh", "", c2)
	if status != http.StatusOK {
		t.Fatalf("a race must not revoke the session: %d %s", status, raw)
	}

	// A replay long after the rotation is still theft: the session goes.
	if _, err := h.pool.Exec(h.baseCtx,
		`UPDATE sessions SET last_used_at = now() - interval '5 minutes' WHERE account_id = $1`, accountID); err != nil {
		t.Fatal(err)
	}
	if status, _, _ := h.webPost("/api/auth/refresh", "", c2); status != http.StatusUnauthorized {
		t.Fatalf("old replay: want 401, got %d", status)
	}
	if status, _, _ := h.webPost("/api/auth/refresh", "", c3); status != http.StatusUnauthorized {
		t.Fatalf("after an old replay the whole session is revoked; got %d", status)
	}
}
