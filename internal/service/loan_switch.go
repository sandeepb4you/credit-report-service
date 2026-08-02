package service

import (
	"context"
	"errors"
	"math"
	"strings"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// switchDisclaimer is the compliance line the S24 screen mandates: the rates
// are indicative market benchmarks curated by an admin, never a firm offer.
const switchDisclaimer = "Rates are indicative market benchmarks, not offers. Actual eligibility and rates are decided by the lender."

// LoanSwitchService curates loan providers (admin) and computes balance-transfer
// savings opportunities from a user's latest credit report (Journey 05·B, S24).
type LoanSwitchService struct {
	loans     *repository.LoanProviderRepo
	analytics *repository.CreditAnalyticsRepo
}

func NewLoanSwitchService(loans *repository.LoanProviderRepo, analytics *repository.CreditAnalyticsRepo) *LoanSwitchService {
	return &LoanSwitchService{loans: loans, analytics: analytics}
}

// ---- Admin: provider CRUD -------------------------------------------------

// ProviderInput is the validated create/update payload for a loan provider.
type ProviderInput struct {
	Name                 string
	LoanType             string
	InterestRatePercent  float64
	ProcessingFeePercent float64
	ProcessingFeeFlat    float64
	MinCreditScore       int
	MaxTenureMonths      *int
	Active               *bool // nil defaults to true on create
}

func (in *ProviderInput) validate() map[string]string {
	d := map[string]string{}
	if strings.TrimSpace(in.Name) == "" {
		d["name"] = "is required"
	}
	if !models.ValidLoanType(in.LoanType) {
		d["loanType"] = "must be one of HOME, PERSONAL, CAR"
	}
	if in.InterestRatePercent <= 0 || in.InterestRatePercent > 100 {
		d["interestRatePercent"] = "must be between 0 and 100"
	}
	if in.ProcessingFeePercent < 0 || in.ProcessingFeePercent > 100 {
		d["processingFeePercent"] = "must be between 0 and 100"
	}
	if in.ProcessingFeeFlat < 0 {
		d["processingFeeFlat"] = "must not be negative"
	}
	if in.MinCreditScore < 0 || in.MinCreditScore > 900 {
		d["minCreditScore"] = "must be between 0 and 900"
	}
	if in.MaxTenureMonths != nil && *in.MaxTenureMonths <= 0 {
		d["maxTenureMonths"] = "must be positive when set"
	}
	return d
}

// CreateProvider inserts a new provider offering.
func (s *LoanSwitchService) CreateProvider(ctx context.Context, in ProviderInput) (*models.LoanProvider, error) {
	if d := in.validate(); len(d) > 0 {
		return nil, apperr.NewValidationWith("Validation failed", d)
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	p := &models.LoanProvider{
		Name:                 strings.TrimSpace(in.Name),
		LoanType:             in.LoanType,
		InterestRatePercent:  in.InterestRatePercent,
		ProcessingFeePercent: in.ProcessingFeePercent,
		ProcessingFeeFlat:    in.ProcessingFeeFlat,
		MinCreditScore:       in.MinCreditScore,
		MaxTenureMonths:      in.MaxTenureMonths,
		Active:               active,
	}
	if err := s.loans.Create(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// UpdateProvider replaces the mutable fields of an existing provider.
func (s *LoanSwitchService) UpdateProvider(ctx context.Context, id int64, in ProviderInput) (*models.LoanProvider, error) {
	if d := in.validate(); len(d) > 0 {
		return nil, apperr.NewValidationWith("Validation failed", d)
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	p := &models.LoanProvider{
		ID:                   id,
		Name:                 strings.TrimSpace(in.Name),
		LoanType:             in.LoanType,
		InterestRatePercent:  in.InterestRatePercent,
		ProcessingFeePercent: in.ProcessingFeePercent,
		ProcessingFeeFlat:    in.ProcessingFeeFlat,
		MinCreditScore:       in.MinCreditScore,
		MaxTenureMonths:      in.MaxTenureMonths,
		Active:               active,
	}
	if err := s.loans.Update(ctx, p); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NewNotFound("Loan provider not found")
		}
		return nil, err
	}
	return p, nil
}

// DeleteProvider removes a provider by id.
func (s *LoanSwitchService) DeleteProvider(ctx context.Context, id int64) error {
	if err := s.loans.Delete(ctx, id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return apperr.NewNotFound("Loan provider not found")
		}
		return err
	}
	return nil
}

// GetProvider returns a single provider by id.
func (s *LoanSwitchService) GetProvider(ctx context.Context, id int64) (*models.LoanProvider, error) {
	p, err := s.loans.FindByID(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("Loan provider not found")
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// ListProviders returns providers, optionally filtered by loan type and active.
func (s *LoanSwitchService) ListProviders(ctx context.Context, loanType *string, active *bool) ([]models.LoanProvider, error) {
	if loanType != nil {
		lt := strings.ToUpper(strings.TrimSpace(*loanType))
		if !models.ValidLoanType(lt) {
			return nil, apperr.NewValidationWith("Validation failed",
				map[string]string{"loanType": "must be one of HOME, PERSONAL, CAR"})
		}
		loanType = &lt
	}
	return s.loans.List(ctx, loanType, active)
}

// ---- Admin: switch settings ------------------------------------------------

// GetSettings returns the switch config (recovery window + default foreclosure
// fees).
func (s *LoanSwitchService) GetSettings(ctx context.Context) (*models.LoanSwitchSettings, error) {
	cfg, err := s.loans.GetSettings(ctx)
	if errors.Is(err, repository.ErrNotFound) {
		// The row is seeded by the migration; a missing row is a real fault.
		return nil, apperr.NewValidation("loan switch settings are not initialized")
	}
	return cfg, err
}

// SettingsInput is the validated update payload for the switch config.
type SettingsInput struct {
	RecoveryWindowMonths   int
	ForeclosureFeeHome     float64
	ForeclosureFeePersonal float64
	ForeclosureFeeCar      float64
}

// UpdateSettings writes the switch config.
func (s *LoanSwitchService) UpdateSettings(ctx context.Context, in SettingsInput) (*models.LoanSwitchSettings, error) {
	d := map[string]string{}
	if in.RecoveryWindowMonths <= 0 {
		d["recoveryWindowMonths"] = "must be a positive number of months"
	}
	for field, v := range map[string]float64{
		"foreclosureFeePercentHome":     in.ForeclosureFeeHome,
		"foreclosureFeePercentPersonal": in.ForeclosureFeePersonal,
		"foreclosureFeePercentCar":      in.ForeclosureFeeCar,
	} {
		if v < 0 || v > 100 {
			d[field] = "must be between 0 and 100"
		}
	}
	if len(d) > 0 {
		return nil, apperr.NewValidationWith("Validation failed", d)
	}
	cfg := &models.LoanSwitchSettings{
		RecoveryWindowMonths:   in.RecoveryWindowMonths,
		ForeclosureFeeHome:     in.ForeclosureFeeHome,
		ForeclosureFeePersonal: in.ForeclosureFeePersonal,
		ForeclosureFeeCar:      in.ForeclosureFeeCar,
	}
	if err := s.loans.UpdateSettings(ctx, cfg); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NewValidation("loan switch settings are not initialized")
		}
		return nil, err
	}
	return cfg, nil
}

// ---- User: switch opportunities from the latest report ---------------------

// SwitchOpportunity is one active loan compared against the best qualifying
// provider. When the report lacks the fields needed to compute a switch (the
// bureau file is often sparse on rate/tenure), Status is "insufficient_data"
// and the numeric fields are omitted.
type SwitchOpportunity struct {
	LoanType           string  `json:"loanType"` // HOME | PERSONAL | CAR
	CurrentLender      string  `json:"currentLender"`
	AccountNumber      string  `json:"accountNumber"`
	OutstandingBalance float64 `json:"outstandingBalance"`

	CurrentRatePercent    float64 `json:"currentRatePercent"`
	RemainingTenureMonths int64   `json:"remainingTenureMonths"`

	// Status: "recommended" | "not_recommended" | "no_better_offer" |
	// "insufficient_data". Reason explains the non-recommended cases.
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`

	BestProvider   *models.LoanProvider `json:"bestProvider,omitempty"`
	NewRatePercent float64              `json:"newRatePercent,omitempty"`

	CurrentEmi          float64 `json:"currentEmi,omitempty"`
	NewEmi              float64 `json:"newEmi,omitempty"`
	MonthlyEmiSaving    float64 `json:"monthlyEmiSaving,omitempty"`
	TotalInterestSaving float64 `json:"totalInterestSaving,omitempty"`
	ForeclosureFee      float64 `json:"foreclosureFee,omitempty"`
	ProcessingFee       float64 `json:"processingFee,omitempty"`
	SwitchingCost       float64 `json:"switchingCost,omitempty"`
	NetSaving           float64 `json:"netSaving,omitempty"`
	RecoveryMonths      *int    `json:"recoveryMonths,omitempty"`
	Recommended         bool    `json:"recommended"`
}

// SwitchOpportunities is the response for GET /loan-switch/opportunities.
type SwitchOpportunities struct {
	ReportID              int64               `json:"reportId"`
	CreditScore           *int64              `json:"creditScore"`
	RecoveryWindowMonths  int                 `json:"recoveryWindowMonths"`
	Opportunities         []SwitchOpportunity `json:"opportunities"`
	TotalMonthlyEmiSaving float64             `json:"totalMonthlyEmiSaving"`
	TotalNetSaving        float64             `json:"totalNetSaving"`
	RecommendedCount      int                 `json:"recommendedCount"`
	Disclaimer            string              `json:"disclaimer"`
}

// GetOpportunities categorizes each active loan on a report and compares it
// against the best qualifying provider. When reportID is nil it uses the
// caller's latest successful report; otherwise it uses that specific report,
// which must belong to the caller (a foreign/missing id reads as not found).
func (s *LoanSwitchService) GetOpportunities(ctx context.Context, accountID int64, reportID *int64) (*SwitchOpportunities, error) {
	var (
		row *models.CreditAnalyticsRequest
		err error
	)
	if reportID != nil {
		row, err = s.analytics.FindByID(ctx, *reportID)
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NewNotFound("Report not found")
		}
		if err != nil {
			return nil, err
		}
		// Ownership check: a report owned by another account is reported as not
		// found so ids can't be probed.
		if row.AccountID == nil || *row.AccountID != accountID {
			return nil, apperr.NewNotFound("Report not found")
		}
	} else {
		row, err = s.analytics.FindLatestByAccount(ctx, accountID)
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NewNotFound("No credit report found")
		}
		if err != nil {
			return nil, err
		}
	}
	insights, err := insightsFromReportRow(row)
	if err != nil {
		return nil, err
	}
	return s.OpportunitiesFromInsights(ctx, insights)
}

// OpportunitiesFromInsights computes switch opportunities from an already-parsed
// report so callers that have the insights in hand (e.g. the analytics service
// enriching its response) don't re-fetch or re-parse the bureau payload.
func (s *LoanSwitchService) OpportunitiesFromInsights(ctx context.Context, insights *ReportInsights) (*SwitchOpportunities, error) {
	cfg, err := s.GetSettings(ctx)
	if err != nil {
		return nil, err
	}

	var score int64
	if insights.CreditScore != nil {
		score = *insights.CreditScore
	}

	out := &SwitchOpportunities{
		ReportID:             insights.ReportID,
		CreditScore:          insights.CreditScore,
		RecoveryWindowMonths: cfg.RecoveryWindowMonths,
		Opportunities:        []SwitchOpportunity{},
		Disclaimer:           switchDisclaimer,
	}

	for _, loan := range insights.LoanAccounts {
		category := models.LoanCategoryFor(loan.LoanType)
		if category == "" {
			continue // not a switchable product (card, education loan, …)
		}
		providers, err := s.loans.ListActiveByType(ctx, category)
		if err != nil {
			return nil, err
		}
		op := s.evaluateLoan(loan, category, score, providers, cfg)
		if op == nil {
			continue
		}
		out.Opportunities = append(out.Opportunities, *op)
		if op.Recommended {
			out.RecommendedCount++
			out.TotalMonthlyEmiSaving = roundTo2(out.TotalMonthlyEmiSaving + op.MonthlyEmiSaving)
			out.TotalNetSaving = roundTo2(out.TotalNetSaving + op.NetSaving)
		}
	}
	return out, nil
}

// evaluateLoan compares one report loan against the candidate providers and
// builds its opportunity entry. Returns nil to omit the loan entirely (e.g. an
// active loan with zero outstanding — nothing to transfer).
func (s *LoanSwitchService) evaluateLoan(
	loan LoanAccount, category string, score int64,
	providers []models.LoanProvider, cfg *models.LoanSwitchSettings,
) *SwitchOpportunity {
	if loan.CurrentBalance <= 0 {
		return nil
	}
	op := &SwitchOpportunity{
		LoanType:              category,
		CurrentLender:         loan.Company,
		AccountNumber:         loan.AccountNumber,
		OutstandingBalance:    roundTo2(loan.CurrentBalance),
		CurrentRatePercent:    loan.InterestRatePercent,
		RemainingTenureMonths: loan.RemainingTenureMonths,
	}

	// The report is often sparse: without the current rate or the remaining
	// tenure we cannot compute an EMI, so we surface the loan honestly rather
	// than inventing numbers.
	if loan.InterestRatePercent <= 0 || loan.RemainingTenureMonths <= 0 {
		op.Status = "insufficient_data"
		op.Reason = "The credit report did not include this loan's interest rate or remaining tenure."
		return op
	}

	best := bestProvider(providers, score, loan.InterestRatePercent, loan.RemainingTenureMonths, loan.Company)
	if best == nil {
		op.Status = "no_better_offer"
		op.Reason = "No configured provider offers a lower rate for your score band."
		return op
	}

	currentEmi := emi(loan.CurrentBalance, loan.InterestRatePercent, loan.RemainingTenureMonths)
	newEmi := emi(loan.CurrentBalance, best.InterestRatePercent, loan.RemainingTenureMonths)
	monthlySaving := currentEmi - newEmi
	if monthlySaving <= 0 {
		// A lower headline rate can still fail to beat the current EMI in edge
		// cases; treat it as no real opportunity.
		op.Status = "no_better_offer"
		op.Reason = "No configured provider produces a lower EMI for your score band."
		return op
	}

	foreclosure := loan.CurrentBalance * cfg.ForeclosureFeePercentFor(category) / 100
	processing := best.ProcessingFeeOn(loan.CurrentBalance)
	switchingCost := foreclosure + processing
	totalInterestSaving := monthlySaving * float64(loan.RemainingTenureMonths)

	recoveryMonths := int(math.Ceil(switchingCost / monthlySaving))
	recommended := recoveryMonths <= cfg.RecoveryWindowMonths

	op.BestProvider = best
	op.NewRatePercent = best.InterestRatePercent
	op.CurrentEmi = roundTo2(currentEmi)
	op.NewEmi = roundTo2(newEmi)
	op.MonthlyEmiSaving = roundTo2(monthlySaving)
	op.TotalInterestSaving = roundTo2(totalInterestSaving)
	op.ForeclosureFee = roundTo2(foreclosure)
	op.ProcessingFee = roundTo2(processing)
	op.SwitchingCost = roundTo2(switchingCost)
	op.NetSaving = roundTo2(totalInterestSaving - switchingCost)
	op.RecoveryMonths = &recoveryMonths
	op.Recommended = recommended
	if recommended {
		op.Status = "recommended"
	} else {
		op.Status = "not_recommended"
		op.Reason = "Switching cost is not recovered within the configured window."
	}
	return op
}

// bestProvider picks the cheapest active provider that the borrower qualifies
// for and that beats the current rate. Candidates must: clear the min credit
// score, offer a strictly lower rate than the current loan, cover the remaining
// tenure, and not be the borrower's current lender. It scans the whole set and
// keeps the lowest rate rather than trusting the input order.
func bestProvider(providers []models.LoanProvider, score int64, currentRate float64, remainingTenure int64, currentLender string) *models.LoanProvider {
	var best *models.LoanProvider
	for i := range providers {
		p := &providers[i]
		if int64(p.MinCreditScore) > score {
			continue
		}
		if p.InterestRatePercent >= currentRate {
			continue
		}
		if p.MaxTenureMonths != nil && int64(*p.MaxTenureMonths) < remainingTenure {
			continue
		}
		if sameLender(p.Name, currentLender) {
			continue // no point "switching" to the lender you're already with
		}
		if best == nil || p.InterestRatePercent < best.InterestRatePercent {
			best = p
		}
	}
	return best
}

// sameLender reports whether two lender names refer to the same institution,
// tolerating the bureau's verbose subscriber names vs. the admin's short names
// (e.g. "HDFC Bank Ltd" vs "HDFC Bank") via a loose containment check on the
// first significant token.
func sameLender(a, b string) bool {
	na, nb := normalizeLender(a), normalizeLender(b)
	if na == "" || nb == "" {
		return false
	}
	return na == nb || strings.HasPrefix(na, nb) || strings.HasPrefix(nb, na)
}

func normalizeLender(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, suffix := range []string{" limited", " ltd", " ltd.", " bank", " finance", " financial services", " housing finance"} {
		s = strings.TrimSuffix(s, suffix)
	}
	return strings.TrimSpace(s)
}

// emi computes the equated monthly instalment for a principal at an annual
// interest rate over n months, using the standard amortization formula. A zero
// rate degrades to straight-line repayment.
func emi(principal, annualRatePercent float64, months int64) float64 {
	if months <= 0 || principal <= 0 {
		return 0
	}
	r := annualRatePercent / 12 / 100
	if r == 0 {
		return principal / float64(months)
	}
	pow := math.Pow(1+r, float64(months))
	return principal * r * pow / (pow - 1)
}
