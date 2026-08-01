// Package payments abstracts the payment gateway (Cashfree PG) behind an
// interface so a log-only stub can be swapped in for tests / local dev,
// mirroring the Mailer and ocr.Provider conventions.
package payments

import (
	"context"
	"fmt"
	"time"
)

// CreateOrderParams is everything needed to open a payment order with the
// gateway. OrderID is our identifier (orders.order_uid) and becomes the
// gateway's order_id.
type CreateOrderParams struct {
	OrderID       string
	Amount        float64
	Currency      string
	CustomerID    string
	CustomerEmail string
	CustomerPhone string
	CustomerName  string
	ReturnURL     string
	NotifyURL     string
	OrderNote     string
}

// OrderResult is the gateway's view of an order. Status uses Cashfree's
// vocabulary: ACTIVE | PAID | EXPIRED | TERMINATED | TERMINATION_REQUESTED.
type OrderResult struct {
	CFOrderID        string
	PaymentSessionID string
	Status           string
	ExpiryTime       *time.Time
}

// Gateway is the payment-gateway abstraction used by the order service.
type Gateway interface {
	// CreateOrder opens an order and returns the payment session the frontend
	// uses to launch checkout.
	CreateOrder(ctx context.Context, p CreateOrderParams) (*OrderResult, error)
	// GetOrder fetches the current order state, for reconciliation when a
	// webhook may have been missed.
	GetOrder(ctx context.Context, orderID string) (*OrderResult, error)
	// VerifyWebhookSignature checks the HMAC signature of a webhook delivery
	// against the raw (unparsed) request body.
	VerifyWebhookSignature(timestamp string, body []byte, signature string) bool
	// Mode is "sandbox" or "production" — the frontend needs it to initialise
	// the Cashfree JS SDK with the matching environment.
	Mode() string
}

// GatewayError carries the gateway's HTTP status and response body so the
// service layer can log the detail without leaking it to clients.
type GatewayError struct {
	StatusCode int
	Body       string
}

func (e *GatewayError) Error() string {
	return fmt.Sprintf("cashfree: HTTP %d: %s", e.StatusCode, e.Body)
}
