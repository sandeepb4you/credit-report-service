package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// A customer who paid before their PAN was approved by hand is owed a report,
// and must get it without coming back to the app.
//
// The production case these replay (2026-10-08): pay ₹299, fail the automated
// PAN check three times, upload the card, an admin approves — and nothing
// happens. Approval changed only the KYC record; the pull waited for a tap the
// customer never made. Worse, the name they typed with each attempt was thrown
// away on every failure, so even a manual pull could not run: the bureau
// request needs a name.

// countReports is how many pull rows an account has.
func (h *harness) countReports(accountID int64) int {
	h.t.Helper()
	var n int
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT count(*) FROM credit_analytics_requests WHERE account_id = $1`, accountID).Scan(&n); err != nil {
		h.t.Fatalf("count reports: %v", err)
	}
	return n
}

// waitForReport polls until the account has a report — the approval hook runs
// the pull in the background — or fails the test after a few seconds.
func (h *harness) waitForReport(accountID int64) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if h.countReports(accountID) > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.t.Fatalf("no report for account %d: the approval did not run the owed pull", accountID)
}

func (h *harness) profileName(accountID int64) (first, last string) {
	h.t.Helper()
	var f, l *string
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT first_name, last_name FROM accounts WHERE id = $1`, accountID).Scan(&f, &l); err != nil {
		h.t.Fatalf("read name: %v", err)
	}
	if f != nil {
		first = *f
	}
	if l != nil {
		last = *l
	}
	return first, last
}

func (h *harness) approvePAN(adminToken string, accountID int64) {
	h.t.Helper()
	res := h.post(fmt.Sprintf("/api/admin/kyc/pan/%d/verify", accountID), adminToken, map[string]any{})
	if res.Status != http.StatusOK {
		h.t.Fatalf("approve PAN: %d %s", res.Status, res.Raw)
	}
}

// failPANCheck submits a name the offline checker does not have on file, as a
// customer whose name differs from the provider's record does.
func (h *harness) failPANCheck(token, name string) {
	h.t.Helper()
	if res := h.verifyPAN(token, stubPAN, name); res.Status != http.StatusUnprocessableEntity {
		h.t.Fatalf("expected the PAN check to fail (422), got %d %s", res.Status, res.Raw)
	}
}

func TestApprovingAPANRunsTheReportAlreadyPaidFor(t *testing.T) {
	h := newHarness(t)
	admin := h.makeAdmin("+919000000951")
	token, accountID := h.signInByPhone("+919000000952", "")

	h.buyPlan(token, "CREDIT_ANALYSIS")
	h.failPANCheck(token, "RAHUL SHARMA")
	if h.countReports(accountID) != 0 {
		t.Fatal("no report can exist before the PAN is approved")
	}

	h.approvePAN(admin, accountID)

	// No tap in the app: the approval itself delivers what was paid for.
	h.waitForReport(accountID)

	// The name typed with the failed attempt was kept and, on approval, became
	// the profile name — which is what let the pull run at all.
	if first, last := h.profileName(accountID); first != "RAHUL" || last != "SHARMA" {
		t.Errorf("profile name = %q %q, want the name typed with the PAN", first, last)
	}

	// Delivered means spent: nobody is left owed.
	owed := h.get("/api/admin/owed-reports", admin)
	if owed.Status != http.StatusOK {
		t.Fatalf("owed list: %d %s", owed.Status, owed.Raw)
	}
	if rows := owedRowsFor(t, owed.Raw, accountID); len(rows) != 0 {
		t.Errorf("a delivered report is no longer owed, got %v", rows)
	}
}

func TestApprovingAPANWithNothingPaidPullsNothing(t *testing.T) {
	h := newHarness(t)
	admin := h.makeAdmin("+919000000961")
	token, accountID := h.signInByPhone("+919000000962", "")

	h.failPANCheck(token, "RAHUL SHARMA")
	h.approvePAN(admin, accountID)

	// The hook runs in the background; give it the time it would need, then
	// check it made no billed call for an account that bought nothing.
	time.Sleep(500 * time.Millisecond)
	if n := h.countReports(accountID); n != 0 {
		t.Errorf("an approval with no purchase behind it must not pull; got %d reports", n)
	}
}

// The admin console's half: who is still owed, and running it by hand —
// including for an account whose name was never captured, which is exactly
// the production account this was written for.
func TestAdminCanSeeAndRunAnOwedReport(t *testing.T) {
	h := newHarness(t)
	admin := h.makeAdmin("+919000000971")
	token, accountID := h.signInByPhone("+919000000972", "")

	h.buyPlan(token, "CREDIT_ANALYSIS")
	h.failPANCheck(token, "PRIYA NAIR")
	// Reproduce the data the old code left behind: no name anywhere.
	if _, err := h.pool.Exec(h.baseCtx,
		`UPDATE kyc_records SET pan_name = NULL WHERE account_id = $1`, accountID); err != nil {
		t.Fatal(err)
	}

	// Still owed, and not yet pullable: the list says so.
	owed := h.get("/api/admin/owed-reports", admin)
	rows := owedRowsFor(t, owed.Raw, accountID)
	if len(rows) != 1 || rows[0]["kycStatus"] != "PENDING" || rows[0]["source"] != "order" {
		t.Fatalf("owed row = %v", rows)
	}

	h.approvePAN(admin, accountID)
	time.Sleep(300 * time.Millisecond) // the hook tries, and fails for want of a name

	run := fmt.Sprintf("/api/admin/accounts/%d/run-owed-report", accountID)
	if res := h.post(run, admin, map[string]any{}); res.Status != http.StatusBadRequest {
		t.Fatalf("no name on file: want the pull's own 400, got %d %s", res.Status, res.Raw)
	}
	if res := h.post(run, admin, map[string]any{"firstName": "PRIYA"}); res.Status != http.StatusBadRequest {
		t.Fatalf("half a name: want 400, got %d %s", res.Status, res.Raw)
	}

	// The admin reads the name off the card; it is saved, then the pull runs.
	res := h.post(run, admin, map[string]any{"firstName": "PRIYA", "lastName": "NAIR"})
	if res.Status != http.StatusOK {
		t.Fatalf("run owed report: %d %s", res.Status, res.Raw)
	}
	if id, _ := res.Body["reportId"].(float64); id <= 0 {
		t.Errorf("want a report id, got %s", res.Raw)
	}
	if first, last := h.profileName(accountID); first != "PRIYA" || last != "NAIR" {
		t.Errorf("profile name = %q %q", first, last)
	}

	// Spent, so a second press is told nothing is owed — not billed again.
	if again := h.post(run, admin, map[string]any{}); again.Status != http.StatusConflict {
		t.Errorf("second run: want 409 nothing owed, got %d %s", again.Status, again.Raw)
	}
	if n := h.countReports(accountID); n != 1 {
		t.Errorf("exactly one report for one purchase, got %d", n)
	}

	// A customer cannot run anyone's owed report, their own included.
	if res := h.post(run, token, map[string]any{}); res.Status != http.StatusForbidden {
		t.Errorf("customer: want 403, got %d", res.Status)
	}
}

// owedRowsFor decodes the owed list (a bare array) and keeps one account's rows.
func owedRowsFor(t *testing.T, raw string, accountID int64) []map[string]any {
	t.Helper()
	var all []map[string]any
	if err := jsonUnmarshal(raw, &all); err != nil {
		t.Fatalf("decode owed list: %v (%s)", err, raw)
	}
	var out []map[string]any
	for _, r := range all {
		if id, _ := r["accountId"].(float64); int64(id) == accountID {
			out = append(out, r)
		}
	}
	return out
}

func jsonUnmarshal(raw string, v any) error { return json.Unmarshal([]byte(raw), v) }

// An admin can correct a customer's name from the user list — and that is
// enough to unblock a report the missing name was holding up.
func TestAdminCanSetANameAndItUnblocksTheOwedReport(t *testing.T) {
	h := newHarness(t)
	admin := h.makeAdmin("+919000000981")
	token, accountID := h.signInByPhone("+919000000982", "")

	h.buyPlan(token, "CREDIT_ANALYSIS")
	h.failPANCheck(token, "ANITA DESAI")
	if _, err := h.pool.Exec(h.baseCtx,
		`UPDATE kyc_records SET pan_name = NULL WHERE account_id = $1`, accountID); err != nil {
		t.Fatal(err)
	}
	h.approvePAN(admin, accountID)
	time.Sleep(300 * time.Millisecond) // the hook tries and fails for want of a name

	path := fmt.Sprintf("/api/admin/accounts/%d/name", accountID)
	for _, body := range []map[string]any{
		{"firstName": "ANITA"}, // the bureau needs both halves
		{"firstName": "  ", "lastName": "DESAI"},
	} {
		if res := h.do(http.MethodPatch, path, admin, body); res.Status != http.StatusBadRequest {
			t.Errorf("%v: want 400, got %d %s", body, res.Status, res.Raw)
		}
	}

	if res := h.do(http.MethodPatch, path, admin,
		map[string]any{"firstName": " Anita ", "lastName": "Desai"}); res.Status != http.StatusOK {
		t.Fatalf("set name: %d %s", res.Status, res.Raw)
	}
	if first, last := h.profileName(accountID); first != "Anita" || last != "Desai" {
		t.Errorf("name = %q %q, want it trimmed and saved", first, last)
	}

	// With a name, the owed report runs without one being supplied again.
	run := fmt.Sprintf("/api/admin/accounts/%d/run-owed-report", accountID)
	if res := h.post(run, admin, map[string]any{}); res.Status != http.StatusOK {
		t.Fatalf("run after naming: %d %s", res.Status, res.Raw)
	}

	// A customer cannot rename anyone, themselves included, through this route.
	if res := h.do(http.MethodPatch, path, token,
		map[string]any{"firstName": "X", "lastName": "Y"}); res.Status != http.StatusForbidden {
		t.Errorf("customer: want 403, got %d", res.Status)
	}
	if res := h.do(http.MethodPatch, "/api/admin/accounts/999999/name", admin,
		map[string]any{"firstName": "A", "lastName": "B"}); res.Status != http.StatusNotFound {
		t.Errorf("unknown account: want 404, got %d", res.Status)
	}
}
