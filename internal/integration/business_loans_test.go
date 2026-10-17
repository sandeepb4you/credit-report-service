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

	// No report yet: still lenders to show, and no status to show with them —
	// except the unscreened one, which is open with or without a score.
	before := status()
	if before["matchStatus"] != "no_score" || before["score"] != nil {
		t.Errorf("before any report: %v", before)
	}
	lenders := listOf(before["lenders"])
	if len(lenders) != 2 {
		t.Fatalf("lenders = %v", lenders)
	}
	if lenders[0]["name"] != "Muthoot FinCorp" || lenders[0]["offerUrl"] != "https://lender.example/apply" ||
		lenders[0]["matchStatus"] != "no_score" {
		t.Errorf("screened lender = %v", lenders[0])
	}
	if lenders[1]["name"] != "mPokket" || lenders[1]["offerUrl"] != "https://open-lender.example/apply" ||
		lenders[1]["matchStatus"] != "open" {
		t.Errorf("open lender = %v", lenders[1])
	}

	// A score the screened lender turns away leaves the unscreened card open:
	// the two verdicts are independent, which is the whole point of carrying
	// one per lender rather than one for the list.
	h.insertScoredReport(accountID, 628)
	rejected := status()
	if got := rejected["matchStatus"]; got != "not_match" {
		t.Errorf("628: %v", got)
	}
	turned := listOf(rejected["lenders"])
	if turned[0]["matchStatus"] != "not_match" || turned[1]["matchStatus"] != "open" {
		t.Errorf("628 per lender: %v", turned)
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
