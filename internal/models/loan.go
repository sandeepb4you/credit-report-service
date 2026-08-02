package models

import (
	"strings"
	"time"
	"unicode"
)

// Loan types a provider can be configured for and that the switch optimizer
// knows how to compare. Stored uppercase; the loan_providers.loan_type CHECK
// constraint mirrors this set.
const (
	LoanTypeHome     = "HOME"
	LoanTypePersonal = "PERSONAL"
	LoanTypeCar      = "CAR"
)

// ValidLoanType reports whether s is one of the switchable loan types.
func ValidLoanType(s string) bool {
	switch s {
	case LoanTypeHome, LoanTypePersonal, LoanTypeCar:
		return true
	default:
		return false
	}
}

// LoanCategoryFor maps a human loan-type label (as produced by the analytics
// parser from the bureau Account_Type code, e.g. "Home Loan", "Auto Loan") to
// one of the switchable categories, or "" when the product is not something we
// offer a switch for (credit cards, education loans, gold loans, …).
func LoanCategoryFor(loanType string) string {
	// Match whole words so "Credit Card" does not read as a car loan on the
	// "car" substring inside "card".
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(loanType), func(r rune) bool {
		return !unicode.IsLetter(r)
	}) {
		words[w] = true
	}
	switch {
	case words["home"] || words["housing"]:
		return LoanTypeHome
	case words["personal"]:
		return LoanTypePersonal
	case words["auto"] || words["car"]:
		// "Auto Loan", "Used Car Loan", "Loan Against Car".
		return LoanTypeCar
	default:
		return ""
	}
}

// LoanProvider is the row model for loan_providers: one lender's offering for
// one loan type, curated by an admin and used by the switch optimizer.
type LoanProvider struct {
	ID       int64  `json:"id"       db:"id"`
	Name     string `json:"name"     db:"name"`
	LoanType string `json:"loanType" db:"loan_type"`

	InterestRatePercent float64 `json:"interestRatePercent" db:"interest_rate_percent"`

	ProcessingFeePercent float64 `json:"processingFeePercent" db:"processing_fee_percent"`
	ProcessingFeeFlat    float64 `json:"processingFeeFlat"    db:"processing_fee_flat"`

	MinCreditScore  int  `json:"minCreditScore"  db:"min_credit_score"`
	MaxTenureMonths *int `json:"maxTenureMonths" db:"max_tenure_months"`
	Active          bool `json:"active"          db:"active"`

	CreatedAt time.Time `json:"createdAt" db:"created_at"`
	UpdatedAt time.Time `json:"updatedAt" db:"updated_at"`
}

// ProcessingFeeOn returns the total processing fee this provider charges to
// transfer a loan of the given outstanding amount: the percentage component
// plus the flat charge.
func (p *LoanProvider) ProcessingFeeOn(amount float64) float64 {
	if amount <= 0 {
		return p.ProcessingFeeFlat
	}
	return amount*p.ProcessingFeePercent/100 + p.ProcessingFeeFlat
}

// LoanSwitchSettings is the single-row config table loan_switch_settings: the
// recovery window and the default foreclosure fees the optimizer applies.
type LoanSwitchSettings struct {
	ID                     int16     `json:"-"                    db:"id"`
	RecoveryWindowMonths   int       `json:"recoveryWindowMonths" db:"recovery_window_months"`
	ForeclosureFeeHome     float64   `json:"foreclosureFeePercentHome"     db:"foreclosure_fee_percent_home"`
	ForeclosureFeePersonal float64   `json:"foreclosureFeePercentPersonal" db:"foreclosure_fee_percent_personal"`
	ForeclosureFeeCar      float64   `json:"foreclosureFeePercentCar"      db:"foreclosure_fee_percent_car"`
	UpdatedAt              time.Time `json:"updatedAt"            db:"updated_at"`
}

// ForeclosureFeePercentFor returns the configured default foreclosure-fee
// percentage for a switchable loan type. An unknown type yields 0.
func (s *LoanSwitchSettings) ForeclosureFeePercentFor(loanType string) float64 {
	switch loanType {
	case LoanTypeHome:
		return s.ForeclosureFeeHome
	case LoanTypePersonal:
		return s.ForeclosureFeePersonal
	case LoanTypeCar:
		return s.ForeclosureFeeCar
	default:
		return 0
	}
}
