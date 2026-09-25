package models

import "time"

// Deletion request lifecycle.
const (
	// DeletionPending is a confirmed request waiting out its grace period.
	DeletionPending = "PENDING"
	// DeletionCancelled is a request the account holder stopped, by signing in
	// during the window or by asking support.
	DeletionCancelled = "CANCELLED"
	// DeletionCompleted is a request the sweep has carried out. The row stays
	// as the receipt — it is what answers "when was this account erased, and
	// who asked for it" after there is no account left to ask.
	DeletionCompleted = "COMPLETED"
)

// Reasons recorded on a cancelled request. Stored rather than inferred because
// "the user changed their mind" and "an admin stopped it" are different facts
// to a regulator, and the row outlives anyone's memory of which happened.
const (
	DeletionCancelledBySignIn = "signed_in"
	DeletionCancelledByAdmin  = "admin"
)

// AccountDeletionRequest is a row of account_deletion_requests: one scheduled
// erasure of one account.
//
// DestinationMasked, not the destination: this row survives the purge as its
// receipt, and keeping the full phone number on it would re-create the
// personal data the purge exists to destroy.
type AccountDeletionRequest struct {
	ID        int64  `json:"id"        db:"id"`
	AccountID int64  `json:"accountId" db:"account_id"`
	Status    string `json:"status"    db:"status"`

	Channel           string `json:"channel"           db:"channel"`
	DestinationMasked string `json:"destinationMasked" db:"destination_masked"`

	RequestedAt  time.Time `json:"requestedAt"  db:"requested_at"`
	ScheduledFor time.Time `json:"scheduledFor" db:"scheduled_for"`

	CancelledAt     *time.Time `json:"cancelledAt,omitempty"     db:"cancelled_at"`
	CancelledReason *string    `json:"cancelledReason,omitempty" db:"cancelled_reason"`
	CompletedAt     *time.Time `json:"completedAt,omitempty"     db:"completed_at"`
}

// AccountPurgeResult is what a completed purge removed, for the audit line.
//
// Counted inside the same transaction that does the deleting, so the receipt
// describes what actually went rather than what was there a moment earlier.
type AccountPurgeResult struct {
	AccountID int64 `json:"accountId"`

	Reports           int  `json:"reports"`
	Documents         int  `json:"documents"`
	BankStatements    int  `json:"bankStatements"`
	Identities        int  `json:"identities"`
	Sessions          int  `json:"sessions"`
	ScheduledChecks   int  `json:"scheduledChecks"`
	PayoutBankDetails int  `json:"payoutBankDetails"`
	HadKYCRecord      bool `json:"hadKycRecord"`

	// RetainedOrders is what deliberately survived: the financial record, now
	// pointing at an anonymous accounts row. Reported rather than silent
	// because "we deleted everything" and "we deleted everything about you"
	// are different claims, and this is the number behind the second one.
	RetainedOrders int `json:"retainedOrders"`

	// The retained rows that had personal data IN them, and were scrubbed
	// rather than deleted. A retained row is only anonymous if its contents
	// are: the webhook payload is Cashfree's raw JSON, which carries the
	// customer's name, phone and email; a withdrawal carries the bank account
	// holder's name. Counted so the audit line shows the scrub happened.
	WebhookEventsScrubbed int `json:"webhookEventsScrubbed"`
	// InvoicesScrubbed kept their numbers and amounts and lost their billed-to
	// name, email and phone, and their stored PDF.
	InvoicesScrubbed int `json:"invoicesScrubbed"`
	WithdrawalsRedacted   int `json:"withdrawalsRedacted"`
	// PendingWithdrawalsCancelled were requested but never paid. The payout
	// bank account they would have been paid to is deleted by the same purge,
	// so they could not be paid any more — rejected with a reason rather than
	// left PENDING in an admin queue nobody can clear.
	PendingWithdrawalsCancelled int `json:"pendingWithdrawalsCancelled"`
	// ReferralCodesRevoked stops the account's own code from attributing new
	// signups — and crediting money — to an account that no longer exists.
	ReferralCodesRevoked int `json:"referralCodesRevoked"`

	// DeletedReportIDs are the purged reports' ids, for the objects no row
	// records: an advanced report's PDF lives at a key DERIVED from account and
	// report id (service.advancedReportKey), so the report rows' own URIs do
	// not name it. Without these the unencrypted advanced PDFs would outlive
	// the purge with nothing pointing at them.
	DeletedReportIDs []int64 `json:"-"`

	// ObjectURIs are the S3 objects whose rows have gone — encrypted report
	// PDFs and uploaded PAN cards. Deleted after the commit, best-effort, and
	// logged at ERROR when that fails: nothing else will ever go looking for
	// them, so an unlogged failure is personal data left in a bucket forever.
	ObjectURIs []string `json:"-"`
}
