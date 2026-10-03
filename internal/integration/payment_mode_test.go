// Live payments for the store app, sandbox payments for internal builds, in one
// deployment — against real handlers and a real Postgres.
//
// The stub gateway the rest of the suite uses cannot test this: it verifies
// every webhook signature, so it cannot show a sandbox-signed delivery being
// REFUSED for a live order, which is the property that matters most here.
// Sandbox payments cost nothing to make; if one could settle a live order, a
// test build would be a way to get paid-for bureau pulls free. These tests use
// two fake gateways that each sign with their own secret, as Razorpay's live
// and test webhooks do.
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

// modeGateway is a fake Razorpay environment: it creates orders, answers
// GetOrder from a status the test sets, and accepts only webhooks signed with
// its own secret. Its gateway order ids are "order_<mode>_<uid>", so a lookup
// can be traced back to both the environment and our order.
type modeGateway struct {
	mode   string
	secret string

	mu       sync.Mutex
	statuses map[string]string // order uid -> status GetOrder reports
	lookups  []string          // order uids GetOrder was asked about (via their gateway id)
}

func newModeGateway(mode string) *modeGateway {
	return &modeGateway{mode: mode, secret: "secret-" + mode, statuses: map[string]string{}}
}

func (g *modeGateway) Mode() string  { return g.mode }
func (g *modeGateway) KeyID() string { return "rzp_" + g.mode + "_key" }

func (g *modeGateway) gatewayID(orderUID string) string { return "order_" + g.mode + "_" + orderUID }

// uidOf recovers our order uid from one of this gateway's order ids, "" when
// the id belongs to the other environment.
func (g *modeGateway) uidOf(gatewayOrderID string) string {
	return strings.TrimPrefix(gatewayOrderID, "order_"+g.mode+"_")
}

func (g *modeGateway) CreateOrder(_ context.Context, p payments.CreateOrderParams) (*payments.OrderResult, error) {
	return &payments.OrderResult{GatewayOrderID: g.gatewayID(p.OrderID), Status: payments.StatusActive}, nil
}

func (g *modeGateway) GetOrder(_ context.Context, gatewayOrderID string) (*payments.OrderResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	uid := g.uidOf(gatewayOrderID)
	g.lookups = append(g.lookups, uid)
	status := g.statuses[uid]
	if status == "" {
		status = payments.StatusActive
	}
	return &payments.OrderResult{GatewayOrderID: gatewayOrderID, Status: status}, nil
}

func (g *modeGateway) GetPayment(_ context.Context, gatewayOrderID string) (*payments.PaymentDetails, error) {
	return &payments.PaymentDetails{
		PaymentID: "pay_" + g.uidOf(gatewayOrderID), Group: "upi", BankReference: "UTR" + g.mode,
	}, nil
}

func (g *modeGateway) VerifyWebhookSignature(_ []byte, signature string) bool {
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

// deliverPaid posts an order.paid webhook for orderUID with the given
// signature and returns the HTTP status.
func (h *harness) deliverPaid(orderUID, signature string) int {
	h.t.Helper()
	return h.deliverWebhook(orderPaidWebhook(orderUID, "pay_1"), signature)
}

func (h *harness) deliverWebhook(body, signature string) int {
	h.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/payments/razorpay/webhook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Razorpay-Signature", signature)
	req.Header.Set("X-Razorpay-Event-Id", fmt.Sprintf("evt_%d", time.Now().UnixNano()))
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
		t.Errorf("store order mode = %v, want production", got)
	}
	// The key is what selects Razorpay's environment in the checkout, so it
	// must be the one belonging to the gateway that created the order.
	if got := store.Body["keyId"]; got != "rzp_production_key" {
		t.Errorf("store order keyId = %v, want the live gateway's key", got)
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
	if got := internal.Body["keyId"]; got != "rzp_sandbox_key" {
		t.Errorf("internal order keyId = %v, want the test gateway's key", got)
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

// A failed attempt does not end a Razorpay order: the checkout stays open for
// another try on the same order. The delivery names only Razorpay's order id,
// and must still be matched to ours — the deletion scrub finds webhook rows by
// order_uid, and this one carries the payer's email, phone and UPI id.
func TestAFailedAttemptLeavesTheOrderPayableAndIsLinkedToIt(t *testing.T) {
	h, live, _ := liveDeployment(t)
	token, _ := h.signInByPhone("+919000000708", "")
	uid := orderUIDOf(t, h.createOrder(token, "CREDIT_ANALYSIS", ""))

	failed := fmt.Sprintf(`{"entity":"event","event":"payment.failed","created_at":%d,
		"payload":{"payment":{"entity":{"id":"pay_F","order_id":%q,"status":"failed","method":"upi",
		"email":"payer@example.com","contact":"+919000000708","vpa":"payer@okaxis",
		"error_description":"Payment was declined by the bank"}}}}`,
		time.Now().Unix(), live.gatewayID(uid))
	if status := h.deliverWebhook(failed, live.signature()); status != http.StatusOK {
		t.Fatalf("payment.failed webhook answered %d", status)
	}
	if got := h.orderColumn(uid, "status"); got != "ACTIVE" {
		t.Errorf("order after a failed attempt = %q, want ACTIVE (still payable)", got)
	}
	var linked int
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT count(*) FROM payment_webhook_events WHERE order_uid = $1 AND event_type = 'payment.failed'`,
		uid).Scan(&linked); err != nil || linked != 1 {
		t.Errorf("payment.failed rows linked to the order = %d (err %v), want 1", linked, err)
	}

	// The same order then pays.
	if status := h.deliverPaid(uid, live.signature()); status != http.StatusOK {
		t.Fatalf("order.paid webhook answered %d", status)
	}
	if got := h.orderColumn(uid, "status"); got != "PAID" {
		t.Errorf("order after paying = %q, want PAID", got)
	}
}
