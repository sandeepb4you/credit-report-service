package models

import "time"

// Where an owed report's funding comes from.
const (
	// OwedFromOrder is a one-time check that was paid for and never spent.
	OwedFromOrder = "order"
	// OwedFromPlan is a plan refresh whose due date has arrived.
	OwedFromPlan = "plan"
)

// OwedReportRow is one customer the service owes a credit report: they paid,
// and nothing has been pulled with that payment yet.
//
// The usual reason is a PAN that went to manual review after payment — the
// pull is gated on a VERIFIED PAN, so it could not run when they paid, and
// nothing ran it when the review cleared. KycStatus says whether a pull can
// run now; LastPull* says whether one was tried and failed.
type OwedReportRow struct {
	AccountID int64   `json:"accountId" db:"account_id"`
	Name      string  `json:"name"      db:"name"`
	Phone     *string `json:"phone"     db:"phone"`
	Email     *string `json:"email"     db:"email"`
	// KycStatus is the PAN record's status, nil when none was ever submitted.
	// Only VERIFIED can be pulled for.
	KycStatus *string `json:"kycStatus" db:"kyc_status"`

	Source      string     `json:"source"      db:"source"`
	OrderID     string     `json:"orderId"     db:"order_uid"`
	ProductCode string     `json:"productCode" db:"product_code"`
	PaymentMode string     `json:"paymentMode" db:"payment_mode"`
	PaidAt      *time.Time `json:"paidAt"      db:"paid_at"`
	// DueOn is the plan refresh's due date; nil for a one-time order.
	DueOn *time.Time `json:"dueOn" db:"due_on"`

	// LastPullAt is the account's most recent pull attempt, and LastPullFailed
	// whether it failed — a paid customer whose pull keeps failing is a
	// different support call from one whose pull never started.
	LastPullAt     *time.Time `json:"lastPullAt"     db:"last_pull_at"`
	LastPullFailed bool       `json:"lastPullFailed" db:"last_pull_failed"`
}

// OwedRunResult is what forcing an owed report produced.
type OwedRunResult struct {
	AccountID   int64  `json:"accountId"`
	Source      string `json:"source"`
	ReportID    int64  `json:"reportId"`
	CreditScore *int64 `json:"creditScore"`
	// Reused is true when a report from inside the reuse window was served in
	// place of a new bureau call — the purchase is spent either way.
	Reused bool `json:"reused"`
}
