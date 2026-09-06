package models

import "time"

// Scheduled-check lifecycle. A row is minted PENDING at fulfilment, claimed
// RUNNING by the runner (or spent directly by a manual check), and ends DONE.
// FAILED is the attempt cap giving up on a run that kept erroring; EXPIRED is
// the validity sweep voiding a run past its plan year. There is no CANCELLED:
// plans are prepaid, not recurring, so there is nothing to cancel server-side.
const (
	ScheduledCheckPending = "PENDING"
	ScheduledCheckRunning = "RUNNING"
	ScheduledCheckDone    = "DONE"
	ScheduledCheckFailed  = "FAILED"
	ScheduledCheckExpired = "EXPIRED"
)

// Who completed a scheduled check (completed_by).
const (
	ScheduledCheckByRunner = "runner"
	ScheduledCheckByManual = "manual"
)

// ScheduledScoreCheck is the row model for scheduled_score_checks: one future
// (or completed) report run that a plan purchase paid for. The schedule is
// data, not derived state — remaining quota is COUNT(status = PENDING), the
// next refresh is MIN(due_on).
//
// ProductCode and IntervalMonths are snapshots taken when the batch was
// minted, so a later catalog edit cannot rewrite what was bought.
type ScheduledScoreCheck struct {
	ID          int64  `json:"id"          db:"id"`
	AccountID   int64  `json:"-"           db:"account_id"`
	OrderID     int64  `json:"-"           db:"order_id"`
	ProductCode string `json:"productCode" db:"product_code"`

	IntervalMonths int `json:"intervalMonths" db:"interval_months"`
	SequenceNo     int `json:"sequenceNo"     db:"sequence_no"`

	// DueOn is the day this run is owed — day granularity on purpose; the
	// runner executes it whatever time of that day its sweep lands. Scanned
	// from a DATE column, so only the date part is meaningful.
	DueOn     time.Time  `json:"dueOn"               db:"due_on"`
	ExpiresOn *time.Time `json:"expiresOn,omitempty" db:"expires_on"`

	Status      string  `json:"status"                db:"status"`
	CompletedBy *string `json:"completedBy,omitempty" db:"completed_by"`
	ReportID    *int64  `json:"reportId,omitempty"    db:"report_id"`

	ExecutedAt    *time.Time `json:"executedAt,omitempty" db:"executed_at"`
	Attempts      int        `json:"-"                    db:"attempts"`
	LastAttemptAt *time.Time `json:"-"                    db:"last_attempt_at"`
	// FailureReason is operator-facing: why the last attempt failed. Stays out
	// of the JSON — upstream error text is not written for end users.
	FailureReason *string `json:"-" db:"failure_reason"`

	CreatedAt time.Time `json:"-" db:"created_at"`
	UpdatedAt time.Time `json:"-" db:"updated_at"`
}
