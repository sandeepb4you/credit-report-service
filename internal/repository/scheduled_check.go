package repository

import (
	"context"
	"errors"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"credit-report-service/internal/models"
)

// ScheduledCheckRepo is the data access layer for scheduled_score_checks: the
// prepaid report runs a plan purchase minted, which the daily runner executes
// and a manual check may spend early.
type ScheduledCheckRepo struct{ pool *pgxpool.Pool }

func NewScheduledCheckRepo(pool *pgxpool.Pool) *ScheduledCheckRepo {
	return &ScheduledCheckRepo{pool: pool}
}

const scheduledCheckCols = `id, account_id, order_id, product_code, interval_months,
    sequence_no, due_on, expires_on, status, completed_by, report_id,
    executed_at, attempts, last_attempt_at, failure_reason, created_at, updated_at`

// Mint inserts the whole batch a plan purchase bought, in one transaction:
// either the account gets every run it paid for or none, and a re-run (the
// fulfilment path is webhook-driven and can race its reconcile twin) hits the
// (order_id, sequence_no) unique key and changes nothing.
//
// dueDates must already be the business-timezone due days, index 0 = first run.
func (r *ScheduledCheckRepo) Mint(
	ctx context.Context, accountID, orderID int64, productCode string,
	intervalMonths int, dueDates []time.Time, expiresOn *time.Time,
) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for i, due := range dueDates {
		if _, err := tx.Exec(ctx,
			`INSERT INTO scheduled_score_checks
			     (account_id, order_id, product_code, interval_months,
			      sequence_no, due_on, expires_on)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)
			 ON CONFLICT (order_id, sequence_no) DO NOTHING`,
			accountID, orderID, productCode, intervalMonths, i+1, due, expiresOn,
		); err != nil {
			return classifyPgErr(err)
		}
	}
	return tx.Commit(ctx)
}

// ListByAccount returns the account's whole schedule, upcoming first within
// PENDING and then history, for the app's plan view.
func (r *ScheduledCheckRepo) ListByAccount(ctx context.Context, accountID int64) ([]models.ScheduledScoreCheck, error) {
	rows := []models.ScheduledScoreCheck{}
	err := pgxscan.Select(ctx, r.pool, &rows,
		`SELECT `+scheduledCheckCols+` FROM scheduled_score_checks
		 WHERE account_id = $1
		 ORDER BY due_on, sequence_no, id`, accountID)
	return rows, err
}

// NextPending returns the account's earliest still-owed run, or ErrNotFound.
// This is the read that GATES a manual check (and prices the confirmation
// dialog); the claim itself happens in SpendNextPending, after the report has
// actually been delivered — the same read/claim split as order entitlements.
func (r *ScheduledCheckRepo) NextPending(ctx context.Context, accountID int64) (*models.ScheduledScoreCheck, error) {
	var row models.ScheduledScoreCheck
	err := pgxscan.Get(ctx, r.pool, &row,
		`SELECT `+scheduledCheckCols+` FROM scheduled_score_checks
		 WHERE account_id = $1 AND status = $2
		 ORDER BY due_on, sequence_no, id
		 LIMIT 1`, accountID, models.ScheduledCheckPending)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &row, err
}

// HasForOrder reports whether an order's batch was ever minted (any status).
// The fulfilment self-heal in OrderService.GetOrder gates on this so a poll of
// a healthy PAID order costs one EXISTS and no writes.
func (r *ScheduledCheckRepo) HasForOrder(ctx context.Context, orderID int64) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM scheduled_score_checks WHERE order_id = $1)`,
		orderID).Scan(&exists)
	return exists, err
}

// CountPending is the account's remaining quota: how many prepaid runs are
// still owed. Quota is COUNTED, never tracked in a counter column — the rows
// are the truth.
func (r *ScheduledCheckRepo) CountPending(ctx context.Context, accountID int64) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM scheduled_score_checks
		 WHERE account_id = $1 AND status = $2`,
		accountID, models.ScheduledCheckPending).Scan(&n)
	return n, err
}

// HasPending reports whether the account still holds unrun scheduled checks —
// what blocks buying a second plan while one is still delivering.
func (r *ScheduledCheckRepo) HasPending(ctx context.Context, accountID int64) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (
		     SELECT 1 FROM scheduled_score_checks
		     WHERE account_id = $1 AND status = $2
		 )`, accountID, models.ScheduledCheckPending).Scan(&exists)
	return exists, err
}

// SpendNextPending claims the account's earliest PENDING run against a
// just-delivered report and re-anchors the rest of the batch from the spend
// day: the next run becomes today + interval, the one after + 2×interval, and
// so on — the schedule restarts from the day the user actually checked.
//
// One transaction: the claim and the re-anchor land together or not at all.
// FOR UPDATE SKIP LOCKED on the claim keeps a concurrent runner sweep and a
// manual check from double-spending the same row (the loser simply finds the
// row gone). Returns false when nothing was claimable.
//
// today must be the business-timezone date; completedBy is 'manual' or
// 'runner' (the runner path claims its row up front instead, see ClaimDue —
// this is the manual-spend path).
func (r *ScheduledCheckRepo) SpendNextPending(
	ctx context.Context, accountID, reportID int64, completedBy string, today time.Time,
) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	var spent models.ScheduledScoreCheck
	err = pgxscan.Get(ctx, tx, &spent,
		`SELECT `+scheduledCheckCols+` FROM scheduled_score_checks
		 WHERE account_id = $1 AND status = $2
		 ORDER BY due_on, sequence_no, id
		 LIMIT 1
		 FOR UPDATE SKIP LOCKED`, accountID, models.ScheduledCheckPending)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	if _, err := tx.Exec(ctx,
		`UPDATE scheduled_score_checks SET
		     status = $2, completed_by = $3, report_id = $4,
		     executed_at = now(), attempts = attempts + 1,
		     last_attempt_at = now(), updated_at = now()
		 WHERE id = $1`,
		spent.ID, models.ScheduledCheckDone, completedBy, reportID,
	); err != nil {
		return false, err
	}

	if err := r.reanchorRemaining(ctx, tx, accountID, spent.IntervalMonths, today); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// reanchorRemaining rewrites every remaining PENDING due date for the account
// to today + k×interval, preserving order. Locked (plain FOR UPDATE — there is
// no competing claimer to skip past inside the spend transaction; a concurrent
// runner claim blocks until this commits and then no longer sees the old dates).
func (r *ScheduledCheckRepo) reanchorRemaining(
	ctx context.Context, tx pgx.Tx, accountID int64, intervalMonths int, today time.Time,
) error {
	var ids []int64
	if err := pgxscan.Select(ctx, tx, &ids,
		`SELECT id FROM scheduled_score_checks
		 WHERE account_id = $1 AND status = $2
		 ORDER BY due_on, sequence_no, id
		 FOR UPDATE`, accountID, models.ScheduledCheckPending); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	dates := make([]time.Time, len(ids))
	for k := range ids {
		dates[k] = today.AddDate(0, intervalMonths*(k+1), 0)
	}
	_, err := tx.Exec(ctx,
		`UPDATE scheduled_score_checks t SET due_on = u.due_on, updated_at = now()
		 FROM (SELECT unnest($1::bigint[]) AS id, unnest($2::date[]) AS due_on) u
		 WHERE t.id = u.id`, ids, dates)
	return err
}

// ---- runner ----------------------------------------------------------------

// ClaimDue atomically hands the runner up to limit runs that are owed as of
// today (due_on <= today — yesterday's missed run is still owed), flipping
// them PENDING -> RUNNING. SKIP LOCKED makes concurrent sweeps (two instances,
// or a tick overlapping a slow predecessor) partition the work instead of
// duplicating it; the status guard hides a claimed row from later sweeps.
func (r *ScheduledCheckRepo) ClaimDue(ctx context.Context, today time.Time, limit int) ([]models.ScheduledScoreCheck, error) {
	rows := []models.ScheduledScoreCheck{}
	err := pgxscan.Select(ctx, r.pool, &rows,
		`UPDATE scheduled_score_checks SET
		     status = $1, attempts = attempts + 1,
		     last_attempt_at = now(), updated_at = now()
		 WHERE id IN (
		     SELECT id FROM scheduled_score_checks
		     WHERE status = $2 AND due_on <= $3
		     ORDER BY due_on, sequence_no, id
		     LIMIT $4
		     FOR UPDATE SKIP LOCKED
		 )
		 RETURNING `+scheduledCheckCols, //nolint:gosec // constant column list
		models.ScheduledCheckRunning, models.ScheduledCheckPending, today, limit)
	return rows, err
}

// Complete marks a claimed run DONE with the report it produced.
func (r *ScheduledCheckRepo) Complete(ctx context.Context, id, reportID int64, completedBy string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE scheduled_score_checks SET
		     status = $2, completed_by = $3, report_id = $4,
		     executed_at = now(), failure_reason = NULL, updated_at = now()
		 WHERE id = $1`,
		id, models.ScheduledCheckDone, completedBy, reportID)
	return err
}

// ReleaseFailed returns a claimed run to PENDING after a failed attempt so the
// next sweep retries it — unless the attempt cap is reached, in which case it
// is marked FAILED and stays for an operator. The quota is never burned by a
// failure: only Complete spends the row.
func (r *ScheduledCheckRepo) ReleaseFailed(ctx context.Context, id int64, reason string, maxAttempts int) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE scheduled_score_checks SET
		     status = CASE WHEN attempts >= $3 THEN $4 ELSE $5 END,
		     failure_reason = $2, updated_at = now()
		 WHERE id = $1`,
		id, reason, maxAttempts, models.ScheduledCheckFailed, models.ScheduledCheckPending)
	return err
}

// ReclaimStale returns RUNNING rows whose attempt started before cutoff to
// PENDING: a crash between claim and completion left them orphaned. The
// retry is safe because the run's idempotency key (sched-<id>) replays the
// stored report if the pull itself had already landed.
func (r *ScheduledCheckRepo) ReclaimStale(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE scheduled_score_checks SET status = $1, updated_at = now()
		 WHERE status = $2 AND last_attempt_at < $3`,
		models.ScheduledCheckPending, models.ScheduledCheckRunning, cutoff)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ExpireOverdue voids PENDING runs whose plan year is over (terms: the plan is
// active until the end of the billing year, no proration). Returns how many.
func (r *ScheduledCheckRepo) ExpireOverdue(ctx context.Context, today time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE scheduled_score_checks SET status = $1, updated_at = now()
		 WHERE status = $2 AND expires_on IS NOT NULL AND expires_on < $3`,
		models.ScheduledCheckExpired, models.ScheduledCheckPending, today)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
