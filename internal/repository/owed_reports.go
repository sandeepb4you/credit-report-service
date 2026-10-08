package repository

import (
	"context"
	"errors"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"

	"credit-report-service/internal/models"
)

// OldestUnspentOrderUID returns the order a one-time pull would spend: the
// account's oldest PAID, unconsumed order of productCode — the same row, in the
// same order, that SpendEntitlement claims. ErrNotFound when there is none.
//
// Used to key a server-initiated pull to the purchase it is spending, so the
// same purchase can never be pulled twice however many times the pull is
// triggered (an approval and an admin's "Run now" a moment later, say).
func (r *OrderRepo) OldestUnspentOrderUID(ctx context.Context, accountID int64, productCode string) (string, error) {
	var uid string
	err := r.pool.QueryRow(ctx,
		`SELECT order_uid FROM orders
		  WHERE account_id = $1
		    AND product_code = $2
		    AND status = $3
		    AND consumed_at IS NULL
		  ORDER BY paid_at, id
		  LIMIT 1`, accountID, productCode, models.OrderPaid).Scan(&uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return uid, err
}

// ListOwedReports returns every customer the service owes a report: an unspent
// one-time order, or a plan whose next refresh is due on or before today.
// Newest payment first. Purged accounts are left out — there is no one to owe.
//
// The one-time half uses HasUnspentEntitlement's predicate, so this list and
// the pull's own paywall can never disagree about who has paid. The plan half
// lists one row per plan (its earliest pending run), so a plan does not appear
// once per month it is behind.
func (r *OrderRepo) ListOwedReports(ctx context.Context, today time.Time) ([]models.OwedReportRow, error) {
	var out []models.OwedReportRow
	err := pgxscan.Select(ctx, r.pool, &out, `
		WITH owed AS (
		  SELECT o.account_id, $3::text AS source, o.order_uid, o.product_code,
		         o.payment_mode, o.paid_at, NULL::date AS due_on
		    FROM orders o
		   WHERE o.status = $1
		     AND o.consumed_at IS NULL
		     AND o.product_code = $2
		  UNION ALL
		  SELECT s.account_id, $4::text, o.order_uid, s.product_code,
		         o.payment_mode, o.paid_at, s.due_on
		    FROM scheduled_score_checks s
		    JOIN orders o ON o.id = s.order_id
		   WHERE s.status = $5
		     AND s.due_on <= $6
		     AND s.sequence_no = (SELECT MIN(s2.sequence_no)
		                            FROM scheduled_score_checks s2
		                           WHERE s2.order_id = s.order_id
		                             AND s2.status = $5)
		)
		SELECT w.account_id,
		       TRIM(BOTH ' ' FROM COALESCE(a.first_name, '') || ' ' ||
		                          COALESCE(a.last_name, ''))  AS name,
		       a.primary_phone                                AS phone,
		       a.primary_email                                AS email,
		       k.status                                       AS kyc_status,
		       w.source, w.order_uid, w.product_code, w.payment_mode,
		       w.paid_at, w.due_on,
		       lp.created_at                                  AS last_pull_at,
		       COALESCE(NOT lp.ok, false)                     AS last_pull_failed
		  FROM owed w
		  JOIN accounts a          ON a.id = w.account_id
		  LEFT JOIN kyc_records k  ON k.account_id = w.account_id
		  LEFT JOIN LATERAL (
		    SELECT cr.created_at, (`+succeededPredicate+`) AS ok
		      FROM credit_analytics_requests cr
		     WHERE cr.account_id = w.account_id
		     ORDER BY cr.id DESC
		     LIMIT 1
		  ) lp ON true
		 WHERE a.status <> $7
		 ORDER BY w.paid_at DESC NULLS LAST, w.order_uid`,
		models.OrderPaid, models.ProductCreditAnalysis, models.OwedFromOrder, models.OwedFromPlan,
		models.ScheduledCheckPending, today, models.AccountDeleted)
	if out == nil {
		out = []models.OwedReportRow{}
	}
	return out, err
}
