package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"

	"credit-report-service/internal/models"
)

// OrderListFilter is everything the admin purchases list can be narrowed by.
// Nil fields are absent filters, as in AccountListFilter.
type OrderListFilter struct {
	// From and To bound when the order was placed, half-open [From, To); nil
	// is an absent bound. Placed rather than paid: an unpaid order has no paid
	// time, and a window that silently dropped every failed order would make
	// the Failed tab useless.
	From *time.Time
	To   *time.Time
	// Statuses is the raw status set of one group (models.OrderStatusGroups);
	// nil is every status.
	Statuses    []string
	PaymentMode *string
	ProductCode *string
	// Search matches buyer name, phone and email, the order uid, the coupon,
	// both gateway ids and the invoice number — whatever the customer read out.
	Search *string
	// OrderBy is a whitelisted fragment from models.OrderSortColumn.
	OrderBy string
	Limit   int
	Offset  int
}

// orderRowsCTE computes one row per order inside the filters.
//
// Entitlement is the customer orders API's rule (OrderService.entitlementOf)
// written as SQL, and must stay in step with it: a plan is ACTIVE while it has
// a PENDING or RUNNING run and ENDED after, a one-time check is USED once
// consumed_at is set, a product the catalog no longer lists has none, and only
// a PAID order has one at all. Two consoles disagreeing about whether a
// customer is still owed a report is exactly the support call this screen
// exists to answer.
//
// $1/$2 window, $3 statuses, $4 mode, $5 product, $6 search;
// $7 PAID, $8/$9 the live plan-run statuses.
const orderRowsCTE = `
	WITH rows AS (
	  SELECT o.id,
	         o.order_uid,
	         o.account_id,
	         TRIM(BOTH ' ' FROM COALESCE(a.first_name, '') || ' ' ||
	                            COALESCE(a.last_name, ''))  AS buyer_name,
	         a.primary_phone                                AS phone,
	         a.primary_email                                AS email,
	         o.product_code,
	         p.name                                         AS product_name,
	         o.amount,
	         o.discount_amount,
	         o.coupon_code,
	         o.currency,
	         o.status,
	         o.payment_mode,
	         o.payment_method,
	         o.cf_order_id,
	         o.cf_payment_id,
	         o.failure_reason,
	         o.created_at,
	         o.paid_at,
	         i.invoice_number,
	         CASE
	           WHEN o.status <> $7 OR p.code IS NULL THEN NULL
	           WHEN p.interval_months IS NOT NULL AND p.interval_months > 0 THEN
	             CASE WHEN EXISTS (SELECT 1 FROM scheduled_score_checks s
	                                WHERE s.order_id = o.id
	                                  AND s.status IN ($8, $9))
	                  THEN 'ACTIVE' ELSE 'ENDED' END
	           WHEN o.consumed_at IS NOT NULL THEN 'USED'
	           ELSE 'UNUSED'
	         END                                            AS entitlement
	    FROM orders o
	    JOIN accounts a      ON a.id = o.account_id
	    LEFT JOIN products p ON p.code = o.product_code
	    LEFT JOIN invoices i ON i.order_id = o.id
	   WHERE ($1::timestamptz IS NULL OR o.created_at >= $1)
	     AND ($2::timestamptz IS NULL OR o.created_at <  $2)
	     AND ($3::text[] IS NULL OR o.status = ANY($3))
	     AND ($4::text IS NULL OR o.payment_mode = $4)
	     AND ($5::text IS NULL OR o.product_code = $5)
	     AND ($6::text IS NULL
	          OR o.order_uid      ILIKE '%' || $6 || '%'
	          OR o.coupon_code    ILIKE '%' || $6 || '%'
	          OR o.cf_order_id    ILIKE '%' || $6 || '%'
	          OR o.cf_payment_id  ILIKE '%' || $6 || '%'
	          OR i.invoice_number ILIKE '%' || $6 || '%'
	          OR a.primary_phone  ILIKE '%' || $6 || '%'
	          OR a.primary_email  ILIKE '%' || $6 || '%'
	          OR TRIM(BOTH ' ' FROM COALESCE(a.first_name, '') || ' ' ||
	                                COALESCE(a.last_name, '')) ILIKE '%' || $6 || '%')
	)`

// ListOrders pages the admin purchases list and totals everything it matched.
//
// The summary and the count come from one query over the same CTE as the page,
// so the "₹X collected" line and the "1–50 of 312" line can never be counting
// different sets.
func (r *OrderRepo) ListOrders(
	ctx context.Context, f OrderListFilter,
) ([]models.AdminOrderRow, models.AdminOrderSummary, error) {
	var statuses any // a typed nil []string would bind as an empty array, not NULL
	if len(f.Statuses) > 0 {
		statuses = f.Statuses
	}
	filterArgs := []any{
		f.From, f.To, statuses, f.PaymentMode, f.ProductCode, f.Search,
		models.OrderPaid, models.ScheduledCheckPending, models.ScheduledCheckRunning,
	}

	var summary models.AdminOrderSummary
	if err := pgxscan.Get(ctx, r.pool, &summary, orderRowsCTE+`
		SELECT COUNT(*)                                                AS orders,
		       COUNT(*) FILTER (WHERE status = $7)                     AS paid,
		       COALESCE(SUM(amount) FILTER (WHERE status = $7), 0)     AS collected,
		       COALESCE(SUM(discount_amount) FILTER (WHERE status = $7), 0) AS discounts,
		       COUNT(DISTINCT account_id) FILTER (WHERE status = $7)   AS buyers
		  FROM rows`, filterArgs...); err != nil {
		return nil, summary, err
	}

	orderBy := f.OrderBy
	if orderBy == "" {
		orderBy = models.OrderSortDefault.SQL(true)
	}
	pageArgs := append(append([]any{}, filterArgs...), f.Limit, f.Offset)

	// The id tie-break makes every sort deterministic, so paging cannot repeat
	// or skip a row when two orders share a value (two ₹299 checks, say).
	var out []models.AdminOrderRow
	err := pgxscan.Select(ctx, r.pool, &out, orderRowsCTE+
		` SELECT order_uid, account_id, buyer_name, phone, email, product_code,
		         product_name, amount, discount_amount, coupon_code, currency,
		         status, payment_mode, payment_method, cf_order_id, cf_payment_id,
		         failure_reason, created_at, paid_at, invoice_number, entitlement
		    FROM rows`+
		fmt.Sprintf(` ORDER BY %s, id DESC LIMIT $10 OFFSET $11`, orderBy),
		pageArgs...)
	if out == nil {
		out = []models.AdminOrderRow{}
	}
	return out, summary, err
}
