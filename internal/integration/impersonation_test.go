package integration

import (
	"fmt"
	"net/http"
	"testing"
)

func (h *harness) startView(adminToken string, accountID int64) response {
	h.t.Helper()
	return h.post(fmt.Sprintf("/api/admin/accounts/%d/impersonate", accountID), adminToken, map[string]any{})
}

// An admin's "View as user" token reads exactly what the customer would see,
// and nothing it sends other than a read gets through — on any route.
func TestViewAsUserIsReadOnly(t *testing.T) {
	h := newHarness(t)
	admin := h.makeAdmin("+919000000801")
	_, accountID := h.signInByPhone("+919000000802", "")
	sessionsBefore := h.liveSessions(accountID)

	res := h.startView(admin, accountID)
	if res.Status != http.StatusOK {
		t.Fatalf("start view: %d %s", res.Status, res.Raw)
	}
	view, _ := res.Body["token"].(string)
	if view == "" {
		t.Fatalf("no token in %s", res.Raw)
	}
	if acc, _ := res.Body["account"].(map[string]any); acc["phone"] != "+919000000802" {
		t.Errorf("banner account = %v", acc)
	}

	// Reads answer as the customer.
	prof := h.get("/api/profile", view)
	if prof.Status != http.StatusOK {
		t.Fatalf("profile through the view: %d %s", prof.Status, prof.Raw)
	}
	if got := fmt.Sprint(prof.Body["id"]); got != fmt.Sprint(accountID) {
		t.Errorf("profile id = %s, want the customer's %d (%s)", got, accountID, prof.Raw)
	}
	if r := h.get("/api/orders", view); r.Status != http.StatusOK {
		t.Errorf("orders list: %d %s", r.Status, r.Raw)
	}

	// Every write is refused, whatever it was aimed at.
	writes := []struct{ method, path string }{
		{http.MethodPost, "/api/orders"},
		{http.MethodPut, "/api/profile"},
		{http.MethodPost, "/api/kyc/pan"},
		{http.MethodPost, "/api/credit-analytics/reports"},
		{http.MethodPost, "/api/auth/logout"},
		{http.MethodPost, "/api/auth/email/send"},
		{http.MethodDelete, "/api/auth/sessions"},
	}
	for _, w := range writes {
		if r := h.do(w.method, w.path, view, map[string]any{"productCode": "CREDIT_ANALYSIS"}); r.Status != http.StatusForbidden {
			t.Errorf("%s %s through the view: want 403, got %d %s", w.method, w.path, r.Status, r.Raw)
		}
	}

	// The view carries the customer's role, so the console stays shut.
	if r := h.get("/api/admin/accounts", view); r.Status != http.StatusForbidden {
		t.Errorf("admin route through the view: want 403, got %d", r.Status)
	}
	// And it is not a sign-in: no session row was written for the customer.
	if got := h.liveSessions(accountID); got != sessionsBefore {
		t.Errorf("sessions = %d, want %d unchanged", got, sessionsBefore)
	}
}

// Ending a view revokes its token at once, rather than leaving it to expire.
func TestEndingAViewRevokesItsToken(t *testing.T) {
	h := newHarness(t)
	admin := h.makeAdmin("+919000000803")
	_, accountID := h.signInByPhone("+919000000804", "")

	res := h.startView(admin, accountID)
	view, _ := res.Body["token"].(string)
	id := int64(res.Body["impersonationId"].(float64))

	// The view's own token cannot end it: that is a write.
	end := fmt.Sprintf("/api/admin/impersonations/%d/end", id)
	if r := h.post(end, view, map[string]any{}); r.Status != http.StatusForbidden {
		t.Errorf("end with the view token: want 403, got %d", r.Status)
	}
	if r := h.post(end, admin, map[string]any{}); r.Status != http.StatusOK {
		t.Fatalf("end: %d %s", r.Status, r.Raw)
	}
	if r := h.post(end, admin, map[string]any{}); r.Status != http.StatusOK {
		t.Errorf("ending twice: want 200, got %d", r.Status)
	}
	if r := h.get("/api/profile", view); r.Status != http.StatusUnauthorized {
		t.Errorf("profile after the view ended: want 401, got %d %s", r.Status, r.Raw)
	}

	// Every view is audited.
	var n int
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT count(*) FROM admin_impersonations WHERE target_account_id = $1 AND ended_at IS NOT NULL`,
		accountID).Scan(&n); err != nil || n != 1 {
		t.Errorf("audit rows = %d (%v), want 1 ended", n, err)
	}
}

// Only a customer can be viewed, and only by someone holding the permission.
func TestViewAsUserIsForAdminsLookingAtCustomers(t *testing.T) {
	h := newHarness(t)
	admin := h.makeAdmin("+919000000805")
	h.makeAdmin("+919000000806")
	customer, customerID := h.signInByPhone("+919000000807", "")

	var otherID int64
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT id FROM accounts WHERE primary_phone = '+919000000806'`).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	if r := h.startView(admin, otherID); r.Status != http.StatusForbidden {
		t.Errorf("viewing an admin: want 403, got %d %s", r.Status, r.Raw)
	}
	if r := h.startView(customer, customerID+1000); r.Status != http.StatusForbidden {
		t.Errorf("a customer starting a view: want 403, got %d", r.Status)
	}
	if r := h.startView(admin, 9_999_999); r.Status != http.StatusNotFound {
		t.Errorf("unknown account: want 404, got %d", r.Status)
	}
}
