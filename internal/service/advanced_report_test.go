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
		CreditHistorySince:     "2014-02-11",
		CreatedAt:              time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC),
		ActiveAccountCount:     4,
		LoanAccounts: []LoanAccount{
			{
				AccountNumber:       "12345918",
				LoanType:            "Home Loan",
				Company:             "State Bank of India",
				Active:              true,
				OpenedOn:            "2019-08-01",
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
				OpenedOn:      "2014-02-11",
				ClosedOn:      "2022-06-30",
				// Deliberately recent: the bureau reports ~36 months per
				// tradeline whatever year the account opened.
				PaymentHistory: []PaymentMonth{
					{Month: "2024-01", Status: "paid"},
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
		"HDFC Bank", // a closed one, on its own page
		"protect your score rather than raise it",           // the fixture's plan adds no points
		"This report is yours",                              // the closing page, so nothing truncated
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

// The file starts when its oldest account opened, not when the payment grid
// happens to begin.
//
// This is the production bug: the history page derived its date from payment
// history, the bureau reports roughly 36 months of it per tradeline, and a file
// open since February 2014 was printed to a real user as April 2018. The
// fixture keeps the two deliberately far apart — a 2014 account whose only
// reported month is 2024 — so the wrong source cannot pass.
func TestAdvancedReport_HistoryStartsWhenTheOldestAccountOpened(t *testing.T) {
	html := flatten(renderFull(t))

	if !strings.Contains(html, "February 2014") {
		t.Error("history page does not name the month the file starts")
	}
	if strings.Contains(html, "January 2024") || strings.Contains(html, "August 2019") {
		t.Error("history page dated the file from payment history or from an open account")
	}
	// The note names the account that anchors it, with the year it closed.
	if !strings.Contains(html, "A HDFC Bank account, closed in 2022") {
		t.Error("the anchoring account is not named with its closing year")
	}
	// And its chip carries the years it actually ran.
	if !strings.Contains(html, "2014–22") {
		t.Error("closed-account chip does not show the tradeline's own year range")
	}
}

// An account the bureau gave no dates for is named without an invented span.
func TestAdvancedReport_AnAccountWithNoDatesGetsNoYearRange(t *testing.T) {
	if got := accountYearRange("", ""); got != "" {
		t.Errorf("accountYearRange(\"\", \"\") = %q, want empty", got)
	}
	// Still open: the year it started, and nothing about an ending.
	if got := accountYearRange("2019-08-01", ""); got != "2019" {
		t.Errorf("open account range = %q, want 2019", got)
	}
	// Opened and closed inside one year reads as that year, not "2021-21".
	if got := accountYearRange("2021-03-05", "2021-11-30"); got != "2021" {
		t.Errorf("same-year range = %q, want 2021", got)
	}
}

// The projection is what the plan's own actions are worth, not the band the
// score already sits in.
//
// Reported from production: a file at 800 printed "WHERE THIS FILE CAN GO —
// 800 → 800–900". 900 is the top of the scale, and every action on a protect
// plan carries zero estimated points precisely because there is nothing left to
// fix, so the arrow pointed at a number nothing in the document supported.
func TestAdvancedReport_ProjectsWhatTheActionsAreWorth(t *testing.T) {
	in := fullInsights()
	lo, hi := 20, 35
	lo2, hi2 := 10, 15
	in.ScoreBuilder.Strategies = []BuilderStrategy{
		{Title: "Crush card utilisation below 30%", Tag: "fastest fix",
			EstimatedPointsMin: &lo, EstimatedPointsMax: &hi},
		{Title: "Keep every payment on time", Tag: "protect",
			EstimatedPointsMin: &lo2, EstimatedPointsMax: &hi2},
		{Title: "Dispute any report errors", Tag: "check"}, // unquantified: adds nothing
	}
	v := buildAdvancedReportView(&models.Account{}, in)

	// 822 + (20+10) … 822 + (35+15)
	if v.TargetRange != "852–872" {
		t.Errorf("TargetRange = %q, want 852–872 (the sum of the listed actions)", v.TargetRange)
	}
	if v.HoldNote != "" {
		t.Errorf("a plan with points to gain should project, not hold: %q", v.HoldNote)
	}
}

// A plan whose actions are all protective says so instead of drawing an arrow.
func TestAdvancedReport_APlanWithNothingToGainSaysSo(t *testing.T) {
	in := fullInsights()
	in.ScoreBuilder.Strategies = []BuilderStrategy{
		{Title: "Autopay every bill to protect your streak", Tag: "protect"},
		{Title: "Claim a lifetime-free premium card", Tag: "perk"},
	}
	v := buildAdvancedReportView(&models.Account{}, in)

	if v.TargetRange != "822" {
		t.Errorf("TargetRange = %q, want the score itself", v.TargetRange)
	}
	if v.HoldNote == "" {
		t.Error("expected a line saying these actions protect rather than raise")
	}
	if v.ProjectionKicker == "" || v.ProjectionKicker == "Where this file can go" {
		t.Errorf("kicker still promises movement: %q", v.ProjectionKicker)
	}
}

// The scale ends at 900, and a sum that runs past it is arithmetic escaping the
// thing it describes.
func TestAdvancedReport_AProjectionIsClampedToTheTopOfTheScale(t *testing.T) {
	if got := scoreRange(880, 960); got != "880–900" {
		t.Errorf("scoreRange(880, 960) = %q, want 880–900", got)
	}
	if got := scoreRange(905, 950); got != "900" {
		t.Errorf("scoreRange(905, 950) = %q, want 900", got)
	}
}

// The "still anchors your credit age" note belongs to the account that actually
// anchors it. A closed account that opened after some still-open one anchors
// nothing, and the file says so two lines above.
func TestAdvancedReport_TheAnchorNoteNamesTheOldestAccountOrNobody(t *testing.T) {
	in := fullInsights()
	// An open account older than every closed one — so no closed account anchors.
	in.CreditHistorySince = "2011-03-04"
	in.LoanAccounts = append(in.LoanAccounts, LoanAccount{
		Company:  "Kotak Mahindra Bank",
		LoanType: "Credit Card",
		Active:   true,
		OpenedOn: "2011-03-04",
	})
	v := buildAdvancedReportView(&models.Account{}, in)

	if v.HistorySince != "March 2011" {
		t.Errorf("HistorySince = %q, want March 2011", v.HistorySince)
	}
	if v.HistoryNote != "" {
		t.Errorf("a closed account that is not the oldest claimed the anchor: %q", v.HistoryNote)
	}
}
