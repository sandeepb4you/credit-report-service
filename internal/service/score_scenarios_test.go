package service

import (
	"encoding/json"
	"testing"
)

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
		})
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
	for _, f := range []string{
		"score_500_poor.json", "score_600_fair.json",
		"score_700_good.json", "score_800_excellent.json",
	} {
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
	for _, f := range []string{
		"score_500_poor.json", "score_600_fair.json",
		"score_700_good.json", "score_800_excellent.json",
	} {
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
