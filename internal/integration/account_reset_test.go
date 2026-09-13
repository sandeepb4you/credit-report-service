// Admin account reset: putting an account back to signup without losing its
// login, against real handlers and a real Postgres.
//
// The case that matters here is the one that broke in production. The reset
// deletes a dozen tables in one transaction, and `scheduled_score_checks` —
// added later, for the prepaid plans — holds an order_id foreign key with NO
// ACTION. Nothing deleted those rows, so the orders delete violated the
// constraint and the whole reset failed with a 500 for every account that had
// ever bought a plan. A plan purchase is the ordinary case for a paying user,
// so this test buys one before resetting.
package integration

import (
	"net/http"
	"testing"
)

// resetAccount calls the admin route the console's "Reset to signup" button hits.
func (h *harness) resetAccount(adminToken string, accountID int64, confirm string) response {
	h.t.Helper()
	return h.post(
		"/api/admin/accounts/"+itoa(accountID)+"/reset",
		adminToken,
		map[string]string{"confirm": confirm},
	)
}

func TestAccountResetClearsAPlanPurchase(t *testing.T) {
	h := newHarness(t)

	const phone = "+919000000501"
	token, accountID := h.signInByPhone(phone, "")
	adminToken := h.makeAdmin("+919000000502")

	// The real buy → webhook → fulfilment path, which mints the scheduled runs
	// whose foreign key is the point of this test.
	h.buyPlan(token, "SCORE_PLUS_MONTHLY")
	if got := pendingRuns(h.scheduledChecks(token)); got == 0 {
		t.Fatalf("expected a minted plan batch before the reset, got %d pending runs", got)
	}

	res := h.resetAccount(adminToken, accountID, phone)
	if res.Status != http.StatusOK {
		t.Fatalf("reset with a plan purchase: %d %s", res.Status, res.Raw)
	}

	// The receipt must name the runs it destroyed: they are the rest of what the
	// user paid for, and an admin confirming a reset is entitled to know.
	removed, _ := res.Body["removed"].(map[string]any)
	if removed == nil {
		t.Fatalf("no removed block in the reset receipt: %s", res.Raw)
	}
	if n, _ := removed["scheduledChecks"].(float64); n == 0 {
		t.Errorf("reset receipt reports no scheduled checks removed: %s", res.Raw)
	}
	if n, _ := removed["paidOrders"].(float64); n != 1 {
		t.Errorf("paidOrders = %v, want 1: %s", removed["paidOrders"], res.Raw)
	}

	// Nothing of the plan survives: the schedule is empty, and the rows are gone
	// rather than merely detached from the order.
	var runs int
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT count(*) FROM scheduled_score_checks WHERE account_id = $1`, accountID,
	).Scan(&runs); err != nil {
		t.Fatalf("count scheduled runs: %v", err)
	}
	if runs != 0 {
		t.Errorf("scheduled_score_checks rows left after reset: %d", runs)
	}

	// The login survives, which is the whole purpose of a reset rather than a
	// delete — and the account it signs back into is the same one.
	newToken, sameID := h.signInByPhone(phone, "")
	if sameID != accountID {
		t.Fatalf("signing in after reset made a new account: %d -> %d", accountID, sameID)
	}
	if got := pendingRuns(h.scheduledChecks(newToken)); got != 0 {
		t.Errorf("plan quota survived the reset: %d pending runs", got)
	}
}

// The confirmation is the guard against a mistyped account id, so it is checked
// before anything is deleted — and a wrong one leaves the plan intact.
func TestAccountResetRefusesAMismatchedConfirmation(t *testing.T) {
	h := newHarness(t)

	const phone = "+919000000503"
	token, accountID := h.signInByPhone(phone, "")
	adminToken := h.makeAdmin("+919000000504")
	h.buyPlan(token, "SCORE_PLUS_MONTHLY")

	res := h.resetAccount(adminToken, accountID, "+919999999999")
	if res.Status != http.StatusBadRequest {
		t.Fatalf("mismatched confirmation: %d %s, want 400", res.Status, res.Raw)
	}
	if got := pendingRuns(h.scheduledChecks(token)); got == 0 {
		t.Error("a refused reset destroyed the plan anyway")
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
