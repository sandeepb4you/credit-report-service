package integration

import (
	"net/http"
	"testing"
)

// A bureau pull that finds nothing still produces a report row, and this endpoint
// still answers 200 for it — with a real reportId and a NULL score.
//
// That contract is load-bearing for the app. Home distinguishes four states off
// it: a score, a load failure, a report with no score, and no report at all. The
// last two used to look identical from the client's side, so a user whose paid
// pull came back "no record found" was shown the paywall — "your credit score is
// waiting", with a Pay button — inviting them to buy the same empty answer again.
//
// If this endpoint ever starts 404ing or erroring for a scoreless report, Home
// falls back to the paywall and that bug returns. Hence the assertions on the
// exact shape rather than just the status.
func TestLatestInsights_ScorelessReportIsTwoHundredWithANullScore(t *testing.T) {
	h := newHarness(t)

	token, accountID := h.signInByPhone(joinerPhone, "")
	reportID := h.insertNoRecordReport(accountID)

	res := h.get("/api/credit-analytics/latest-insights", token)
	if res.Status != http.StatusOK {
		t.Fatalf("latest-insights: %d %s, want 200 — a scoreless report is not a missing one",
			res.Status, res.Raw)
	}

	if got := int64(intOf(res.Body["reportId"])); got != reportID {
		t.Errorf("reportId = %d, want %d", got, reportID)
	}
	if score, present := res.Body["creditScore"]; !present || score != nil {
		t.Errorf("creditScore = %v, want an explicit null: the client tells "+
			"'no score on file' from 'no report' by this field", score)
	}
}

// The other half of the pair: an account with no report at all must 404, which is
// the ONE case the paywall is the honest thing to show.
func TestLatestInsights_NoReportAtAllIsNotFound(t *testing.T) {
	h := newHarness(t)

	token, _ := h.signInByPhone(joinerPhone, "")

	res := h.get("/api/credit-analytics/latest-insights", token)
	if res.Status != http.StatusNotFound {
		t.Errorf("latest-insights with no report: %d %s, want 404", res.Status, res.Raw)
	}
}

// A scored report must not be confused with a scoreless one in the other
// direction either.
func TestLatestInsights_ScoredReportCarriesItsScore(t *testing.T) {
	h := newHarness(t)

	token, accountID := h.signInByPhone(joinerPhone, "")
	h.insertScoredReport(accountID, 772)

	res := h.get("/api/credit-analytics/latest-insights", token)
	if res.Status != http.StatusOK {
		t.Fatalf("latest-insights: %d %s", res.Status, res.Raw)
	}
	if got := intOf(res.Body["creditScore"]); got != 772 {
		t.Errorf("creditScore = %d, want 772", got)
	}
}
