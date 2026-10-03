package payments

import (
	"context"
	"log"
)

// StubGateway is the dev fallback used when no Razorpay key is configured. It
// fabricates order results and logs instead of calling out, mirroring the mail
// stub. Webhook signatures always verify.
type StubGateway struct{ mode string }

func NewStubGateway(mode string) *StubGateway {
	if mode == "" {
		mode = "sandbox"
	}
	return &StubGateway{mode: mode}
}

func (s *StubGateway) Mode() string { return s.mode }

// KeyID is obviously not a key: a checkout opened with it fails at once, which
// on a dev machine with no keys is the honest outcome.
func (s *StubGateway) KeyID() string { return "rzp_stub" }

func (s *StubGateway) CreateOrder(_ context.Context, p CreateOrderParams) (*OrderResult, error) {
	log.Printf("[RAZORPAY-STUB] create order %s: %s %.2f %s",
		p.OrderID, p.OrderNote, p.Amount, p.Currency)
	return &OrderResult{GatewayOrderID: "order_stub_" + p.OrderID, Status: StatusActive}, nil
}

func (s *StubGateway) GetOrder(_ context.Context, gatewayOrderID string) (*OrderResult, error) {
	log.Printf("[RAZORPAY-STUB] get order %s", gatewayOrderID)
	return &OrderResult{GatewayOrderID: gatewayOrderID, Status: StatusActive}, nil
}

// GetPayment fabricates a UPI payment, so a local run's invoice has payment
// lines to render. The reference is obviously not a UTR.
func (s *StubGateway) GetPayment(_ context.Context, gatewayOrderID string) (*PaymentDetails, error) {
	log.Printf("[RAZORPAY-STUB] get payments %s", gatewayOrderID)
	return &PaymentDetails{PaymentID: "pay_stub", Group: "upi", BankReference: "STUB000000"}, nil
}

func (s *StubGateway) VerifyWebhookSignature([]byte, string) bool { return true }
