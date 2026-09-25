// Live payments for the store app, sandbox payments for internal builds, in one
// deployment — against real handlers and a real Postgres.
//
// The stub gateway the rest of the suite uses cannot test this: it verifies
// every webhook signature, so it cannot show a sandbox-signed delivery being
// REFUSED for a live order, which is the property that matters most here.
// Sandbox payments cost nothing to make; if one could settle a live order, a
// test build would be a way to get paid-for bureau pulls free. These tests use
// two fake gateways that each sign with their own secret, as Cashfree's two
// environments do.
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"credit-report-service/internal/payments"
)

const testPaymentsKey = "internal-build-key"

// modeGateway is a fake Cashfree environment: it creates orders, answers
// GetOrder from a status the test sets, and accepts only webhooks signed with
// its own secret.
type modeGateway struct {
	mode   string
	secret string

	mu       sync.Mutex
	statuses map[string]string // order uid -> status GetOrder reports
	lookups  []string          // order uids GetOrder was asked about
}

func newModeGateway(mode string) *modeGateway {
	return &modeGateway{mode: mode, secret: "secret-" + mode, statuses: map[string]string{}}
}

func (g *modeGateway) Mode() string { return g.mode }

func (g *modeGateway) CreateOrder(_ context.Context, p payments.CreateOrderParams) (*payments.OrderResult, error) {
	return &payments.OrderResult{
		CFOrderID:        "cf-" + g.mode + "-" + p.OrderID,
		PaymentSessionID: "session-" + g.mode,
		Status:           "ACTIVE",
	}, nil
}

func (g *modeGateway) GetOrder(_ context.Context, orderID string) (*payments.OrderResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lookups = append(g.lookups, orderID)
	status := g.statuses[orderID]
	if status == "" {
		status = "ACTIVE"
	}
	return &payments.OrderResult{CFOrderID: "cf-" + orderID, Status: status}, nil
}

func (g *modeGateway) VerifyWebhookSignature(_ string, _ []byte, signature string) bool {
	return signature == g.signature()
}

func (g *modeGateway) signature() string { return "sig-" + g.secret }

func (g *modeGateway) setStatus(orderUID, status string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.statuses[orderUID] = status
}

func (g *modeGateway) lookedUp(orderUID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, id := range g.lookups {
		if id == orderUID {
			return true
		}
	}
	return false
}

// liveDeployment is the production shape: live by default, sandbox for builds
// that carry the key.
func liveDeployment(t *testing.T) (*harness, *modeGateway, *modeGateway) {
	live, sandbox := newModeGateway("production"), newModeGateway("sandbox")
	h := newHarness(t, paymentSetup{gateway: live, testGateway: sandbox, testKey: testPaymentsKey})
	return h, live, sandbox
}

// createOrder calls POST /api/orders the way the app does, with the internal
// build's key header when testKey is not empty.
func (h *harness) createOrder(token, productCode, testKey string) response {
	h.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/orders/",
		strings.NewReader(fmt.Sprintf(`{"productCode":%q}`, productCode)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if testKey != "" {
		req.Header.Set("X-Test-Payments-Key", testKey)
	}
	return h.send(req)
}

// deliverPaid posts a PAYMENT_SUCCESS webhook for orderUID with the given
// signature and returns the HTTP status.
func (h *harness) deliverPaid(orderUID, signature string) int {
	h.t.Helper()
	body := fmt.Sprintf(`{"type":"PAYMENT_SUCCESS_WEBHOOK","data":{"order":{"order_id":%q},
		"payment":{"cf_payment_id":"pay-1","payment_status":"SUCCESS","payment_group":"upi",
		"payment_time":%q}}}`, orderUID, time.Now().UTC().Format(time.RFC3339))
	req := httptest.NewRequest(http.MethodPost, "/api/payments/cashfree/webhook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-webhook-timestamp", "1")
	req.Header.Set("x-webhook-signature", signature)
	req.Header.Set("x-idempotency-key", fmt.Sprintf("wh-%s-%d", orderUID, time.Now().UnixNano()))
	return h.send(req).Status
}

func (h *harness) send(req *http.Request) response {
	h.t.Helper()
	res, err := h.app.Test(req, -1)
	if err != nil {
		h.t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		h.t.Fatalf("read %s %s body: %v", req.Method, req.URL.Path, err)
	}
	out := response{Status: res.StatusCode, Raw: string(raw)}
	_ = json.Unmarshal(raw, &out.Body)
	return out
}

func (h *harness) orderColumn(orderUID, column string) string {
	h.t.Helper()
	var v string
	// column is a literal from this file only.
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT `+column+`::text FROM orders WHERE order_uid = $1`, orderUID).Scan(&v); err != nil {
		h.t.Fatalf("read orders.%s for %s: %v", column, orderUID, err)
	}
	return v
}

func (h *harness) countOrders() int {
	h.t.Helper()
	var n int
	if err := h.pool.QueryRow(h.baseCtx, `SELECT count(*) FROM orders`).Scan(&n); err != nil {
		h.t.Fatalf("count orders: %v", err)
	}
	return n
}

func orderUIDOf(t *testing.T, res response) string {
	t.Helper()
	uid, _ := res.Body["orderId"].(string)
	if uid == "" {
		t.Fatalf("no orderId in create response: %d %s", res.Status, res.Raw)
	}
	return uid
}

func TestStoreBuildsPayLiveAndInternalBuildsPaySandbox(t *testing.T) {
	h, _, _ := liveDeployment(t)
	token, _ := h.signInByPhone("+919000000701", "")

	store := h.createOrder(token, "CREDIT_ANALYSIS", "")
	if store.Status != http.StatusCreated {
		t.Fatalf("store order: %d %s", store.Status, store.Raw)
	}
	if got := store.Body["mode"]; got != "production" {
		t.Errorf("store order mode = %v, want production (the app opens checkout in this)", got)
	}
	if got := h.orderColumn(orderUIDOf(t, store), "payment_mode"); got != "production" {
		t.Errorf("store order stored as %q, want production", got)
	}

	internal := h.createOrder(token, "CREDIT_ANALYSIS", testPaymentsKey)
	if internal.Status != http.StatusCreated {
		t.Fatalf("internal order: %d %s", internal.Status, internal.Raw)
	}
	if got := internal.Body["mode"]; got != "sandbox" {
		t.Errorf("internal order mode = %v, want sandbox", got)
	}
	if got := h.orderColumn(orderUIDOf(t, internal), "payment_mode"); got != "sandbox" {
		t.Errorf("internal order stored as %q, want sandbox", got)
	}
}

// A wrong key is refused, not quietly treated as a live purchase: only an
// internal build ever sends one, and a misbuilt one falling through to live
// would charge a tester real money for what they think is a test.
func TestAWrongTestKeyIsRefusedAndWritesNothing(t *testing.T) {
	h, _, _ := liveDeployment(t)
	token, _ := h.signInByPhone("+919000000702", "")
	before := h.countOrders()

	res := h.createOrder(token, "CREDIT_ANALYSIS", "not-the-key")
	if res.Status != http.StatusForbidden {
		t.Fatalf("wrong key: %d %s, want 403", res.Status, res.Raw)
	}
	if after := h.countOrders(); after != before {
		t.Errorf("a refused key left %d order row(s) behind", after-before)
	}
}

// The property the whole design rests on.
func TestASandboxSignedWebhookCannotSettleALiveOrder(t *testing.T) {
	h, live, sandbox := liveDeployment(t)
	token, _ := h.signInByPhone("+919000000703", "")
	uid := orderUIDOf(t, h.createOrder(token, "CREDIT_ANALYSIS", ""))

	if status := h.deliverPaid(uid, sandbox.signature()); status != http.StatusUnauthorized {
		t.Errorf("sandbox-signed webhook for a live order answered %d, want 401", status)
	}
	if got := h.orderColumn(uid, "status"); got == "PAID" {
		t.Fatalf("a sandbox-signed webhook marked a live order PAID")
	}

	if status := h.deliverPaid(uid, live.signature()); status != http.StatusOK {
		t.Fatalf("live-signed webhook answered %d", status)
	}
	if got := h.orderColumn(uid, "status"); got != "PAID" {
		t.Errorf("live order status after a live webhook = %q, want PAID", got)
	}
}

// Reconciling asks the environment that created the order — which is also what
// keeps orders opened before a switch settling after it.
func TestReconcileGoesToTheOrdersOwnEnvironment(t *testing.T) {
	h, live, sandbox := liveDeployment(t)
	token, _ := h.signInByPhone("+919000000704", "")
	uid := orderUIDOf(t, h.createOrder(token, "CREDIT_ANALYSIS", testPaymentsKey))

	sandbox.setStatus(uid, "PAID")
	res := h.get("/api/orders/"+uid, token)
	if res.Status != http.StatusOK {
		t.Fatalf("get order: %d %s", res.Status, res.Raw)
	}
	if got := res.Body["status"]; got != "PAID" {
		t.Errorf("sandbox order after reconcile = %v, want PAID", got)
	}
	if live.lookedUp(uid) {
		t.Errorf("a sandbox order was reconciled against the live gateway")
	}
}

// Referral rewards are paid out in real money, so a sandbox purchase in a live
// deployment earns nothing — otherwise a test build is a way to farm payouts.
func TestASandboxPurchaseEarnsNoReferralRewardOnceLive(t *testing.T) {
	h, live, sandbox := liveDeployment(t)
	referrerToken, referrerID := h.signInByPhone("+919000000705", "")
	code := h.referralCodeOf(referrerToken)

	tester, _ := h.signInByPhone("+919000000706", code)
	testUID := orderUIDOf(t, h.createOrder(tester, "CREDIT_ANALYSIS", testPaymentsKey))
	if status := h.deliverPaid(testUID, sandbox.signature()); status != http.StatusOK {
		t.Fatalf("sandbox webhook for a sandbox order answered %d", status)
	}
	if got := h.orderColumn(testUID, "status"); got != "PAID" {
		t.Fatalf("sandbox order status = %q, want PAID (it still settles, it just earns nothing)", got)
	}
	if n := h.earningsOf(referrerID); n != 0 {
		t.Errorf("a sandbox purchase credited %d referral reward(s)", n)
	}

	// The control: the same referral through a live purchase does credit.
	customer, _ := h.signInByPhone("+919000000707", code)
	liveUID := orderUIDOf(t, h.createOrder(customer, "CREDIT_ANALYSIS", ""))
	if status := h.deliverPaid(liveUID, live.signature()); status != http.StatusOK {
		t.Fatalf("live webhook answered %d", status)
	}
	if n := h.earningsOf(referrerID); n != 1 {
		t.Errorf("a live purchase credited %d referral reward(s), want 1", n)
	}
}

func (h *harness) earningsOf(referrerID int64) int {
	h.t.Helper()
	var n int
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT count(*) FROM referral_earnings WHERE referrer_account_id = $1`, referrerID).Scan(&n); err != nil {
		h.t.Fatalf("count referral earnings: %v", err)
	}
	return n
}
