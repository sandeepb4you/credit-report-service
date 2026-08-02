package payments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"credit-report-service/internal/config"
)

// Cashfree PG base URLs per environment.
const (
	cashfreeSandboxURL    = "https://sandbox.cashfree.com/pg"
	cashfreeProductionURL = "https://api.cashfree.com/pg"
)

// CashfreeClient is the real Gateway implementation over the Cashfree PG REST
// API (x-api-version 2025-01-01).
type CashfreeClient struct {
	cfg     config.CashfreeConfig
	baseURL string
	http    *http.Client
}

// NewCashfreeClient builds the HTTP-backed gateway. The base URL is derived
// from cfg.Mode unless cfg.BaseURL overrides it.
func NewCashfreeClient(cfg config.CashfreeConfig) *CashfreeClient {
	base := cfg.BaseURL
	if base == "" {
		if cfg.Mode == "production" {
			base = cashfreeProductionURL
		} else {
			base = cashfreeSandboxURL
		}
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &CashfreeClient{
		cfg:     cfg,
		baseURL: base,
		http:    &http.Client{Timeout: timeout},
	}
}

func (c *CashfreeClient) Mode() string { return c.cfg.Mode }

// ---- Create Order (POST /orders) -----------------------------------------

type cfCustomerDetails struct {
	CustomerID    string `json:"customer_id"`
	CustomerEmail string `json:"customer_email,omitempty"`
	CustomerPhone string `json:"customer_phone"`
	CustomerName  string `json:"customer_name,omitempty"`
}

type cfOrderMeta struct {
	ReturnURL string `json:"return_url,omitempty"`
	NotifyURL string `json:"notify_url,omitempty"`
}

type cfCreateOrderReq struct {
	OrderID         string            `json:"order_id"`
	OrderAmount     float64           `json:"order_amount"`
	OrderCurrency   string            `json:"order_currency"`
	CustomerDetails cfCustomerDetails `json:"customer_details"`
	OrderMeta       *cfOrderMeta      `json:"order_meta,omitempty"`
	OrderNote       string            `json:"order_note,omitempty"`
}

type cfOrderResp struct {
	CFOrderID        string `json:"cf_order_id"`
	OrderID          string `json:"order_id"`
	PaymentSessionID string `json:"payment_session_id"`
	OrderStatus      string `json:"order_status"`
	OrderExpiryTime  string `json:"order_expiry_time"`
}

func (c *CashfreeClient) CreateOrder(ctx context.Context, p CreateOrderParams) (*OrderResult, error) {
	reqBody := cfCreateOrderReq{
		OrderID:       p.OrderID,
		OrderAmount:   p.Amount,
		OrderCurrency: p.Currency,
		CustomerDetails: cfCustomerDetails{
			CustomerID:    p.CustomerID,
			CustomerEmail: p.CustomerEmail,
			CustomerPhone: p.CustomerPhone,
			CustomerName:  p.CustomerName,
		},
		OrderNote: p.OrderNote,
	}
	if p.ReturnURL != "" || p.NotifyURL != "" {
		reqBody.OrderMeta = &cfOrderMeta{ReturnURL: p.ReturnURL, NotifyURL: p.NotifyURL}
	}

	var resp cfOrderResp
	// Cashfree replays the original response for a repeated idempotency key,
	// so a network-level retry of the same order can't create a duplicate.
	if err := c.do(ctx, http.MethodPost, "/orders", p.OrderID, reqBody, &resp); err != nil {
		return nil, err
	}
	return toOrderResult(&resp), nil
}

// ---- Get Order (GET /orders/{order_id}) ----------------------------------

func (c *CashfreeClient) GetOrder(ctx context.Context, orderID string) (*OrderResult, error) {
	var resp cfOrderResp
	if err := c.do(ctx, http.MethodGet, "/orders/"+orderID, "", nil, &resp); err != nil {
		return nil, err
	}
	return toOrderResult(&resp), nil
}

// ---- Webhook signature -----------------------------------------------------

// VerifyWebhookSignature implements Cashfree's scheme: the signature is
// Base64(HMAC-SHA256(timestamp + rawBody)) keyed with the client secret. The
// raw body bytes must be used verbatim; re-serialised JSON will not match.
func (c *CashfreeClient) VerifyWebhookSignature(timestamp string, body []byte, signature string) bool {
	mac := hmac.New(sha256.New, []byte(c.cfg.ClientSecret))
	mac.Write([]byte(timestamp))
	mac.Write(body)
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

// ---- shared ----------------------------------------------------------------

func (c *CashfreeClient) do(ctx context.Context, method, path, idempotencyKey string, body, out interface{}) error {
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
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-version", c.cfg.APIVersion)
	req.Header.Set("x-client-id", c.cfg.ClientID)
	req.Header.Set("x-client-secret", c.cfg.ClientSecret)
	if idempotencyKey != "" {
		req.Header.Set("x-idempotency-key", idempotencyKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cashfree %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("cashfree %s %s: read body: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &GatewayError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}
	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("cashfree %s %s: decode response: %w", method, path, err)
		}
	}
	return nil
}

func toOrderResult(r *cfOrderResp) *OrderResult {
	res := &OrderResult{
		CFOrderID:        r.CFOrderID,
		PaymentSessionID: r.PaymentSessionID,
		Status:           r.OrderStatus,
	}
	if r.OrderExpiryTime != "" {
		if t, err := time.Parse(time.RFC3339, r.OrderExpiryTime); err == nil {
			res.ExpiryTime = &t
		}
	}
	return res
}
