package models

import "time"

// Order status lifecycle. CREATION_REQUESTED/CREATION_FAILED are local-only;
// the rest mirror Cashfree's order_status vocabulary, plus FAILED which we set
// from a payment-failure webhook.
const (
	OrderCreationRequested = "CREATION_REQUESTED" // row saved, Cashfree not yet called
	OrderCreationFailed    = "CREATION_FAILED"    // Cashfree create-order call failed
	OrderActive            = "ACTIVE"             // awaiting payment
	OrderPaid              = "PAID"
	OrderFailed            = "FAILED"
	OrderExpired           = "EXPIRED"
	OrderTerminated        = "TERMINATED"
)

// Product codes seeded in the products table.
const (
	ProductCreditAnalysis        = "CREDIT_ANALYSIS"
	ProductBankStatementAnalysis = "BANK_STATEMENT_ANALYSIS"
	ProductUPIStatementAnalysis  = "UPI_STATEMENT_ANALYSIS"
)

// Product is the row model for the products table: the purchasable catalog.
type Product struct {
	Code   string  `json:"code"   db:"code"`
	Name   string  `json:"name"   db:"name"`
	Amount float64 `json:"amount" db:"amount"`

	// Description is customer-facing copy: newline-separated feature lines that
	// clients render as a checklist on the plans screen.
	Description string `json:"description" db:"description"`

	Currency  string    `json:"currency"  db:"currency"`
	Active    bool      `json:"active"    db:"active"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
	UpdatedAt time.Time `json:"updatedAt" db:"updated_at"`
}

// Order is the row model for the orders table: one purchase attempt. OrderUID
// is our public identifier and the order_id sent to Cashfree; the serial id
// stays internal.
type Order struct {
	ID          int64  `json:"-"           db:"id"`
	OrderUID    string `json:"orderId"     db:"order_uid"`
	AccountID   int64  `json:"-"           db:"account_id"`
	ProductCode string `json:"productCode" db:"product_code"`

	// Amount is what the customer is charged, i.e. already net of any coupon.
	// DiscountAmount and CouponCode are snapshots of how it got there, so the
	// list price is Amount + DiscountAmount and later edits to the coupon can
	// never rewrite this order.
	Amount         float64 `json:"amount"         db:"amount"`
	DiscountAmount float64 `json:"discountAmount" db:"discount_amount"`
	CouponCode     *string `json:"couponCode"     db:"coupon_code"`
	Currency       string  `json:"currency"       db:"currency"`
	Status         string  `json:"status"         db:"status"`

	CFOrderID        *string    `json:"cfOrderId"        db:"cf_order_id"`
	PaymentSessionID *string    `json:"paymentSessionId" db:"payment_session_id"`
	CFPaymentID      *string    `json:"-"                db:"cf_payment_id"`
	PaymentMethod    *string    `json:"paymentMethod"    db:"payment_method"`
	FailureReason    *string    `json:"failureReason"    db:"failure_reason"`
	OrderExpiryTime  *time.Time `json:"-"                db:"order_expiry_time"`

	PaidAt      *time.Time `json:"paidAt" db:"paid_at"`
	FulfilledAt *time.Time `json:"-"      db:"fulfilled_at"`

	CreatedAt time.Time `json:"createdAt" db:"created_at"`
	UpdatedAt time.Time `json:"updatedAt" db:"updated_at"`
}

// PaymentWebhookEvent is the row model for payment_webhook_events: a verbatim
// record of each received gateway webhook, keyed for idempotency.
type PaymentWebhookEvent struct {
	ID             int64     `json:"id"             db:"id"`
	IdempotencyKey *string   `json:"idempotencyKey" db:"idempotency_key"`
	EventType      string    `json:"eventType"      db:"event_type"`
	OrderUID       *string   `json:"orderId"        db:"order_uid"`
	Payload        string    `json:"-"              db:"payload"`
	Processed      bool      `json:"processed"      db:"processed"`
	ReceivedAt     time.Time `json:"receivedAt"     db:"received_at"`
}
