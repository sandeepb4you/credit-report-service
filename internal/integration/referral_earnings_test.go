package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Referral earnings: ₹125 credited once per referred account when its first
// order turns PAID, and withdrawn through the manual-payout queue (PENDING on
// request locking the amount, PAID once an admin pays off-app, REJECTED
// freeing it). End to end against real handlers and a real Postgres; the
// purchase walks the REAL webhook fulfilment path via buyPlan, not a shortcut.
const (
	earnReferrer = "+919810000001"
	earnJoiner   = "+919810000002"
	earnAdmin    = "+919810000009"
)

// earnSetup signs a referrer in, reads their code, and signs a joiner up
// through it. Returns both tokens.
func (h *harness) earnSetup() (referrerToken, joinerToken string) {
	h.t.Helper()

	referrerToken, _ = h.signInByPhone(earnReferrer, "")
	code := h.referralCodeOf(referrerToken)
	joinerToken, _ = h.signInByPhone(earnJoiner, code)
	return referrerToken, joinerToken
}

func summaryOf(h *harness, token string) map[string]any {
	h.t.Helper()

	res := h.get("/api/referrals/summary", token)
	if res.Status != http.StatusOK {
		h.t.Fatalf("summary: %d %s", res.Status, res.Raw)
	}
	return res.Body
}

func paisa(body map[string]any, key string) int {
	n, _ := body[key].(float64)
	return int(n)
}

// arrayOf decodes a bare-array response body. List endpoints render [] rather
// than {"items": []} (same as GET /coupons), which the harness's map-shaped
// Body cannot hold — so the test reads Raw instead.
func arrayOf(h *harness, res response) []map[string]any {
	h.t.Helper()

	var out []map[string]any
	if err := json.Unmarshal([]byte(res.Raw), &out); err != nil {
		h.t.Fatalf("decode array response %q: %v", res.Raw, err)
	}
	if out == nil {
		out = []map[string]any{}
	}
	return out
}

// Before any purchase the referral is pending: counted, but worth nothing.
func TestEarnings_BeforePurchase_ReferralIsPendingAndWorthNothing(t *testing.T) {
	h := newHarness(t)
	referrerToken, _ := h.earnSetup()

	s := summaryOf(h, referrerToken)
	if got := paisa(s, "availablePaise"); got != 0 {
		t.Errorf("availablePaise = %d, want 0 before any purchase", got)
	}
	if got := paisa(s, "successfulCount"); got != 0 {
		t.Errorf("successfulCount = %d, want 0", got)
	}
	if got := paisa(s, "pendingCount"); got != 1 {
		t.Errorf("pendingCount = %d, want 1", got)
	}

	mine := h.get("/api/referrals/mine", referrerToken)
	if mine.Status != http.StatusOK {
		t.Fatalf("mine: %d %s", mine.Status, mine.Raw)
	}
	items := arrayOf(h, mine)
	if len(items) != 1 {
		t.Fatalf("mine has %d items, want 1: %s", len(items), mine.Raw)
	}
	item := items[0]
	if item["status"] != "pending" {
		t.Errorf("status = %v, want pending", item["status"])
	}
	// Contacts are masked: the caller recognises the person, never the number.
	phone, _ := item["maskedPhone"].(string)
	if phone == "" || phone == earnJoiner || !strings.Contains(phone, "xxx") {
		t.Errorf("maskedPhone = %q, want a masked form of the number", phone)
	}
}

// The first paid order credits ₹125; the purchase date lands on the row.
func TestEarnings_FirstPaidPurchase_Credits125Once(t *testing.T) {
	h := newHarness(t)
	referrerToken, joinerToken := h.earnSetup()

	h.buyPlan(joinerToken, "CREDIT_ANALYSIS")

	s := summaryOf(h, referrerToken)
	if got := paisa(s, "totalEarnedPaise"); got != 12500 {
		t.Errorf("totalEarnedPaise = %d, want 12500", got)
	}
	if got := paisa(s, "availablePaise"); got != 12500 {
		t.Errorf("availablePaise = %d, want 12500", got)
	}
	if got := paisa(s, "successfulCount"); got != 1 {
		t.Errorf("successfulCount = %d, want 1", got)
	}
	if got := paisa(s, "pendingCount"); got != 0 {
		t.Errorf("pendingCount = %d, want 0", got)
	}

	mine := h.get("/api/referrals/mine", referrerToken)
	if mine.Status != http.StatusOK {
		t.Fatalf("mine: %d %s", mine.Status, mine.Raw)
	}
	items := arrayOf(h, mine)
	if len(items) != 1 {
		t.Fatalf("mine has %d items, want 1: %s", len(items), mine.Raw)
	}
	item := items[0]
	if item["status"] != "paid" {
		t.Errorf("status = %v, want paid", item["status"])
	}
	if got, _ := item["rewardPaise"].(float64); int(got) != 12500 {
		t.Errorf("rewardPaise = %v, want 12500", item["rewardPaise"])
	}
	if date, _ := item["purchaseDate"].(string); len(date) != 10 {
		t.Errorf("purchaseDate = %q, want YYYY-MM-DD", date)
	}
}

// A second purchase by the same referred account credits nothing: the reward
// is once per user, not once per order.
func TestEarnings_SecondPurchase_CreditsNothing(t *testing.T) {
	h := newHarness(t)
	referrerToken, joinerToken := h.earnSetup()

	h.buyPlan(joinerToken, "CREDIT_ANALYSIS")
	h.buyPlan(joinerToken, "SCORE_PLUS_MONTHLY")

	if got := paisa(summaryOf(h, referrerToken), "totalEarnedPaise"); got != 12500 {
		t.Errorf("totalEarnedPaise = %d, want 12500 after two purchases", got)
	}
}

// An account nobody referred earns nothing off purchases.
func TestEarnings_UnreferredBuyer_CreditsNobody(t *testing.T) {
	h := newHarness(t)
	token, _ := h.signInByPhone("+919810000003", "")

	h.buyPlan(token, "CREDIT_ANALYSIS")

	if got := paisa(summaryOf(h, token), "totalEarnedPaise"); got != 0 {
		t.Errorf("totalEarnedPaise = %d, want 0 for an unreferred buyer", got)
	}
}

// saveBank stores a payout destination for tests. IFSCs are real-shaped:
// 4 letters, zero, 6 alphanumerics.
func (h *harness) saveBank(token string) {
	h.t.Helper()

	res := h.do(http.MethodPut, "/api/referrals/bank-account", token, map[string]string{
		"holderName": "Earn Referrer", "accountNumber": "123456789012",
		"confirmNumber": "123456789012", "ifsc": "HDFC0001234",
	})
	if res.Status != http.StatusOK {
		h.t.Fatalf("save bank: %d %s", res.Status, res.Raw)
	}
}

// lowerMin drops the minimum withdrawal to Rs 1 for tests that exercise the
// request/pay/reject mechanics rather than the minimum itself. The minimum
// has its own tests below, against the seeded Rs 500 default.
func (h *harness) lowerMin(adminToken string) {
	h.t.Helper()

	res := h.do(http.MethodPut, "/api/admin/referral-settings", adminToken, map[string]int{
		"minWithdrawalPaise": 100,
	})
	if res.Status != http.StatusOK {
		h.t.Fatalf("lower min withdrawal: %d %s", res.Status, res.Raw)
	}
}

// The full withdraw lifecycle: request locks the amount, a second request
// over the locked balance fails, the admin pays, and paying twice 404s.
func TestEarnings_Withdraw_LocksThenPays(t *testing.T) {
	h := newHarness(t)
	referrerToken, joinerToken := h.earnSetup()
	h.buyPlan(joinerToken, "CREDIT_ANALYSIS")
	h.saveBank(referrerToken)
	adminToken := h.makeAdmin(earnAdmin)
	h.lowerMin(adminToken)

	created := h.post("/api/referrals/withdrawals", referrerToken,
		map[string]int{"amountPaise": 12500})
	if created.Status != http.StatusCreated {
		t.Fatalf("request withdrawal: %d %s", created.Status, created.Raw)
	}
	if created.Body["status"] != "PENDING" {
		t.Errorf("status = %v, want PENDING", created.Body["status"])
	}
	id, _ := created.Body["id"].(float64)

	// The pending request locks its amount: nothing is available now.
	s := summaryOf(h, referrerToken)
	if got := paisa(s, "availablePaise"); got != 0 {
		t.Errorf("availablePaise = %d, want 0 while a request is pending", got)
	}
	if got := paisa(s, "totalPendingPaise"); got != 12500 {
		t.Errorf("totalPendingPaise = %d, want 12500", got)
	}

	// A second request over the locked balance is rejected, not queued.
	again := h.post("/api/referrals/withdrawals", referrerToken,
		map[string]int{"amountPaise": 100})
	if again.Status != http.StatusBadRequest {
		t.Errorf("second request: %d %s, want 400", again.Status, again.Raw)
	}

	queue := h.get("/api/admin/withdrawals?status=PENDING", adminToken)
	if queue.Status != http.StatusOK {
		t.Fatalf("admin queue: %d %s", queue.Status, queue.Raw)
	}

	paid := h.post(fmt.Sprintf("/api/admin/withdrawals/%d/pay", int(id)), adminToken, nil)
	if paid.Status != http.StatusOK {
		t.Fatalf("mark paid: %d %s", paid.Status, paid.Raw)
	}

	s = summaryOf(h, referrerToken)
	if got := paisa(s, "totalPaidPaise"); got != 12500 {
		t.Errorf("totalPaidPaise = %d, want 12500", got)
	}
	if got := paisa(s, "totalPendingPaise"); got != 0 {
		t.Errorf("totalPendingPaise = %d, want 0 after pay", got)
	}

	// Paying twice is a 404, not a second payout.
	repaid := h.post(fmt.Sprintf("/api/admin/withdrawals/%d/pay", int(id)), adminToken, nil)
	if repaid.Status != http.StatusNotFound {
		t.Errorf("repay: %d %s, want 404", repaid.Status, repaid.Raw)
	}
}

// Rejecting frees the locked amount back into the available balance.
func TestEarnings_Withdraw_RejectFreesTheBalance(t *testing.T) {
	h := newHarness(t)
	referrerToken, joinerToken := h.earnSetup()
	h.buyPlan(joinerToken, "CREDIT_ANALYSIS")
	h.saveBank(referrerToken)
	adminToken := h.makeAdmin(earnAdmin)
	h.lowerMin(adminToken)

	created := h.post("/api/referrals/withdrawals", referrerToken,
		map[string]int{"amountPaise": 5000})
	if created.Status != http.StatusCreated {
		t.Fatalf("request withdrawal: %d %s", created.Status, created.Raw)
	}
	id, _ := created.Body["id"].(float64)

	rejected := h.post(fmt.Sprintf("/api/admin/withdrawals/%d/reject", int(id)), adminToken,
		map[string]string{"reason": "IFSC looks wrong, please re-check"})
	if rejected.Status != http.StatusOK {
		t.Fatalf("reject: %d %s", rejected.Status, rejected.Raw)
	}

	if got := paisa(summaryOf(h, referrerToken), "availablePaise"); got != 12500 {
		t.Errorf("availablePaise = %d, want 12500 after reject", got)
	}

	mine := h.get("/api/referrals/withdrawals", referrerToken)
	if mine.Status != http.StatusOK {
		t.Fatalf("own withdrawals: %d %s", mine.Status, mine.Raw)
	}
	items := arrayOf(h, mine)
	if len(items) != 1 {
		t.Fatalf("own withdrawals has %d items, want 1", len(items))
	}
	item := items[0]
	if item["status"] != "REJECTED" {
		t.Errorf("status = %v, want REJECTED", item["status"])
	}
}

// The payout queue is admin-only: an ordinary user reads 403.
func TestEarnings_AdminQueue_RequiresReviewPermission(t *testing.T) {
	h := newHarness(t)
	referrerToken, _ := h.earnSetup()

	if res := h.get("/api/admin/withdrawals", referrerToken); res.Status != http.StatusForbidden {
		t.Errorf("user queue read: %d %s, want 403", res.Status, res.Raw)
	}
	if res := h.post("/api/admin/withdrawals/1/pay", referrerToken, nil); res.Status != http.StatusForbidden {
		t.Errorf("user mark paid: %d %s, want 403", res.Status, res.Raw)
	}
}

// Bank validation: mismatched numbers and bad IFSCs fail on named fields so
// the app can point at the right box.
func TestEarnings_Bank_MismatchedNumbersAndBadIfscAreRejected(t *testing.T) {
	h := newHarness(t)
	token, _ := h.signInByPhone(earnReferrer, "")

	mismatch := h.do(http.MethodPut, "/api/referrals/bank-account", token, map[string]string{
		"holderName": "Earn Referrer", "accountNumber": "123456789012",
		"confirmNumber": "123456789013", "ifsc": "HDFC0001234",
	})
	if mismatch.Status != http.StatusBadRequest {
		t.Errorf("mismatch: %d %s, want 400", mismatch.Status, mismatch.Raw)
	}

	badIfsc := h.do(http.MethodPut, "/api/referrals/bank-account", token, map[string]string{
		"holderName": "Earn Referrer", "accountNumber": "123456789012",
		"confirmNumber": "123456789012", "ifsc": "NOPE",
	})
	if badIfsc.Status != http.StatusBadRequest {
		t.Errorf("bad ifsc: %d %s, want 400", badIfsc.Status, badIfsc.Raw)
	}
}

// No bank account, no withdrawal: the request names the missing sheet.
func TestEarnings_Withdraw_WithoutBankAccountIsRejected(t *testing.T) {
	h := newHarness(t)
	referrerToken, joinerToken := h.earnSetup()
	h.buyPlan(joinerToken, "CREDIT_ANALYSIS")

	res := h.post("/api/referrals/withdrawals", referrerToken,
		map[string]int{"amountPaise": 100})
	if res.Status != http.StatusBadRequest {
		t.Errorf("withdraw without bank: %d %s, want 400", res.Status, res.Raw)
	}
}

// The seeded Rs 500 minimum holds: Rs 125 of earnings cannot be withdrawn,
// and the error names the minimum rather than failing silently.
func TestEarnings_Withdraw_BelowMinimumIsRejected(t *testing.T) {
	h := newHarness(t)
	referrerToken, joinerToken := h.earnSetup()
	h.buyPlan(joinerToken, "CREDIT_ANALYSIS")
	h.saveBank(referrerToken)

	res := h.post("/api/referrals/withdrawals", referrerToken,
		map[string]int{"amountPaise": 12500})
	if res.Status != http.StatusBadRequest {
		t.Fatalf("below-minimum request: %d %s, want 400", res.Status, res.Raw)
	}
}

// Repriced economics apply going forward only: a new conversion credits the
// new reward, the old credit keeps its snapshot, and the summary advertises
// the new values.
func TestEarnings_Settings_RepriceAppliesGoingForward(t *testing.T) {
	h := newHarness(t)
	referrerToken, joinerToken := h.earnSetup()
	h.buyPlan(joinerToken, "CREDIT_ANALYSIS")

	adminToken := h.makeAdmin(earnAdmin)
	repriced := h.do(http.MethodPut, "/api/admin/referral-settings", adminToken, map[string]int{
		"rewardPaise": 20000, "minWithdrawalPaise": 100,
	})
	if repriced.Status != http.StatusOK {
		t.Fatalf("reprice: %d %s", repriced.Status, repriced.Raw)
	}

	// A second joiner converting now earns the new reward.
	secondToken, _ := h.signInByPhone("+919810000007", h.referralCodeOf(referrerToken))
	h.buyPlan(secondToken, "CREDIT_ANALYSIS")

	s := summaryOf(h, referrerToken)
	if got := paisa(s, "totalEarnedPaise"); got != 12500+20000 {
		t.Errorf("totalEarnedPaise = %d, want 32500 (old snapshot + new reward)", got)
	}
	if got := paisa(s, "rewardPerReferralPaise"); got != 20000 {
		t.Errorf("rewardPerReferralPaise = %d, want the repriced 20000", got)
	}
	if got := paisa(s, "minWithdrawalPaise"); got != 100 {
		t.Errorf("minWithdrawalPaise = %d, want 100", got)
	}
}

// The settings endpoints are admin-only.
func TestEarnings_Settings_RequiresManagePermission(t *testing.T) {
	h := newHarness(t)
	token, _ := h.signInByPhone(earnReferrer, "")

	if res := h.get("/api/admin/referral-settings", token); res.Status != http.StatusForbidden {
		t.Errorf("user settings read: %d %s, want 403", res.Status, res.Raw)
	}
	if res := h.do(http.MethodPut, "/api/admin/referral-settings", token,
		map[string]int{"rewardPaise": 1}); res.Status != http.StatusForbidden {
		t.Errorf("user settings write: %d %s, want 403", res.Status, res.Raw)
	}
}

// Zero and negative values are rejected on named fields.
func TestEarnings_Settings_NonPositiveValuesAreRejected(t *testing.T) {
	h := newHarness(t)
	adminToken := h.makeAdmin(earnAdmin)

	res := h.do(http.MethodPut, "/api/admin/referral-settings", adminToken,
		map[string]int{"rewardPaise": 0, "minWithdrawalPaise": -5})
	if res.Status != http.StatusBadRequest {
		t.Errorf("non-positive settings: %d %s, want 400", res.Status, res.Raw)
	}
}
