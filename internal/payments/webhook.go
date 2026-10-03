package payments

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// WebhookKind is what a delivery means for the order it names.
type WebhookKind int

const (
	// WebhookIgnored changes nothing: an event we do not act on.
	WebhookIgnored WebhookKind = iota
	// WebhookPaid means money was captured against the order.
	WebhookPaid
	// WebhookPaymentFailed is one failed attempt. It does NOT end the order:
	// Razorpay's checkout stays open for another try on the same order, so the
	// order is left payable and only the reason is recorded.
	WebhookPaymentFailed
)

// Razorpay events acted on. Subscribe the dashboard webhook to these three.
const (
	eventOrderPaid       = "order.paid"
	eventPaymentCaptured = "payment.captured"
	eventPaymentFailed   = "payment.failed"
)

// WebhookEvent is a verified delivery, decoded into what the order service
// needs and nothing more.
type WebhookEvent struct {
	// Type is the gateway's event name, stored with the raw payload.
	Type string
	Kind WebhookKind
	// OrderUID is our order id when the delivery carries it (an order.paid
	// event has the order's receipt). Empty otherwise — GatewayOrderID then
	// finds the order.
	OrderUID       string
	GatewayOrderID string
	PaymentID      string
	// PaymentGroup is the payment group in the orders table's vocabulary.
	PaymentGroup  string
	PaidAt        time.Time
	FailureReason string
	// Payment is the payment's description for the invoice, nil when absent.
	Payment *PaymentDetails
}

// ErrInvalidWebhook is returned for a body that is not a Razorpay event.
var ErrInvalidWebhook = errors.New("invalid webhook payload")

type rzpWebhook struct {
	Event     string `json:"event"`
	CreatedAt int64  `json:"created_at"`
	Payload   struct {
		Payment *struct {
			Entity rzpPayment `json:"entity"`
		} `json:"payment"`
		Order *struct {
			Entity rzpOrder `json:"entity"`
		} `json:"order"`
	} `json:"payload"`
}

// ParseWebhook decodes a Razorpay webhook body. The signature must already
// have been checked against the raw bytes.
func ParseWebhook(body []byte) (*WebhookEvent, error) {
	var w rzpWebhook
	if err := json.Unmarshal(body, &w); err != nil || w.Event == "" {
		return nil, ErrInvalidWebhook
	}
	ev := &WebhookEvent{Type: w.Event}
	if o := w.Payload.Order; o != nil {
		ev.OrderUID = o.Entity.Receipt
		ev.GatewayOrderID = o.Entity.ID
	}
	if p := w.Payload.Payment; p != nil {
		pay := p.Entity
		if ev.GatewayOrderID == "" {
			ev.GatewayOrderID = pay.OrderID
		}
		ev.PaymentID = pay.ID
		ev.PaymentGroup = pay.group()
		if pay.Method != "" {
			ev.Payment = pay.details()
		}
		ev.FailureReason = failureText(pay)
	}

	switch strings.ToLower(w.Event) {
	case eventOrderPaid, eventPaymentCaptured:
		ev.Kind = WebhookPaid
		ev.PaidAt = time.Now().UTC()
		if w.CreatedAt > 0 {
			ev.PaidAt = time.Unix(w.CreatedAt, 0).UTC()
		}
	case eventPaymentFailed:
		ev.Kind = WebhookPaymentFailed
	default:
		ev.Kind = WebhookIgnored
	}
	return ev, nil
}

// failureText is the reason a payment failed, in Razorpay's customer-facing
// wording ("Payment was cancelled by the user"), falling back to its code.
func failureText(p rzpPayment) string {
	if d := strings.TrimSpace(p.ErrorDesc); d != "" {
		return d
	}
	return strings.TrimSpace(p.ErrorReason)
}
