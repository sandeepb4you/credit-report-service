package payments

import (
	"context"
	"log"
)

// StubGateway is the dev fallback used when no Cashfree client-id is
// configured. It fabricates order results and logs instead of calling out,
// mirroring the mail stub. Webhook signatures always verify.
type StubGateway struct{ mode string }

func NewStubGateway(mode string) *StubGateway {
	if mode == "" {
		mode = "sandbox"
	}
	return &StubGateway{mode: mode}
}

func (s *StubGateway) Mode() string { return s.mode }

func (s *StubGateway) CreateOrder(_ context.Context, p CreateOrderParams) (*OrderResult, error) {
	log.Printf("[CASHFREE-STUB] create order %s: %s %.2f %s for customer %s",
		p.OrderID, p.OrderNote, p.Amount, p.Currency, p.CustomerID)
	return &OrderResult{
		CFOrderID:        "stub-cf-" + p.OrderID,
		PaymentSessionID: "stub-session-" + p.OrderID,
		Status:           "ACTIVE",
	}, nil
}

func (s *StubGateway) GetOrder(_ context.Context, orderID string) (*OrderResult, error) {
	log.Printf("[CASHFREE-STUB] get order %s", orderID)
	return &OrderResult{
		CFOrderID:        "stub-cf-" + orderID,
		PaymentSessionID: "stub-session-" + orderID,
		Status:           "ACTIVE",
	}, nil
}

func (s *StubGateway) VerifyWebhookSignature(string, []byte, string) bool { return true }
