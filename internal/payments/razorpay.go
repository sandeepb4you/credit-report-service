package payments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// razorpayBaseURL serves both environments; the key decides which one a call
// lands in.
const razorpayBaseURL = "https://api.razorpay.com/v1"

// RazorpayCredentials is the key set one RazorpayClient signs with.
type RazorpayCredentials struct {
	KeyID         string
	KeySecret     string
	WebhookSecret string
}

// RazorpayClient is the real Gateway implementation over Razorpay's REST API
// (Orders + Payments, HTTP basic auth with the key pair).
type RazorpayClient struct {
	mode    string
	creds   RazorpayCredentials
	baseURL string
	http    *http.Client
}

// NewRazorpayClient builds the HTTP-backed gateway. mode is recorded on every
// order this client creates; config validation has already checked that the
// key's rzp_live_/rzp_test_ prefix agrees with it.
func NewRazorpayClient(mode string, creds RazorpayCredentials, baseURL string, timeout time.Duration) *RazorpayClient {
	if baseURL == "" {
		baseURL = razorpayBaseURL
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &RazorpayClient{
		mode:    mode,
		creds:   creds,
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout},
	}
}

func (c *RazorpayClient) Mode() string  { return c.mode }
func (c *RazorpayClient) KeyID() string { return c.creds.KeyID }

// ---- Create Order (POST /orders) -----------------------------------------

type rzpCreateOrderReq struct {
	// Amount is in the currency's smallest unit — paise for INR.
	Amount   int64             `json:"amount"`
	Currency string            `json:"currency"`
	Receipt  string            `json:"receipt"`
	Notes    map[string]string `json:"notes,omitempty"`
}

type rzpOrder struct {
	ID         string `json:"id"`
	Receipt    string `json:"receipt"`
	Status     string `json:"status"` // created | attempted | paid
	Amount     int64  `json:"amount"`
	AmountPaid int64  `json:"amount_paid"`
}

func (c *RazorpayClient) CreateOrder(ctx context.Context, p CreateOrderParams) (*OrderResult, error) {
	// No customer details are sent: Razorpay's order has no place for them, and
	// the checkout's prefill comes from the app. One less copy of a phone number
	// in a third party's records.
	req := rzpCreateOrderReq{
		Amount:   ToMinorUnits(p.Amount),
		Currency: p.Currency,
		Receipt:  p.OrderID,
		Notes:    map[string]string{"order_uid": p.OrderID},
	}
	if p.OrderNote != "" {
		req.Notes["product"] = p.OrderNote
	}
	var resp rzpOrder
	if err := c.do(ctx, http.MethodPost, "/orders", req, &resp); err != nil {
		return nil, err
	}
	return toOrderResult(&resp), nil
}

// ---- Get Order (GET /orders/{id}) ----------------------------------------

func (c *RazorpayClient) GetOrder(ctx context.Context, gatewayOrderID string) (*OrderResult, error) {
	if err := checkOrderID(gatewayOrderID); err != nil {
		return nil, err
	}
	var resp rzpOrder
	if err := c.do(ctx, http.MethodGet, "/orders/"+gatewayOrderID, nil, &resp); err != nil {
		return nil, err
	}
	return toOrderResult(&resp), nil
}

// ---- Payments for an order (GET /orders/{id}/payments) -------------------

func (c *RazorpayClient) GetPayment(ctx context.Context, gatewayOrderID string) (*PaymentDetails, error) {
	if err := checkOrderID(gatewayOrderID); err != nil {
		return nil, err
	}
	var resp struct {
		Items []rzpPayment `json:"items"`
	}
	if err := c.do(ctx, http.MethodGet, "/orders/"+gatewayOrderID+"/payments", nil, &resp); err != nil {
		return nil, err
	}
	p, err := capturedPayment(resp.Items)
	if err != nil {
		return nil, err
	}
	// The order's payment list carries card_id but not the card itself; the
	// network and last four are only on the payment fetched with the card
	// expanded. Best-effort: without them the invoice still says "Card".
	if p.Method == "card" && p.Card == nil && p.ID != "" {
		var full rzpPayment
		if err := c.do(ctx, http.MethodGet,
			"/payments/"+url.PathEscape(p.ID)+"?expand[]=card", nil, &full); err == nil && full.Card != nil {
			p.Card = full.Card
		}
	}
	return p.details(), nil
}

// ---- Webhook signature -----------------------------------------------------

// VerifyWebhookSignature implements Razorpay's scheme: X-Razorpay-Signature is
// hex(HMAC-SHA256(rawBody)) keyed with the webhook's own secret. The raw body
// bytes must be used verbatim; re-serialised JSON will not match.
//
// No secret means nothing verifies. An HMAC under an empty key is one anybody
// can compute, so accepting it would be accepting every forged delivery.
func (c *RazorpayClient) VerifyWebhookSignature(body []byte, signature string) bool {
	if c.creds.WebhookSecret == "" || signature == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(c.creds.WebhookSecret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(strings.ToLower(strings.TrimSpace(signature))))
}

// ---- shared ----------------------------------------------------------------

func (c *RazorpayClient) do(ctx context.Context, method, path string, body, out interface{}) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.SetBasicAuth(c.creds.KeyID, c.creds.KeySecret)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("razorpay %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("razorpay %s %s: read body: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &GatewayError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}
	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("razorpay %s %s: decode response: %w", method, path, err)
		}
	}
	return nil
}

// checkOrderID refuses anything that is not a Razorpay order id before it is
// put in a URL. Orders created under the previous gateway (Cashfree) carry that
// gateway's ids in the same column; asking Razorpay about one would only ever
// come back 400, so it is answered here without the round trip.
func checkOrderID(id string) error {
	if !strings.HasPrefix(id, "order_") || strings.ContainsAny(id, "/?#") {
		return fmt.Errorf("razorpay: %q is not a Razorpay order id", id)
	}
	return nil
}

// toOrderResult maps Razorpay's order status onto ours. created and attempted
// (a payment was tried and did not go through) both mean the order is still
// payable. paid means a payment was CAPTURED — an authorized-but-uncaptured
// payment leaves the order "attempted", which is why the account must have
// automatic capture on (Dashboard -> Account & Settings -> Payment capture).
func toOrderResult(o *rzpOrder) *OrderResult {
	status := StatusActive
	if o.Status == "paid" {
		status = StatusPaid
	}
	return &OrderResult{GatewayOrderID: o.ID, Status: status}
}

// ToMinorUnits converts a rupee amount to paise. Rounded, not truncated:
// 299.99 * 100 is 29998.999... in floating point.
func ToMinorUnits(amount float64) int64 {
	return int64(math.Round(amount * 100))
}
