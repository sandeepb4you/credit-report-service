package service

import (
	"math"
	"strings"
	"testing"

	"credit-report-service/internal/models"
)

func approx(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

func TestEmi_StandardAmortization(t *testing.T) {
	// ₹10,00,000 at 12% p.a. over 120 months → well-known ≈ ₹14,347.09.
	got := emi(1000000, 12, 120)
	if !approx(got, 14347.09, 0.5) {
		t.Errorf("emi = %.2f, want ≈14347.09", got)
	}
}

func TestEmi_ZeroRate(t *testing.T) {
	// 0% degrades to straight-line: 120000 / 12 = 10000.
	if got := emi(120000, 0, 12); got != 10000 {
		t.Errorf("emi zero-rate = %.2f, want 10000", got)
	}
}

func TestEmi_Guards(t *testing.T) {
	if emi(0, 10, 12) != 0 {
		t.Error("zero principal should give 0")
	}
	if emi(100000, 10, 0) != 0 {
		t.Error("zero months should give 0")
	}
}

func TestEmi_LowerRateLowerEmi(t *testing.T) {
	hi := emi(8200000, 7.45, 288)
	lo := emi(8200000, 7.20, 288)
	if !(lo < hi) {
		t.Errorf("lower rate should give lower EMI: %.2f !< %.2f", lo, hi)
	}
}

func iptr(i int) *int { return &i }

func TestBestProvider_PicksCheapestQualifying(t *testing.T) {
	providers := []models.LoanProvider{
		{Name: "A", InterestRatePercent: 7.30, MinCreditScore: 0},
		{Name: "B", InterestRatePercent: 7.10, MinCreditScore: 800}, // cheapest but needs 800
		{Name: "C", InterestRatePercent: 7.20, MinCreditScore: 750},
	}
	// Score 780: B is out (needs 800), C qualifies and beats A.
	best := bestProvider(providers, 780, 7.45, 240, "HDFC")
	if best == nil || best.Name != "C" {
		t.Fatalf("want C, got %v", best)
	}
	// Score 820: B now qualifies and is cheapest.
	best = bestProvider(providers, 820, 7.45, 240, "HDFC")
	if best == nil || best.Name != "B" {
		t.Fatalf("want B, got %v", best)
	}
}

func TestBestProvider_ExcludesNotBetterAndSelfAndTenure(t *testing.T) {
	providers := []models.LoanProvider{
		{Name: "Same Bank", InterestRatePercent: 7.00, MinCreditScore: 0}, // cheaper but current lender
		{Name: "Higher", InterestRatePercent: 7.50, MinCreditScore: 0},    // not better than 7.45
		{Name: "ShortTenure", InterestRatePercent: 7.10, MinCreditScore: 0, MaxTenureMonths: iptr(60)},
		{Name: "Good", InterestRatePercent: 7.20, MinCreditScore: 0},
	}
	best := bestProvider(providers, 800, 7.45, 240, "Same Bank Ltd")
	if best == nil || best.Name != "Good" {
		t.Fatalf("want Good (self/higher/short-tenure excluded), got %v", best)
	}
}

func TestBestProvider_NoneQualifies(t *testing.T) {
	providers := []models.LoanProvider{
		{Name: "Pricey", InterestRatePercent: 8.0, MinCreditScore: 0},
	}
	if best := bestProvider(providers, 800, 7.45, 240, "HDFC"); best != nil {
		t.Fatalf("expected nil, got %v", best)
	}
}

func TestEvaluateLoan_InsufficientData(t *testing.T) {
	s := &LoanSwitchService{}
	cfg := &models.LoanSwitchSettings{RecoveryWindowMonths: 12}
	// Missing rate → insufficient_data.
	loan := LoanAccount{Company: "HDFC", CurrentBalance: 8200000, InterestRatePercent: 0, RemainingTenureMonths: 288}
	op := s.evaluateLoan(loan, models.LoanTypeHome, 815, nil, cfg)
	if op == nil || op.Status != "insufficient_data" {
		t.Fatalf("want insufficient_data, got %+v", op)
	}
}

func TestEvaluateLoan_ZeroBalanceOmitted(t *testing.T) {
	s := &LoanSwitchService{}
	cfg := &models.LoanSwitchSettings{RecoveryWindowMonths: 12}
	loan := LoanAccount{Company: "HDFC", CurrentBalance: 0, InterestRatePercent: 7.45, RemainingTenureMonths: 288}
	if op := s.evaluateLoan(loan, models.LoanTypeHome, 815, nil, cfg); op != nil {
		t.Fatalf("zero-balance loan should be omitted, got %+v", op)
	}
}

func TestEvaluateLoan_RecommendedWhenRecoverableInWindow(t *testing.T) {
	s := &LoanSwitchService{}
	// HOME foreclosure 0%, provider processing 0.25% + flat 0. Window 12 months.
	cfg := &models.LoanSwitchSettings{RecoveryWindowMonths: 12, ForeclosureFeeHome: 0}
	providers := []models.LoanProvider{
		{Name: "Kotak", LoanType: models.LoanTypeHome, InterestRatePercent: 7.20,
			ProcessingFeePercent: 0.10, MinCreditScore: 800},
	}
	loan := LoanAccount{
		Company: "HDFC Bank Ltd", AccountNumber: "XX1", CurrentBalance: 8200000,
		InterestRatePercent: 7.45, RemainingTenureMonths: 288,
	}
	op := s.evaluateLoan(loan, models.LoanTypeHome, 815, providers, cfg)
	if op == nil {
		t.Fatal("expected an opportunity")
	}
	if op.Status != "recommended" || !op.Recommended {
		t.Fatalf("want recommended, got status=%q recommended=%v (recoveryMonths=%v cost=%.0f monthlySaving=%.2f)",
			op.Status, op.Recommended, op.RecoveryMonths, op.SwitchingCost, op.MonthlyEmiSaving)
	}
	if op.MonthlyEmiSaving <= 0 {
		t.Errorf("monthly saving should be positive, got %.2f", op.MonthlyEmiSaving)
	}
	// Processing fee = 0.10% of 82L = 8200; foreclosure 0. Recovery = ceil(8200/monthlySaving).
	if op.SwitchingCost <= 0 || op.RecoveryMonths == nil {
		t.Errorf("expected switching cost + recovery months set, got cost=%.2f rm=%v", op.SwitchingCost, op.RecoveryMonths)
	}
}

func TestEvaluateLoan_NotRecommendedWhenRecoveryTooSlow(t *testing.T) {
	s := &LoanSwitchService{}
	// Tiny rate gap + heavy fees → recovery beyond a 1-month window.
	cfg := &models.LoanSwitchSettings{RecoveryWindowMonths: 1, ForeclosureFeePersonal: 4}
	providers := []models.LoanProvider{
		{Name: "NBFC", LoanType: models.LoanTypePersonal, InterestRatePercent: 13.9,
			ProcessingFeePercent: 2, ProcessingFeeFlat: 5000, MinCreditScore: 0},
	}
	loan := LoanAccount{
		Company: "Bajaj", CurrentBalance: 300000,
		InterestRatePercent: 14.0, RemainingTenureMonths: 24,
	}
	op := s.evaluateLoan(loan, models.LoanTypePersonal, 700, providers, cfg)
	if op == nil {
		t.Fatal("expected an opportunity entry")
	}
	if op.Recommended || op.Status != "not_recommended" {
		t.Fatalf("want not_recommended, got status=%q recommended=%v recoveryMonths=%v",
			op.Status, op.Recommended, op.RecoveryMonths)
	}
}

func TestLoanCategoryFor(t *testing.T) {
	cases := map[string]string{
		"Home Loan":      models.LoanTypeHome,
		"Housing Loan":   models.LoanTypeHome,
		"Personal Loan":  models.LoanTypePersonal,
		"Auto Loan":      models.LoanTypeCar,
		"Used Car Loan":  models.LoanTypeCar,
		"Credit Card":    "",
		"Education Loan": "",
		"Gold Loan":      "",
	}
	for in, want := range cases {
		if got := models.LoanCategoryFor(in); got != want {
			t.Errorf("LoanCategoryFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildRecommendations_MergesInterestAndScore(t *testing.T) {
	rm := 4
	ins := &ReportInsights{
		InterestSavings: &SwitchOpportunities{
			Opportunities: []SwitchOpportunity{
				{LoanType: "HOME", CurrentLender: "HDFC Bank Ltd", CurrentRatePercent: 7.45,
					NewRatePercent: 7.20, NetSaving: 372000, MonthlyEmiSaving: 1310, RecoveryMonths: &rm,
					Recommended: true, BestProvider: &models.LoanProvider{Name: "Kotak Mahindra Bank"}},
				{LoanType: "CAR", Recommended: false}, // not recommended -> excluded
			},
		},
		ReportCard: &ReportCard{Factors: []CardFactor{
			{Name: "Payment history", Grade: "A+", Detail: "keep it up", Summary: "elite"},    // A+ -> excluded
			{Name: "Credit utilisation", Grade: "C", Detail: "pay down", Summary: "64% used"}, // improvable
			{Name: "Enquiries", Grade: "B", Detail: "pause apps", Summary: "4 in 6 months"},   // improvable
		}},
	}
	recs := buildRecommendations(ins)
	if len(recs) != 3 {
		t.Fatalf("want 3 recs (1 interest + 2 score), got %d: %+v", len(recs), recs)
	}
	// Interest recommendation comes first.
	if recs[0].Category != "interest" || recs[0].Priority != 1 {
		t.Errorf("first rec should be interest/priority1, got %+v", recs[0])
	}
	if !strings.Contains(recs[0].Detail, "Kotak") {
		t.Errorf("interest detail should name the target provider, got %q", recs[0].Detail)
	}
	// Score recs follow, weakest grade (C) before B.
	if recs[1].Category != "score" || recs[1].Title != "Improve credit utilisation" {
		t.Errorf("second rec should be the C-grade utilisation, got %+v", recs[1])
	}
	if recs[2].Title != "Improve enquiries" {
		t.Errorf("third rec should be the B-grade enquiries, got %+v", recs[2])
	}
	// Score recs carry estimated point ranges; interest recs do not.
	if recs[0].EstimatedPointsMin != nil {
		t.Errorf("interest rec should have no point range, got %v", *recs[0].EstimatedPointsMin)
	}
	if recs[1].EstimatedPointsMin == nil || *recs[1].EstimatedPointsMin != 30 || *recs[1].EstimatedPointsMax != 50 {
		t.Errorf("utilisation (C) should estimate +30–50, got %v", recs[1])
	}
	if recs[2].EstimatedPointsMin == nil || *recs[2].EstimatedPointsMin != 10 || *recs[2].EstimatedPointsMax != 20 {
		t.Errorf("enquiries (B) should estimate +10–20, got %v", recs[2])
	}
	if !strings.HasPrefix(recs[1].Impact, "Estimated +30–50 pts") {
		t.Errorf("utilisation impact should lead with the estimated range, got %q", recs[1].Impact)
	}
}

func TestBuildRecommendations_NilSafe(t *testing.T) {
	if got := buildRecommendations(&ReportInsights{}); len(got) != 0 {
		t.Errorf("empty insights should yield no recommendations, got %+v", got)
	}
}
