package models

import "time"

// Product types a score-builder offering can be. Stored uppercase; the
// bank_offerings.product_type CHECK constraint mirrors this set. FD_CARD is the
// hero of the low-score toolkit (S28); SECURED_LOAN is reserved for future
// secured-installment products.
const (
	OfferingTypeFDCard      = "FD_CARD"
	OfferingTypeSecuredLoan = "SECURED_LOAN"
)

// ValidOfferingType reports whether s is one of the score-builder product types.
func ValidOfferingType(s string) bool {
	switch s {
	case OfferingTypeFDCard, OfferingTypeSecuredLoan:
		return true
	default:
		return false
	}
}

// BankOffering is the row model for bank_offerings: one partner product that
// helps a user rebuild credit, curated by an admin and surfaced by the
// score-builder when the user's score falls in the offering's target band.
type BankOffering struct {
	ID          int64  `json:"id"        db:"id"`
	Name        string `json:"name"      db:"name"`
	ProductType string `json:"productType" db:"product_type"`

	// MinFDAmount is the fixed deposit the user opens to obtain the product
	// (e.g. a card against the FD). 0 when not applicable.
	MinFDAmount float64 `json:"minFdAmount" db:"min_fd_amount"`
	// InterestRatePercent is the FD yield for display (the deposit keeps earning
	// while the card builds history). Not a borrowing rate.
	InterestRatePercent float64 `json:"interestRatePercent" db:"interest_rate_percent"`

	// ScoreBand gates the offering: it is surfaced only when the user's score is
	// within [MinCreditScore, MaxCreditScore]. Defaults 0..900 = "everyone".
	MinCreditScore int `json:"minCreditScore" db:"min_credit_score"`
	MaxCreditScore int `json:"maxCreditScore" db:"max_credit_score"`

	// EstimatedPointsMin/Max bound the estimated score gain. Always rendered with
	// the "estimated, not guaranteed" disclaimer.
	EstimatedPointsMin int `json:"estimatedPointsMin" db:"estimated_points_min"`
	EstimatedPointsMax int `json:"estimatedPointsMax" db:"estimated_points_max"`

	// ApplyURL is the CTA destination for taking up the product.
	ApplyURL string `json:"applyUrl" db:"apply_url"`
	// RevenueNote is an ops-facing referral/commission note.
	RevenueNote string `json:"revenueNote" db:"revenue_note"`

	Active    bool      `json:"active"    db:"active"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
	UpdatedAt time.Time `json:"updatedAt" db:"updated_at"`
}

// BandContains reports whether a credit score falls within this offering's
// target band, inclusive on both ends.
func (o *BankOffering) BandContains(score int) bool {
	return score >= o.MinCreditScore && score <= o.MaxCreditScore
}
