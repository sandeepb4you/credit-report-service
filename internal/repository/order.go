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

// OrderRepo is the data access layer for products, orders, and
// payment_webhook_events.
type OrderRepo struct{ pool *pgxpool.Pool }

func NewOrderRepo(pool *pgxpool.Pool) *OrderRepo { return &OrderRepo{pool: pool} }

// BeginTx starts a transaction so the service layer can commit an order and
// its coupon redemption together.
func (r *OrderRepo) BeginTx(ctx context.Context) (pgx.Tx, error) { return r.pool.Begin(ctx) }

// ---- products ------------------------------------------------------------

const productCols = `code, name, amount, description, currency, active, created_at, updated_at`

func (r *OrderRepo) ListActiveProducts(ctx context.Context) ([]models.Product, error) {
	ps := []models.Product{}
	err := pgxscan.Select(ctx, r.pool, &ps,
		`SELECT `+productCols+` FROM products WHERE active ORDER BY code`)
	return ps, err
}

func (r *OrderRepo) FindProduct(ctx context.Context, code string) (*models.Product, error) {
	var p models.Product
	err := pgxscan.Get(ctx, r.pool, &p,
		`SELECT `+productCols+` FROM products WHERE code = $1`, code)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &p, err
}

// ---- orders ---------------------------------------------------------------

const orderCols = `id, order_uid, account_id, product_code, amount, discount_amount,
    coupon_code, currency, status,
    cf_order_id, payment_session_id, cf_payment_id, payment_method, failure_reason,
    order_expiry_time, paid_at, fulfilled_at, consumed_at, consumed_report_id,
    created_at, updated_at`

// CreateOrder inserts the local row before the gateway is called, so the
// order_uid exists to send as the gateway's order_id.
func (r *OrderRepo) CreateOrder(ctx context.Context, o *models.Order) error {
	return r.insertOrder(ctx, r.pool, o)
}

// CreateOrderTx inserts the order inside a caller-managed transaction, so an
// order and the coupon redemption that priced it commit together or not at all.
func (r *OrderRepo) CreateOrderTx(ctx context.Context, tx pgx.Tx, o *models.Order) error {
	return r.insertOrder(ctx, tx, o)
}

// querier is the subset of pgxpool.Pool and pgx.Tx that insertOrder needs, so
// the statement lives in one place regardless of who runs it.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (r *OrderRepo) insertOrder(ctx context.Context, q querier, o *models.Order) error {
	row := q.QueryRow(ctx,
		`INSERT INTO orders
		     (order_uid, account_id, product_code, amount, discount_amount,
		      coupon_code, currency, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING id, created_at, updated_at`,
		o.OrderUID, o.AccountID, o.ProductCode, o.Amount, o.DiscountAmount,
		o.CouponCode, o.Currency, o.Status,
	)
	if err := row.Scan(&o.ID, &o.CreatedAt, &o.UpdatedAt); err != nil {
		return classifyPgErr(err)
	}
	return nil
}

func (r *OrderRepo) FindOrderByUID(ctx context.Context, uid string) (*models.Order, error) {
	var o models.Order
	err := pgxscan.Get(ctx, r.pool, &o,
		`SELECT `+orderCols+` FROM orders WHERE order_uid = $1`, uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &o, err
}

func (r *OrderRepo) ListOrdersByAccount(ctx context.Context, accountID int64) ([]models.Order, error) {
	os := []models.Order{}
	err := pgxscan.Select(ctx, r.pool, &os,
		`SELECT `+orderCols+` FROM orders
		 WHERE account_id = $1 ORDER BY created_at DESC, id DESC`, accountID)
	return os, err
}

// MarkOrderCreated records a successful gateway create-order call.
func (r *OrderRepo) MarkOrderCreated(ctx context.Context, o *models.Order) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE orders SET
		     status = $2, cf_order_id = $3, payment_session_id = $4,
		     order_expiry_time = $5, updated_at = now()
		 WHERE order_uid = $1`,
		o.OrderUID, o.Status, o.CFOrderID, o.PaymentSessionID, o.OrderExpiryTime,
	)
	return classifyPgErr(err)
}

// MarkOrderCreationFailed records a failed gateway create-order call.
func (r *OrderRepo) MarkOrderCreationFailed(ctx context.Context, uid, reason string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE orders SET status = $2, failure_reason = $3, updated_at = now()
		 WHERE order_uid = $1`,
		uid, models.OrderCreationFailed, reason)
	return err
}

// MarkOrderPaid transitions an order to PAID exactly once: the status guard
// makes replayed success webhooks no-ops. Returns true on the first (real)
// transition — the caller triggers fulfilment only then.
func (r *OrderRepo) MarkOrderPaid(ctx context.Context, uid string, cfPaymentID, method *string, paidAt time.Time) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE orders SET
		     status = $2,
		     cf_payment_id = COALESCE($3, cf_payment_id),
		     payment_method = COALESCE($4, payment_method),
		     paid_at = $5,
		     fulfilled_at = now(),
		     updated_at = now()
		 WHERE order_uid = $1 AND status <> $2`,
		uid, models.OrderPaid, cfPaymentID, method, paidAt,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ---- entitlements ---------------------------------------------------------

// UnspentEntitlementPaidAt returns when the account paid for the check that
// SpendEntitlement would claim next, or nil when it holds none.
//
// This is the read used to GATE an action, and it is deliberately separate from
// SpendEntitlement, which claims one. Checking before doing the expensive,
// billable work means a user with no entitlement is turned away without a vendor
// call; claiming afterwards means a vendor failure cannot swallow their purchase.
//
// It returns the timestamp rather than a bool because "do they have one" is not
// enough to decide whether a stored report may satisfy it. A report generated
// BEFORE the money was paid cannot be what that money bought — see
// reusableReport. Ordered to match SpendEntitlement exactly, so the order whose
// paid_at is inspected here is the one that actually gets spent.
//
// A nil paid_at on a PAID row (which should not happen) comes back as nil and is
// treated as no entitlement rather than as a timestamp we could compare against.
func (r *OrderRepo) UnspentEntitlementPaidAt(ctx context.Context, accountID int64, productCode string) (*time.Time, error) {
	var paidAt *time.Time
	err := r.pool.QueryRow(ctx,
		`SELECT paid_at FROM orders
		 WHERE account_id = $1
		   AND product_code = $2
		   AND status = $3
		   AND consumed_at IS NULL
		 ORDER BY paid_at, id
		 LIMIT 1`, accountID, productCode, models.OrderPaid).Scan(&paidAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return paidAt, nil
}

// SpendEntitlement claims the account's oldest unspent PAID order of productCode
// against the report it paid for, and reports whether one was claimed.
//
// Oldest first so a user who bought two checks has both honoured in the order
// they were bought, and so the row a refund would look at is the one whose money
// is actually gone.
//
// FOR UPDATE SKIP LOCKED makes two concurrent claims take two different orders
// rather than both taking one: the subquery locks the row it picks, and a
// competing transaction skips past it to the next unspent order instead of
// blocking and then double-spending the same one.
//
// A false return is not an error. It means the pull ran without an entitlement
// to spend, which is what the gate is supposed to prevent — the caller logs it
// rather than failing a report the user has already received.
func (r *OrderRepo) SpendEntitlement(ctx context.Context, accountID int64, productCode string, reportID int64) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE orders SET
		     consumed_at = now(),
		     consumed_report_id = $4,
		     updated_at = now()
		 WHERE id = (
		     SELECT id FROM orders
		     WHERE account_id = $1
		       AND product_code = $2
		       AND status = $3
		       AND consumed_at IS NULL
		     ORDER BY paid_at, id
		     LIMIT 1
		     FOR UPDATE SKIP LOCKED
		 )`, accountID, productCode, models.OrderPaid, reportID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// UpdateOrderStatus moves an order to a non-PAID terminal/interim status
// (FAILED, EXPIRED, TERMINATED). A PAID order is never downgraded.
func (r *OrderRepo) UpdateOrderStatus(ctx context.Context, uid, status string, failureReason *string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE orders SET
		     status = $2,
		     failure_reason = COALESCE($3, failure_reason),
		     updated_at = now()
		 WHERE order_uid = $1 AND status <> $4`,
		uid, status, failureReason, models.OrderPaid)
	return err
}

// ---- payment_webhook_events ------------------------------------------------

// CreateWebhookEvent records a received webhook. A duplicate idempotency key
// returns ErrConflict, which the service treats as "already handled".
func (r *OrderRepo) CreateWebhookEvent(ctx context.Context, e *models.PaymentWebhookEvent) error {
	row := r.pool.QueryRow(ctx,
		`INSERT INTO payment_webhook_events (idempotency_key, event_type, order_uid, payload)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, received_at`,
		e.IdempotencyKey, e.EventType, e.OrderUID, e.Payload,
	)
	if err := row.Scan(&e.ID, &e.ReceivedAt); err != nil {
		return classifyPgErr(err)
	}
	return nil
}

// MarkWebhookProcessed flags an event as fully handled.
func (r *OrderRepo) MarkWebhookProcessed(ctx context.Context, id int64) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE payment_webhook_events SET processed = TRUE WHERE id = $1`, id)
	return err
}
