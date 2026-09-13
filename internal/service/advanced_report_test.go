package service

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"credit-report-service/internal/models"
)

// The advanced report is a document about somebody's finances, so what it must
// never do is state something it does not know. These assert the two halves of
// that: a full file produces every section with the right figures, and a thin
// one drops the sections it cannot fill rather than printing a blank or a zero.
//
// Rendered as HTML here — no browser. The PDF step is Chromium printing this
// document; what is worth pinning is the document.

// flatten collapses runs of whitespace so a prose assertion is not defeated by
// where the template happens to wrap a line.
var wsRun = regexp.MustCompile(`\s+`)

func flatten(html string) string { return wsRun.ReplaceAllString(html, " ") }

func strptr(s string) *string { return &s }
func i64ptr(v int64) *int64   { return &v }

func fullInsights() *ReportInsights {
	return &ReportInsights{
		ReportID:               42,
		CreditScore:            i64ptr(822),
		CardUtilizationPercent: 19,
		CreatedAt:              time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC),
		ActiveAccountCount:     4,
		LoanAccounts: []LoanAccount{
			{
				AccountNumber:       "12345918",
				LoanType:            "Home Loan",
				Company:             "State Bank of India",
				Active:              true,
				InterestRatePercent: 7.4,
				CurrentBalance:      11400000,
				PaymentHistory: []PaymentMonth{
					{Month: "2026-01", Status: "paid"},
					{Month: "2026-02", Status: "paid"},
					{Month: "2025-12", Status: "paid"},
				},
			},
			{
				AccountNumber:  "99994150",
				LoanType:       "Credit Card",
				Company:        "IDFC FIRST Bank",
				Active:         true,
				CurrentBalance: 116000,
				PaymentHistory: []PaymentMonth{{Month: "2026-01", Status: "paid"}},
			},
			{
				AccountNumber: "77770001",
				LoanType:      "Credit Card",
				Company:       "HDFC Bank",
				Active:        false,
				PaymentHistory: []PaymentMonth{
					{Month: "2014-02", Status: "paid"},
				},
			},
		},
		ReportCard: &ReportCard{
			OverallGrade: "A+",
			Factors: []CardFactor{
				{Name: "Payment history", Weight: 35, Grade: "A+", Summary: "No missed payments"},
				{Name: "Credit utilisation", Weight: 30, Grade: "A", Summary: "19% of your limit"},
			},
		},
		ScoreBuilder: &ScoreBuilder{
			Journey:           "protect",
			TargetScoreMin:    835,
			TargetScoreMax:    845,
			TimelineMonthsMax: 6,
			Strategies: []BuilderStrategy{
				{Title: "Keep the Auto Loan streak alive", Detail: "37 months and counting", Tag: "protect"},
				{Title: "Hold utilisation under 30%", Tag: "protect"},
			},
		},
	}
}

func renderFull(t *testing.T) string {
	t.Helper()
	svc := &CreditAnalyticsService{}
	acc := &models.Account{FirstName: strptr("Tandava"), LastName: strptr("Aradhyula")}
	html, err := svc.renderAdvancedHTML(acc, fullInsights())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return html
}

func TestAdvancedReport_RendersTheWholeDocument(t *testing.T) {
	html := flatten(renderFull(t))

	for _, want := range []string{
		"Tandava Aradhyula",   // the holder, on every running head
		"822",                 // the score
		"EXCELLENT",           // its band
		"5 September 2026",    // the day the report was pulled, not today
		"Payment history",     // a factor row
		"State Bank of India", // an open account
		"₹1.14 Cr",            // its balance, in the document's own units
		"IDFC FIRST Bank",
		"HDFC Bank",            // a closed one, on its own page
		"835",                  // the projection
		"This report is yours", // the closing page, so nothing truncated
		"not a credit information company or credit bureau", // the legal position
	} {
		if !strings.Contains(html, want) {
			t.Errorf("document is missing %q", want)
		}
	}
}

// The document shows account numbers as the last four digits. A full number in
// a file people forward is exactly what the masking is for.
func TestAdvancedReport_NeverPrintsAFullAccountNumber(t *testing.T) {
	html := flatten(renderFull(t))

	for _, full := range []string{"12345918", "99994150", "77770001"} {
		if strings.Contains(html, full) {
			t.Errorf("full account number %q reached the document", full)
		}
	}
	if !strings.Contains(html, "····5918") {
		t.Error("expected the masked last four digits")
	}
}

// A thin file is a normal bureau answer, not an error: no score, no per-account
// detail, no factor grades. Every section that has nothing to say must be
// absent, because an empty row in a financial document reads as a fact.
func TestAdvancedReport_OmitsWhatItCannotFill(t *testing.T) {
	svc := &CreditAnalyticsService{}
	acc := &models.Account{}
	html, err := svc.renderAdvancedHTML(acc, &ReportInsights{
		ReportID:  7,
		CreatedAt: time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	html = flatten(html)

	for _, absent := range []string{
		"carrying you forward",      // open accounts
		"settled and behind you",    // closed accounts
		"not one missed",            // the payment grid's headline
		"in three numbers",          // the highlights page
		"Projections are estimates", // the projection page's footer
	} {
		if strings.Contains(html, absent) {
			t.Errorf("thin report still rendered %q", absent)
		}
	}
	// What must survive: the cover, addressed to nobody rather than to "", and
	// the closing page with the legal position on it.
	for _, want := range []string{"Your report", "Your Experian score", "This report is yours", "—"} {
		if !strings.Contains(html, want) {
			t.Errorf("thin report is missing %q", want)
		}
	}
}

// The cover line is chosen, not composed, and a weak file is met with the plan
// rather than a verdict — the design's rule that low scores get warm amber and
// never a shaming red.
func TestAdvancedReport_CoverNeverShamesALowScore(t *testing.T) {
	for score, want := range map[int]string{
		822: "A file most lenders rarely see.",
		770: "A strong file, with room at the top.",
		690: "A solid base to build on.",
		540: "Every one of these is fixable.",
	} {
		if got := coverHeadlineFor(score); got != want {
			t.Errorf("coverHeadlineFor(%d) = %q, want %q", score, got, want)
		}
	}
}

// The streak stops at the first month anyone was late, and a month nobody
// reported is not a missed one.
func TestAdvancedReport_StreakStopsAtTheFirstMiss(t *testing.T) {
	accounts := []LoanAccount{{
		PaymentHistory: []PaymentMonth{
			{Month: "2026-03", Status: "paid"},
			{Month: "2026-02", Status: "not_reported"},
			{Month: "2026-01", Status: "paid"},
			{Month: "2025-12", Status: "delayed", DaysLate: 30},
			{Month: "2025-11", Status: "paid"},
		},
	}}
	if got := onTimeStreak(accounts); got != 2 {
		t.Errorf("onTimeStreak = %d, want 2 (a gap breaks nothing, a late month does)", got)
	}
}
