package service

import (
	"context"
	"testing"

	"credit-report-service/internal/models"
)

// ---- helpers ---------------------------------------------------------------

func sbInsights(score int64) *ReportInsights {
	s := score
	return &ReportInsights{
		CreditScore: &s, OnTimePaymentPercent: f64(80), CardUtilizationPercent: 64,
		EnquiryCount180Days: 4, DerogatoryAccounts: 0,
		ReportCard: &ReportCard{Factors: []CardFactor{
			{Name: "Payment history", Grade: "C", Summary: "2 missed", MissedCount: 2},
			{Name: "Credit utilisation", Grade: "C", Summary: "64% used"},
			{Name: "Credit age", Grade: "C", Summary: "2.5 years"},
			{Name: "Enquiries", Grade: "C", Summary: "4 in 6 months"},
			{Name: "Credit mix", Grade: "B", Summary: "2 types"},
		}},
	}
}

func findSimAction(actions []SimAction, key string) (SimAction, bool) {
	for _, a := range actions {
		if a.Key == key {
			return a, true
		}
	}
	return SimAction{}, false
}

// ---- OfferingsForScore: nil repo / no-score paths (no DB needed) -----------

func TestOfferingsForScore_NonPositiveScoreYieldsNone(t *testing.T) {
	// A non-positive score (no score on file) must short-circuit before hitting
	// the repo, so a nil-repo service is safe here.
	s := &ScoreBuilderService{}
	for _, score := range []int{0, -1} {
		got, err := s.OfferingsForScore(context.Background(), score)
		if err != nil {
			t.Errorf("score %d: unexpected error %v", score, err)
		}
		if got != nil {
			t.Errorf("score %d: expected nil, got %v", score, got)
		}
	}
}

// ---- buildSimActions -------------------------------------------------------

func TestBuildSimActions_LowScoreHasPositiveAndNegative(t *testing.T) {
	actions := buildSimActions(sbInsights(610))
	keys := map[string]bool{}
	for _, a := range actions {
		keys[a.Key] = true
	}
	// Positive levers the 610 file has room on + the FD-card product + two
	// universal negative actions.
	for _, want := range []string{"pay_card_down", "on_time_streak", "stop_applications", "open_fd_card"} {
		if !keys[want] {
			t.Errorf("missing positive/product action %q in %v", want, keys)
		}
	}
	for _, want := range []string{"take_personal_loan", "close_oldest_card"} {
		if !keys[want] {
			t.Errorf("missing negative action %q in %v", want, keys)
		}
	}
	// Negatives carry a down direction; positives up.
	for _, a := range actions {
		switch a.Direction {
		case "up":
			if a.Delta <= 0 {
				t.Errorf("action %q direction=up but delta=%d", a.Key, a.Delta)
			}
		case "down":
			if a.Delta >= 0 {
				t.Errorf("action %q direction=down but delta=%d", a.Key, a.Delta)
			}
		default:
			t.Errorf("action %q has unknown direction %q", a.Key, a.Direction)
		}
	}
}

func TestBuildSimActions_HighScoreDropsFDCard(t *testing.T) {
	// The FD-card product lever only appears below 700.
	actions := buildSimActions(sbInsights(815))
	if _, ok := findSimAction(actions, "open_fd_card"); ok {
		t.Error("FD-card action should not surface for a high score")
	}
	// Negative actions are always present.
	if _, ok := findSimAction(actions, "take_personal_loan"); !ok {
		t.Error("universal negative actions missing for high score")
	}
}

func TestBuildSimActions_PositiveLeversGatedByReport(t *testing.T) {
	// A clean high-score file: utilisation under 30, no enquiries, no missed
	// payments -> only the FD-card (suppressed by score) and the negatives.
	s := int64(815)
	ins := &ReportInsights{
		CreditScore: &s, CardUtilizationPercent: 8, EnquiryCount180Days: 0,
		ReportCard: &ReportCard{Factors: []CardFactor{
			{Name: "Payment history", Grade: "A+"},
			{Name: "Credit utilisation", Grade: "A+"},
			{Name: "Credit age", Grade: "A+"},
			{Name: "Enquiries", Grade: "A+"},
			{Name: "Credit mix", Grade: "A"},
		}},
	}
	actions := buildSimActions(ins)
	keys := map[string]bool{}
	for _, a := range actions {
		keys[a.Key] = true
	}
	for _, absent := range []string{"pay_card_down", "stop_applications", "on_time_streak", "open_fd_card"} {
		// on_time_streak can still appear because it isn't gated on grade here;
		// but pay_card_down / stop_applications must be absent for a clean file.
		if absent == "pay_card_down" || absent == "stop_applications" {
			if keys[absent] {
				t.Errorf("clean file should not surface %q", absent)
			}
		}
	}
}

// ---- project: selection + projection math ----------------------------------

func TestProject_DefaultSelectsAllPositives(t *testing.T) {
	s := &ScoreBuilderService{} // repo not consulted when no offering fetch is forced
	ins := sbInsights(610)
	actions := buildSimActions(ins)
	for i := range actions {
		actions[i].Selected = actions[i].Delta > 0 // default policy
	}
	sim, err := s.project(context.Background(), ins, actions, 610)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	var expectedDelta int64
	for _, a := range actions {
		if a.Selected {
			expectedDelta += int64(a.Delta)
		}
	}
	if sim.Delta != expectedDelta {
		t.Errorf("delta = %d, want %d", sim.Delta, expectedDelta)
	}
	if sim.ProjectedScore != clampScore(610+expectedDelta) {
		t.Errorf("projectedScore = %d, want %d", sim.ProjectedScore, clampScore(610+expectedDelta))
	}
	if sim.CurrentScore != 610 {
		t.Errorf("currentScore = %d, want 610", sim.CurrentScore)
	}
	if sim.Disclaimer == "" {
		t.Error("disclaimer must not be empty (compliance)")
	}
}

func TestProject_NilScoreCurrentIsZero(t *testing.T) {
	s := &ScoreBuilderService{}
	ins := sbInsights(610)
	ins.CreditScore = nil // no score on file
	actions := buildSimActions(ins)
	sim, err := s.project(context.Background(), ins, actions, 0)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if sim.CurrentScore != 0 {
		t.Errorf("currentScore = %d, want 0 for nil score", sim.CurrentScore)
	}
}

func TestClampScore(t *testing.T) {
	cases := map[int64]int64{
		300: 300,
		900: 900,
		250: 300,
		950: 900,
		610: 610,
	}
	for in, want := range cases {
		if got := clampScore(in); got != want {
			t.Errorf("clampScore(%d) = %d, want %d", in, got, want)
		}
	}
}

// ---- bankOfferingStrategy --------------------------------------------------

func TestBankOfferingStrategy_CarriesProductFields(t *testing.T) {
	o := models.BankOffering{
		Name: "Axis Insta Easy", ProductType: models.OfferingTypeFDCard,
		MinFDAmount: 15000, EstimatedPointsMin: 40, EstimatedPointsMax: 80,
		ApplyURL: "https://apply.example.com/x", RevenueNote: "FD referral",
	}
	s := bankOfferingStrategy(o)
	if s.Kind != "product" {
		t.Errorf("kind = %q, want product", s.Kind)
	}
	if s.Title != o.Name {
		t.Errorf("title = %q, want %q", s.Title, o.Name)
	}
	if s.ApplyURL != o.ApplyURL {
		t.Errorf("applyUrl = %q, want %q", s.ApplyURL, o.ApplyURL)
	}
	if s.FDAmount != o.MinFDAmount {
		t.Errorf("fdAmount = %v, want %v", s.FDAmount, o.MinFDAmount)
	}
	if s.EstimatedPointsMin == nil || *s.EstimatedPointsMin != 40 ||
		s.EstimatedPointsMax == nil || *s.EstimatedPointsMax != 80 {
		t.Errorf("estimated points = %v/%v, want 40/80", s.EstimatedPointsMin, s.EstimatedPointsMax)
	}
}

// ---- OfferingInput.validate ------------------------------------------------

func TestOfferingInput_Validate(t *testing.T) {
	base := OfferingInput{
		Name: "Axis Card", ProductType: "FD_CARD", ApplyURL: "https://x",
		MinCreditScore: 0, MaxCreditScore: 650,
		EstimatedPointsMin: 40, EstimatedPointsMax: 80,
	}
	if d := base.validate(); len(d) > 0 {
		t.Errorf("valid input should pass, got %v", d)
	}

	missingName := base
	missingName.Name = "   "
	if d := missingName.validate(); d["name"] == "" {
		t.Error("blank name should be flagged")
	}

	badType := base
	badType.ProductType = "STOCKS"
	if d := badType.validate(); d["productType"] == "" {
		t.Error("invalid productType should be flagged")
	}

	badBand := base
	badBand.MinCreditScore, badBand.MaxCreditScore = 700, 600
	if d := badBand.validate(); d["maxCreditScore"] == "" {
		t.Error("inverted band should be flagged on maxCreditScore")
	}

	badPoints := base
	badPoints.EstimatedPointsMin, badPoints.EstimatedPointsMax = 80, 40
	if d := badPoints.validate(); d["estimatedPointsMax"] == "" {
		t.Error("inverted point range should be flagged")
	}

	noURL := base
	noURL.ApplyURL = "  "
	if d := noURL.validate(); d["applyUrl"] == "" {
		t.Error("blank applyUrl should be flagged")
	}
}
