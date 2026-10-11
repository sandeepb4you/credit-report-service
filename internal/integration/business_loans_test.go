package integration

import (
	"net/http"
	"testing"
)

// The list is shown to everyone; the status follows the newest scored report,
// and nothing in the response says which rule decided it.
func TestBusinessLoanListFollowsTheLatestReport(t *testing.T) {
	h := newHarness(t)
	token, accountID := h.signInByPhone("+919000001101", "")

	status := func() map[string]any {
		t.Helper()
		r := h.get("/api/loans/business", token)
		if r.Status != http.StatusOK {
			t.Fatalf("list: %d %s", r.Status, r.Raw)
		}
		return r.Body
	}

	// No report yet: still a lender to show, and no status to show with it.
	before := status()
	if before["matchStatus"] != "no_score" || before["score"] != nil {
		t.Errorf("before any report: %v", before)
	}
	lenders := listOf(before["lenders"])
	if len(lenders) != 1 || lenders[0]["name"] != "Muthoot FinCorp" || lenders[0]["offerUrl"] != "https://lender.example/apply" {
		t.Errorf("lenders = %v", lenders)
	}

	h.insertScoredReport(accountID, 628)
	if got := status()["matchStatus"]; got != "not_match" {
		t.Errorf("628: %v", got)
	}
	h.insertScoredReport(accountID, 702)
	if got := status()["matchStatus"]; got != "good" {
		t.Errorf("702: %v", got)
	}
	h.insertScoredReport(accountID, 812)
	after := h.get("/api/loans/business", token)
	if after.Body["matchStatus"] != "excellent" || after.Body["score"] != float64(812) {
		t.Errorf("812: %s", after.Raw)
	}
	// The status and nothing about how it was reached.
	for k := range after.Body {
		if k != "matchStatus" && k != "score" && k != "lenders" {
			t.Errorf("unexpected field %q: %s", k, after.Raw)
		}
	}

	if r := h.get("/api/loans/business", ""); r.Status != http.StatusUnauthorized {
		t.Errorf("signed out: want 401, got %d", r.Status)
	}
}
