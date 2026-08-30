package service

import (
	"encoding/json"
	"testing"
)

// allGeneratedFixtures is every hand-built report under
// internal/digitap/testdata. The whole-set invariants below run over all of
// them, so a scenario added to the generator without a case in either table
// above is still held to the rules every report has to obey.
var allGeneratedFixtures = []string{
	"score_500_poor.json", "score_600_fair.json",
	"score_700_good.json", "score_800_excellent.json",
	"boundary_650_blended.json", "boundary_750_protect.json",
	"all_accounts_closed_720.json", "card_only_680.json",
	"high_utilisation_clean_640.json",
}

// Guards the four score-band scenario fixtures in internal/digitap/testdata
// (score_800_excellent, score_700_good, score_600_fair, score_500_poor).
//
// They exist so every score band can be demoed and regression-tested without a
// live bureau pull, and their whole value is that the derived report card
// matches the headline score: a 500 whose factors all grade A reads as a bug in
// the app, not as an unusual customer. These tests pin that agreement, so a
// change to the grading thresholds shows up here rather than in a screenshot.
//
// They are generated (see the loader SQL in docs/examples) rather than
// hand-written, so an assertion failing here usually means a threshold moved,
// not that a file was mistyped.

// scenarioWant is the shape a fixture must parse into.
type scenarioWant struct {
	file  string
	score int64

	overallGrade string
	// factorGrades keyed by factor name; every listed factor must be present
	// with that grade. Naming them individually is the point — the overall
	// grade alone would let two factors move in opposite directions unnoticed.
	factorGrades map[string]string

	totalAccounts  int64
	activeAccounts int64
	derogatory     int

	onTimePercent   float64
	utilizationPct  float64
	enquiries180    int64
	outstanding     float64
	monthlyEMI      float64
	interestPerYear float64
}

func TestScoreBandScenarios(t *testing.T) {
	cases := []scenarioWant{
		{
			// Long, clean, low-utilisation file: the "protect what you have"
			// persona. Every factor at the top of its band.
			file:         "score_800_excellent.json",
			score:        800,
			overallGrade: "A+",
			factorGrades: map[string]string{
				"Payment history":    "A+",
				"Credit utilisation": "A+",
				"Credit age":         "A+",
				"Enquiries":          "A+",
				"Credit mix":         "A+",
			},
			totalAccounts:  4,
			activeAccounts: 2,
			derogatory:     0,
			onTimePercent:  100,
			utilizationPct: 5.6,
			enquiries180:   0,
			outstanding:    3148000,
			monthlyEMI:     55900,
			// 3,120,000 @ 8.35% + 28,000 @ 42%
			interestPerYear: 272280,
		},
		{
			// Solid borrower with real blemishes: a few late months and a card
			// running in the thirties. The blended journey.
			file:         "score_700_good.json",
			score:        700,
			overallGrade: "B",
			factorGrades: map[string]string{
				"Payment history":    "B",
				"Credit utilisation": "B",
				"Credit age":         "A",
				"Enquiries":          "B",
				"Credit mix":         "A",
			},
			totalAccounts:  3,
			activeAccounts: 2,
			derogatory:     0,
			onTimePercent:  94.4,
			utilizationPct: 36.8,
			enquiries180:   3,
			outstanding:    414000,
			monthlyEMI:     13950,
			// 92,000 @ 41.88% + 322,000 @ 15.25%
			interestPerYear: 87634.6,
		},
		{
			// Stressed but NOT defaulted. The distinction matters: with no
			// write-offs the rebuild plan can still open with something true and
			// reassuring, which is the whole design of the low-score screens.
			file:         "score_600_fair.json",
			score:        600,
			overallGrade: "C",
			factorGrades: map[string]string{
				"Payment history":    "C",
				"Credit utilisation": "D",
				"Credit age":         "B",
				"Enquiries":          "C",
				"Credit mix":         "A",
			},
			totalAccounts:  3,
			activeAccounts: 2,
			derogatory:     0,
			onTimePercent:  81.5,
			utilizationPct: 78.7,
			enquiries180:   5,
			outstanding:    304000,
			monthlyEMI:     11080,
			// 118,000 @ 43.2% + 186,000 @ 19.75%
			interestPerYear: 87711,
		},
		{
			// Derogatory: one written-off loan, one settled account, a maxed
			// card. This is the fixture that exercises the paths a clean file
			// never reaches.
			file:         "score_500_poor.json",
			score:        500,
			overallGrade: "D",
			factorGrades: map[string]string{
				"Payment history":    "F",
				"Credit utilisation": "D",
				"Credit age":         "C",
				"Enquiries":          "D",
				"Credit mix":         "A",
			},
			totalAccounts:  3,
			activeAccounts: 2,
			derogatory:     2,
			onTimePercent:  48.1,
			utilizationPct: 99.2,
			enquiries180:   9,
			// A written-off loan is still owed, so its balance stays in the
			// outstanding total: 59,500 card + 143,500 written off.
			outstanding: 203000,
			monthlyEMI:  9850,
			// 59,500 @ 45.6% + 143,500 @ 26.5%
			interestPerYear: 65159.5,
		},
	}

	for _, want := range cases {
		t.Run(want.file, func(t *testing.T) {
			assertScenario(t, want)
		})
	}
}

// assertScenario checks one fixture against the shape it is supposed to have.
// Shared by both tables so a band scenario and an edge case cannot drift into
// being checked to different standards.
func assertScenario(t *testing.T, want scenarioWant) {
	t.Helper()

	result, code := loadEnvelope(t, want.file)
	if code == nil || *code != 101 {
		t.Fatalf("result_code = %v, want 101", code)
	}
	ins := parseEnvelopeResult(t, result)

	// The headline the whole fixture is built around, lifted the same
	// way the service lifts it when it stores the row.
	gotScore := extractBureauScore(result)
	if gotScore == nil || *gotScore != want.score {
		t.Errorf("BureauScore = %v, want %d", gotScore, want.score)
	}

	if ins.TotalAccountCount != want.totalAccounts {
		t.Errorf("TotalAccountCount = %d, want %d", ins.TotalAccountCount, want.totalAccounts)
	}
	if ins.ActiveAccountCount != want.activeAccounts {
		t.Errorf("ActiveAccountCount = %d, want %d", ins.ActiveAccountCount, want.activeAccounts)
	}
	if ins.DerogatoryAccounts != want.derogatory {
		t.Errorf("DerogatoryAccounts = %d, want %d", ins.DerogatoryAccounts, want.derogatory)
	}
	if ins.OnTimePaymentPercent == nil {
		t.Fatalf("OnTimePaymentPercent is nil; every scenario reports payment history")
	}
	if got := *ins.OnTimePaymentPercent; !closeEnough(got, want.onTimePercent) {
		t.Errorf("OnTimePaymentPercent = %.1f, want %.1f", got, want.onTimePercent)
	}
	if !closeEnough(ins.CardUtilizationPercent, want.utilizationPct) {
		t.Errorf("CardUtilizationPercent = %.1f, want %.1f",
			ins.CardUtilizationPercent, want.utilizationPct)
	}
	if ins.EnquiryCount180Days != want.enquiries180 {
		t.Errorf("EnquiryCount180Days = %d, want %d", ins.EnquiryCount180Days, want.enquiries180)
	}
	if !closeEnough(ins.TotalOutstandingAmount, want.outstanding) {
		t.Errorf("TotalOutstandingAmount = %.2f, want %.2f",
			ins.TotalOutstandingAmount, want.outstanding)
	}
	if !closeEnough(ins.MonthlyEMI, want.monthlyEMI) {
		t.Errorf("MonthlyEMI = %.2f, want %.2f", ins.MonthlyEMI, want.monthlyEMI)
	}
	if !closeEnough(ins.InterestPaidPerYear, want.interestPerYear) {
		t.Errorf("InterestPaidPerYear = %.2f, want %.2f",
			ins.InterestPaidPerYear, want.interestPerYear)
	}

	if ins.ReportCard == nil {
		t.Fatalf("no report card")
	}
	if ins.ReportCard.OverallGrade != want.overallGrade {
		t.Errorf("OverallGrade = %q, want %q", ins.ReportCard.OverallGrade, want.overallGrade)
	}
	got := map[string]string{}
	for _, f := range ins.ReportCard.Factors {
		got[f.Name] = f.Grade
	}
	for name, grade := range want.factorGrades {
		if got[name] != grade {
			t.Errorf("factor %q graded %q, want %q", name, got[name], grade)
		}
	}
}

// The scenarios are meant to be read as a progression, so the ordering between
// them is a property in its own right: a change that leaves each file passing
// its own case but crosses two bands over would slip through otherwise.
func TestScoreBandScenarios_FormAProgression(t *testing.T) {
	files := []string{
		"score_500_poor.json",
		"score_600_fair.json",
		"score_700_good.json",
		"score_800_excellent.json",
	}

	var prevOnTime, prevUtil float64
	var prevEnquiries int64 = 1 << 30
	for i, f := range files {
		result, _ := loadEnvelope(t, f)
		ins := parseEnvelopeResult(t, result)

		onTime := *ins.OnTimePaymentPercent
		if i > 0 {
			if onTime <= prevOnTime {
				t.Errorf("%s on-time %.1f%% is not above the previous band's %.1f%%",
					f, onTime, prevOnTime)
			}
			if ins.CardUtilizationPercent >= prevUtil {
				t.Errorf("%s utilisation %.1f%% is not below the previous band's %.1f%%",
					f, ins.CardUtilizationPercent, prevUtil)
			}
			if ins.EnquiryCount180Days >= prevEnquiries {
				t.Errorf("%s has %d enquiries, not fewer than the previous band's %d",
					f, ins.EnquiryCount180Days, prevEnquiries)
			}
		}
		prevOnTime, prevUtil, prevEnquiries = onTime, ins.CardUtilizationPercent, ins.EnquiryCount180Days
	}
}

// Every tradeline must carry a month-by-month history, because the payment
// heatmap (S23) renders it directly and an empty strip is the one failure that
// looks like a styling bug rather than missing data.
func TestScoreBandScenarios_EveryAccountHasPaymentHistory(t *testing.T) {
	for _, f := range allGeneratedFixtures {
		result, _ := loadEnvelope(t, f)
		ins := parseEnvelopeResult(t, result)
		for _, acct := range ins.LoanAccounts {
			if len(acct.PaymentHistory) == 0 {
				t.Errorf("%s: %s (%s) has no payment history", f, acct.Company, acct.LoanType)
			}
		}
	}
}

func closeEnough(got, want float64) bool {
	d := got - want
	if d < 0 {
		d = -d
	}
	return d < 0.05
}

// A lender stops reporting the month it closes an account, so a tradeline
// settled in 2023 must not carry rows up to last month.
//
// Worth pinning because the generator counts history backwards from an anchor,
// and defaulting that anchor to the report date for every account is the easy
// mistake: it inflates the on-time months on a file whose closed accounts were
// its clean ones, and the payment heatmap draws a closed loan as still live.
func TestScoreBandScenarios_ClosedAccountsStopReportingAtClosure(t *testing.T) {
	for _, f := range allGeneratedFixtures {
		result, _ := loadEnvelope(t, f)

		var doc struct {
			ResultJSON struct {
				INProfileResponse struct {
					CAISAccount struct {
						Details []struct {
							Subscriber string  `json:"Subscriber_Name"`
							DateClosed *string `json:"Date_Closed"`
							History    []struct {
								Month string `json:"Month"`
								Year  string `json:"Year"`
							} `json:"CAIS_Account_History"`
						} `json:"CAIS_Account_DETAILS"`
					} `json:"CAIS_Account"`
				} `json:"INProfileResponse"`
			} `json:"result_json"`
		}
		if err := json.Unmarshal(result, &doc); err != nil {
			t.Fatalf("%s: %v", f, err)
		}

		for _, acct := range doc.ResultJSON.INProfileResponse.CAISAccount.Details {
			if acct.DateClosed == nil || *acct.DateClosed == "" {
				continue
			}
			closed := (*acct.DateClosed)[:6] // YYYYMM
			for _, h := range acct.History {
				got := h.Year + h.Month
				if got > closed {
					t.Errorf("%s: %s closed %s but reports %s",
						f, acct.Subscriber, closed, got)
				}
			}
		}
	}
}

// Edge-case fixtures, each covering a path the score-band set does not reach.
// Values here were measured from the real parser, not predicted.
func TestEdgeCaseScenarios(t *testing.T) {
	cases := []scenarioWant{
		{
			// The exact rebuild/blended boundary. buildScoreBuilder switches
			// journey at <650 vs 650-749, and nothing else in the set sits on
			// the edge, so an off-by-one there would go unnoticed.
			//
			// The journey itself is not asserted here: parseReportInsights does
			// not populate ScoreBuilder or CreditScore — those are filled in
			// higher up from the stored row — so what this pins is that the
			// fixture really does carry 650, which is what makes it usable for
			// the boundary check by hand or in a demo.
			file:         "boundary_650_blended.json",
			score:        650,
			overallGrade: "B",
			factorGrades: map[string]string{
				"Payment history": "B", "Credit utilisation": "B",
				"Credit age": "B", "Enquiries": "B", "Credit mix": "A",
			},
			totalAccounts: 3, activeAccounts: 2, derogatory: 0,
			onTimePercent: 88.1, utilizationPct: 44.0, enquiries180: 4,
			outstanding: 298000, monthlyEMI: 11500,
			// 88,000 @ 42% + 210,000 @ 16.5%
			interestPerYear: 71610,
		},
		{
			// The blended/protect boundary at the other end.
			file:         "boundary_750_protect.json",
			score:        750,
			overallGrade: "A",
			factorGrades: map[string]string{
				"Payment history": "A", "Credit utilisation": "A",
				"Credit age": "A", "Enquiries": "A", "Credit mix": "A",
			},
			totalAccounts: 3, activeAccounts: 2, derogatory: 0,
			onTimePercent: 99.0, utilizationPct: 18.0, enquiries180: 1,
			outstanding: 2734000, monthlyEMI: 37200,
			// 2,680,000 @ 8.75% + 54,000 @ 41%
			interestPerYear: 256640,
		},
		{
			// Nothing live on the file. Outstanding, EMI and interest are all
			// zero, which several screens have to render without dividing by
			// anything.
			//
			// Utilisation is 0%, not absent: the parser measures revolving
			// limits BEFORE it checks whether the account is open, so a closed
			// card still contributes its limit. Worth pinning, because the
			// obvious reading of that loop is that it would not.
			file:         "all_accounts_closed_720.json",
			score:        720,
			overallGrade: "A+",
			factorGrades: map[string]string{
				"Payment history": "A+", "Credit utilisation": "A+",
				"Credit age": "A+", "Enquiries": "A+", "Credit mix": "A",
			},
			totalAccounts: 3, activeAccounts: 0, derogatory: 0,
			onTimePercent: 100, utilizationPct: 0, enquiries180: 0,
			outstanding: 0, monthlyEMI: 0, interestPerYear: 0,
		},
		{
			// One product type, so credit mix grades C — the common shape for a
			// young borrower and otherwise untested. Both accounts are cards, so
			// there is no EMI either.
			file:         "card_only_680.json",
			score:        680,
			overallGrade: "B",
			factorGrades: map[string]string{
				"Payment history": "B", "Credit utilisation": "A",
				"Credit age": "A", "Enquiries": "A", "Credit mix": "C",
			},
			totalAccounts: 2, activeAccounts: 2, derogatory: 0,
			onTimePercent: 91.7, utilizationPct: 24.8, enquiries180: 2,
			outstanding: 57000, monthlyEMI: 0,
			// 42,000 @ 42% + 15,000 @ 43.2%
			interestPerYear: 24120,
		},
		{
			// Every payment on time for three years, and a card at 92%.
			//
			// The overall grade stays at B while utilisation alone is a D, which
			// is the case worth having: a headline that reads "fine" over a
			// single severe factor is exactly when the per-factor breakdown has
			// to be the thing the user is shown.
			file:         "high_utilisation_clean_640.json",
			score:        640,
			overallGrade: "B",
			factorGrades: map[string]string{
				"Payment history": "A+", "Credit utilisation": "D",
				"Credit age": "A", "Enquiries": "A+", "Credit mix": "B",
			},
			totalAccounts: 2, activeAccounts: 2, derogatory: 0,
			onTimePercent: 100, utilizationPct: 92.0, enquiries180: 0,
			outstanding: 382000, monthlyEMI: 11400,
			// 92,000 @ 44% + 290,000 @ 13.75%
			interestPerYear: 80355,
		},
	}

	for _, want := range cases {
		t.Run(want.file, func(t *testing.T) {
			assertScenario(t, want)
		})
	}
}
