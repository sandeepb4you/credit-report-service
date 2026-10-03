// Package payments abstracts the payment gateway (Razorpay) behind an
// interface so a log-only stub can be swapped in for tests / local dev,
// mirroring the Mailer and ocr.Provider conventions.
package payments

import (
	"context"
	"fmt"
	"time"
)

// Order statuses in the gateway-neutral vocabulary the orders table stores
// (models.Order*). Razorpay's own created/attempted/paid are mapped onto these.
const (
	StatusActive = "ACTIVE"
	StatusPaid   = "PAID"
)

// CreateOrderParams is everything needed to open a payment order with the
// gateway. OrderID is our identifier (orders.order_uid); it travels as the
// gateway order's receipt, which is how a webhook finds its way back.
type CreateOrderParams struct {
	OrderID   string
	Amount    float64
	Currency  string
	OrderNote string
}

// OrderResult is the gateway's view of an order.
type OrderResult struct {
	// GatewayOrderID is Razorpay's order id (order_...): what the checkout is
	// opened with, and what every later lookup is keyed by.
	GatewayOrderID string
	// Status is StatusActive or StatusPaid.
	Status     string
	ExpiryTime *time.Time
}

// Gateway is the payment-gateway abstraction used by the order service.
type Gateway interface {
	// CreateOrder opens an order the frontend then launches checkout against.
	CreateOrder(ctx context.Context, p CreateOrderParams) (*OrderResult, error)
	// GetOrder fetches the current order state by the gateway's order id, for
	// reconciliation when a webhook may have been missed.
	GetOrder(ctx context.Context, gatewayOrderID string) (*OrderResult, error)
	// GetPayment returns the successful payment on an order, for the invoice's
	// payment lines. ErrNoSuccessfulPayment when there is none.
	GetPayment(ctx context.Context, gatewayOrderID string) (*PaymentDetails, error)
	// VerifyWebhookSignature checks the HMAC signature of a webhook delivery
	// against the raw (unparsed) request body.
	VerifyWebhookSignature(body []byte, signature string) bool
	// Mode is "sandbox" or "production" — the environment every order created
	// through this gateway records, and is settled by.
	Mode() string
	// KeyID is the public key the checkout is opened with. It selects the
	// Razorpay environment on the client, so it must come from the same gateway
	// that created the order.
	KeyID() string
}

// GatewayError carries the gateway's HTTP status and response body so the
// service layer can log the detail without leaking it to clients.
type GatewayError struct {
	StatusCode int
	Body       string
}

func (e *GatewayError) Error() string {
	return fmt.Sprintf("razorpay: HTTP %d: %s", e.StatusCode, e.Body)
}
