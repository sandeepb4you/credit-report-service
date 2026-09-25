package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"credit-report-service/internal/models"
)

// InvoiceRepo is the data access layer for invoices, their number counters and
// their email log. See migration 0032.
type InvoiceRepo struct{ pool *pgxpool.Pool }

func NewInvoiceRepo(pool *pgxpool.Pool) *InvoiceRepo { return &InvoiceRepo{pool: pool} }

const invoiceCols = `id, invoice_number, specimen, order_id, order_uid, account_id, issued_at,
    product_code, valid_until, currency, total_paise, taxable_paise, cgst_paise, sgst_paise,
    igst_paise, gst_rate_percent, list_price_paise, discount_paise, coupon_code,
    place_of_supply, place_of_supply_code, supplier_name, supplier_address, supplier_gstin, sac,
    billed_to_name, billed_to_email, billed_to_phone, details,
    payment_label, payment_ref_label, payment_ref, payment_fetch_attempts, pdf_uri,
    auto_email, auto_email_attempts, next_attempt_at, last_error, created_at, updated_at`

// Issue numbers and inserts the invoice for inv.OrderID, or returns the one
// that already exists. created reports which.
//
// One transaction, three steps, and the order of them is the point:
//
//  1. Lock the order row. Two paths can reach issue for the same order at once
//     (a webhook and the app's reconcile poll); the lock makes the second wait
//     and then find the first one's invoice, rather than both taking a number.
//  2. Take the next number from the (series, year) counter, under that row's
//     lock. The upsert starts a new year at 1.
//  3. Insert the invoice with it.
//
// A failure anywhere rolls all three back, so a number is spent exactly when an
// invoice exists — the gapless series rule 46 asks for.
func (r *InvoiceRepo) Issue(
	ctx context.Context, inv *models.Invoice, series, financialYear string,
) (*models.Invoice, bool, error) {
	if inv.OrderID == nil {
		return nil, false, errors.New("issue invoice: no order id")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var locked int64
	if err := tx.QueryRow(ctx, `SELECT id FROM orders WHERE id = $1 FOR UPDATE`, *inv.OrderID).
		Scan(&locked); err != nil {
		return nil, false, fmt.Errorf("lock order: %w", err)
	}

	var existing models.Invoice
	err = pgxscan.Get(ctx, tx, &existing,
		`SELECT `+invoiceCols+` FROM invoices WHERE order_id = $1`, *inv.OrderID)
	if err == nil {
		return &existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}

	var n int
	if err := tx.QueryRow(ctx,
		`INSERT INTO invoice_counters (series, financial_year, last_number) VALUES ($1, $2, 1)
		 ON CONFLICT (series, financial_year)
		   DO UPDATE SET last_number = invoice_counters.last_number + 1
		 RETURNING last_number`, series, financialYear).Scan(&n); err != nil {
		return nil, false, fmt.Errorf("next invoice number: %w", err)
	}
	inv.Number = FormatInvoiceNumber(series, financialYear, n)

	var out models.Invoice
	err = pgxscan.Get(ctx, tx, &out,
		`INSERT INTO invoices (
		     invoice_number, specimen, order_id, order_uid, account_id, issued_at,
		     product_code, valid_until, currency, total_paise, taxable_paise,
		     cgst_paise, sgst_paise, igst_paise, gst_rate_percent, list_price_paise,
		     discount_paise, coupon_code, place_of_supply, place_of_supply_code,
		     supplier_name, supplier_address, supplier_gstin, sac,
		     billed_to_name, billed_to_email, billed_to_phone, details,
		     payment_label, payment_ref_label, payment_ref, auto_email, next_attempt_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,
		         $21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33)
		 RETURNING `+invoiceCols,
		inv.Number, inv.Specimen, inv.OrderID, inv.OrderUID, inv.AccountID, inv.IssuedAt,
		inv.ProductCode, inv.ValidUntil, inv.Currency, inv.TotalPaise, inv.TaxablePaise,
		inv.CGSTPaise, inv.SGSTPaise, inv.IGSTPaise, inv.GSTRatePercent, inv.ListPricePaise,
		inv.DiscountPaise, inv.CouponCode, inv.PlaceOfSupply, inv.PlaceOfSupplyCode,
		inv.SupplierName, inv.SupplierAddress, inv.SupplierGSTIN, inv.SAC,
		inv.BilledToName, inv.BilledToEmail, inv.BilledToPhone, inv.Details,
		inv.PaymentLabel, inv.PaymentRefLabel, inv.PaymentRef, inv.AutoEmail, inv.NextAttemptAt)
	if err != nil {
		return nil, false, fmt.Errorf("insert invoice: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return &out, true, nil
}

// FormatInvoiceNumber renders MSC/26-27/000184. Six digits covers a million
// invoices a year, and the whole stays within rule 46's sixteen characters for
// a three-letter series.
func FormatInvoiceNumber(series, financialYear string, n int) string {
	return fmt.Sprintf("%s/%s/%06d", series, financialYear, n)
}

func (r *InvoiceRepo) FindByOrderID(ctx context.Context, orderID int64) (*models.Invoice, error) {
	return r.findOne(ctx, `WHERE order_id = $1`, orderID)
}

func (r *InvoiceRepo) FindByID(ctx context.Context, id int64) (*models.Invoice, error) {
	return r.findOne(ctx, `WHERE id = $1`, id)
}

func (r *InvoiceRepo) findOne(ctx context.Context, where string, arg any) (*models.Invoice, error) {
	var inv models.Invoice
	err := pgxscan.Get(ctx, r.pool, &inv, `SELECT `+invoiceCols+` FROM invoices `+where, arg)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &inv, nil
}

// ListByAccount returns the account's invoices that still belong to an order
// (a reset leaves its invoices orderless, and those are nobody's purchase any
// more as far as My Purchases is concerned).
func (r *InvoiceRepo) ListByAccount(ctx context.Context, accountID int64) ([]models.Invoice, error) {
	out := []models.Invoice{}
	err := pgxscan.Select(ctx, r.pool, &out,
		`SELECT `+invoiceCols+` FROM invoices
		  WHERE account_id = $1 AND order_id IS NOT NULL`, accountID)
	return out, err
}

// SetPayment records the payment lines once they have been read from Cashfree.
func (r *InvoiceRepo) SetPayment(ctx context.Context, id int64, label, refLabel, ref *string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE invoices SET payment_label = $2, payment_ref_label = $3, payment_ref = $4,
		        updated_at = now()
		  WHERE id = $1`, id, label, refLabel, ref)
	return err
}

// CountPaymentFetch records one attempt to read the payment record, so an
// order whose payment Cashfree cannot describe stops being asked about.
func (r *InvoiceRepo) CountPaymentFetch(ctx context.Context, id int64) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE invoices SET payment_fetch_attempts = payment_fetch_attempts + 1, updated_at = now()
		  WHERE id = $1`, id)
	return err
}

func (r *InvoiceRepo) SetPDF(ctx context.Context, id int64, uri string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE invoices SET pdf_uri = $2, updated_at = now() WHERE id = $1`, id, uri)
	return err
}

// DuePendingEmails returns invoices whose automatic email is owed now.
func (r *InvoiceRepo) DuePendingEmails(ctx context.Context, limit int) ([]models.Invoice, error) {
	out := []models.Invoice{}
	err := pgxscan.Select(ctx, r.pool, &out,
		`SELECT `+invoiceCols+` FROM invoices
		  WHERE auto_email = $1 AND (next_attempt_at IS NULL OR next_attempt_at <= now())
		  ORDER BY id LIMIT $2`, models.InvoiceEmailPending, limit)
	return out, err
}

// SetAutoEmail moves the automatic email out of PENDING (SENT, NO_ADDRESS).
// Guarded on PENDING so a purge that cancelled it mid-send is not overwritten.
func (r *InvoiceRepo) SetAutoEmail(ctx context.Context, id int64, status string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE invoices SET auto_email = $2, next_attempt_at = NULL, updated_at = now()
		  WHERE id = $1 AND auto_email = $3`, id, status, models.InvoiceEmailPending)
	return err
}

// RetryAutoEmail records a failed attempt. With next nil the retries are
// spent and the email is marked FAILED.
func (r *InvoiceRepo) RetryAutoEmail(ctx context.Context, id int64, cause string, next *time.Time) error {
	status := models.InvoiceEmailPending
	if next == nil {
		status = models.InvoiceEmailFailed
	}
	_, err := r.pool.Exec(ctx,
		`UPDATE invoices SET auto_email = $2, auto_email_attempts = auto_email_attempts + 1,
		        next_attempt_at = $3, last_error = $4, updated_at = now()
		  WHERE id = $1 AND auto_email = $5`,
		id, status, next, cause, models.InvoiceEmailPending)
	return err
}

// RecordSend logs one email the invoice went out in.
func (r *InvoiceRepo) RecordSend(
	ctx context.Context, invoiceID, accountID int64, kind, recipientHash string,
) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO invoice_email_sends (invoice_id, account_id, kind, recipient_hash)
		 VALUES ($1, $2, $3, $4)`, invoiceID, accountID, kind, recipientHash)
	return err
}

// SendStats is what the rate limits need: when the invoice last went out, and
// how many one-time sends it has had since `since`.
func (r *InvoiceRepo) SendStats(
	ctx context.Context, invoiceID int64, since time.Time,
) (last *time.Time, oneTime int, err error) {
	err = r.pool.QueryRow(ctx,
		`SELECT max(sent_at),
		        count(*) FILTER (WHERE kind = $3 AND sent_at >= $2)
		   FROM invoice_email_sends WHERE invoice_id = $1`,
		invoiceID, since, models.InvoiceSendOneTime).Scan(&last, &oneTime)
	return last, oneTime, err
}

// PaidOrdersMissingInvoice finds orders paid since `since` that have no
// invoice: an issue that failed at fulfilment, or live orders paid while no
// GSTIN was configured. Bounded in time on purpose — the caller passes a short
// window, so the first sweep after invoicing is deployed (or a GSTIN is set)
// picks up that window's orders and nothing older is ever invoiced after the
// fact.
func (r *InvoiceRepo) PaidOrdersMissingInvoice(
	ctx context.Context, since time.Time, limit int,
) ([]models.Order, error) {
	out := []models.Order{}
	err := pgxscan.Select(ctx, r.pool, &out,
		`SELECT `+orderCols+` FROM orders o
		  WHERE o.status = $1 AND o.paid_at >= $2
		    AND NOT EXISTS (SELECT 1 FROM invoices i WHERE i.order_id = o.id)
		  ORDER BY o.id LIMIT $3`, models.OrderPaid, since, limit)
	return out, err
}
