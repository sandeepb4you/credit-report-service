// Push tokens on the session row: registration binds a token to the caller's
// own session, logout revokes the session and silences the device with no
// push-specific cleanup, and re-login on the same device STEALS the token from
// the dead row — the invariant that stops double-sends. The admin send is
// permission-gated and honest about an unconfigured (stub) server.
package integration

import (
	"net/http"
	"testing"
)

const testFCMToken = "fcm-test-token-abcdefghijklmnopqrstuvwxyz-0123456789"

// pushTokenRows reads (session_id, token) pairs straight off the table.
func (h *harness) pushTokenRows(t *testing.T, accountID int64) map[int64]string {
	t.Helper()
	rows, err := h.pool.Query(h.baseCtx,
		`SELECT id, fcm_token FROM sessions
		  WHERE account_id = $1 AND fcm_token IS NOT NULL`, accountID)
	if err != nil {
		t.Fatalf("read tokens: %v", err)
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var tok string
		if err := rows.Scan(&id, &tok); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[id] = tok
	}
	return out
}

func TestPushToken_RegistersOnTheCallersSession(t *testing.T) {
	h := newHarness(t)
	token, accountID := h.signInByPhone("+919700000001", "")

	res := h.put("/api/push-token", token, map[string]string{"token": testFCMToken})
	if res.Status != http.StatusNoContent {
		t.Fatalf("register: %d %s", res.Status, res.Raw)
	}
	rows := h.pushTokenRows(t, accountID)
	if len(rows) != 1 {
		t.Fatalf("token rows = %d, want 1", len(rows))
	}
}

func TestPushToken_LogoutSilencesTheDevice(t *testing.T) {
	h := newHarness(t)
	token, accountID := h.signInByPhone("+919700000002", "")
	if res := h.put("/api/push-token", token, map[string]string{"token": testFCMToken}); res.Status != http.StatusNoContent {
		t.Fatalf("register: %d %s", res.Status, res.Raw)
	}

	if res := h.post("/api/auth/logout", token, map[string]string{}); res.Status >= 300 {
		t.Fatalf("logout: %d %s", res.Status, res.Raw)
	}

	// The token may still sit on the revoked row — what matters is that the
	// fan-out read returns nothing for the account.
	var live int
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT count(*) FROM sessions
		  WHERE account_id = $1 AND revoked_at IS NULL AND fcm_token IS NOT NULL`,
		accountID).Scan(&live); err != nil {
		t.Fatalf("count: %v", err)
	}
	if live != 0 {
		t.Fatalf("live tokens after logout = %d, want 0", live)
	}
}

func TestPushToken_ReloginStealsTheTokenFromTheOldSession(t *testing.T) {
	h := newHarness(t)
	first, accountID := h.signInByPhone("+919700000003", "")
	if res := h.put("/api/push-token", first, map[string]string{"token": testFCMToken}); res.Status != http.StatusNoContent {
		t.Fatalf("first register: %d %s", res.Status, res.Raw)
	}

	// Same device, new sign-in (no logout — the app died and came back). The
	// harness sends no X-Device-Id, so this mints a second session row.
	second, _ := h.signInByPhone("+919700000003", "")
	if res := h.put("/api/push-token", second, map[string]string{"token": testFCMToken}); res.Status != http.StatusNoContent {
		t.Fatalf("second register: %d %s", res.Status, res.Raw)
	}

	rows := h.pushTokenRows(t, accountID)
	if len(rows) != 1 {
		t.Fatalf("after steal, token rows = %d, want exactly 1 (no double-sends)", len(rows))
	}
}

func TestAdminNotify_NeedsThePermissionAndReportsStubHonestly(t *testing.T) {
	h := newHarness(t)
	userToken, accountID := h.signInByPhone("+919700000004", "")
	if res := h.put("/api/push-token", userToken, map[string]string{"token": testFCMToken}); res.Status != http.StatusNoContent {
		t.Fatalf("register: %d %s", res.Status, res.Raw)
	}

	body := map[string]string{"title": "Hello", "body": "From the test"}
	path := "/api/admin/accounts/" + itoa64(accountID) + "/notify"

	// A customer cannot send notifications.
	if res := h.post(path, userToken, body); res.Status != http.StatusForbidden {
		t.Fatalf("customer notify: %d, want 403", res.Status)
	}

	// An admin can — and the stub server says so instead of pretending to send.
	adminToken := h.makeAdmin("+919700000005")
	if res := h.post(path, adminToken, body); res.Status != http.StatusServiceUnavailable {
		t.Fatalf("admin notify on stub: %d %s, want 503 (push unconfigured)", res.Status, res.Raw)
	}
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
