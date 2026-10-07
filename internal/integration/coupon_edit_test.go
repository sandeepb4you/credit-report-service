package integration

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// Coupon editing: a revoked code can be brought back, and every bounded field
// can be widened, narrowed or cleared — all through PATCH /api/coupons/{code}.
//
// The whole point of the endpoint is that revocation is not a one-way door: an
// operator who kills a code that turns out to have been fine has to be able to
// restore it without minting a new one, because the old one is already printed
// on whatever it was printed on.

// createCoupon issues a discount code as the given admin.
func (h *harness) createCoupon(token string, body map[string]any) response {
	h.t.Helper()
	return h.post("/api/coupons", token, body)
}

// couponByCode finds one coupon in the admin's list. Returns nil when absent.
func (h *harness) couponByCode(token, code string) map[string]any {
	h.t.Helper()
	res := h.get("/api/coupons", token)
	if res.Status != http.StatusOK {
		h.t.Fatalf("list coupons: %d %s", res.Status, res.Raw)
	}
	// The list is a bare JSON array, so it does not decode into response.Body.
	var out []map[string]any
	if err := json.Unmarshal([]byte(res.Raw), &out); err != nil {
		h.t.Fatalf("decode coupon list: %v", err)
	}
	for _, c := range out {
		if c["code"] == code {
			return c
		}
	}
	return nil
}

func TestCouponRevokeThenRestore(t *testing.T) {
	h := newHarness(t)
	admin := h.makeAdmin("+919000000801")

	if res := h.createCoupon(admin, map[string]any{
		"code": "COMEBACK", "discountPercent": 20, "maxRedemptions": 5,
	}); res.Status != http.StatusCreated {
		t.Fatalf("create coupon: %d %s", res.Status, res.Raw)
	}

	if res := h.do(http.MethodDelete, "/api/coupons/COMEBACK", admin, nil); res.Status != http.StatusOK {
		t.Fatalf("revoke: %d %s", res.Status, res.Raw)
	}
	if c := h.couponByCode(admin, "COMEBACK"); c["revokedAt"] == nil {
		t.Fatalf("expected the coupon to be revoked, got %v", c)
	}

	// The restore, and a different discount in the same call: an operator who
	// brings a code back usually wants it to mean something different.
	res := h.do(http.MethodPatch, "/api/coupons/COMEBACK", admin, map[string]any{
		"revoked": false, "discountPercent": 35,
	})
	if res.Status != http.StatusOK {
		t.Fatalf("restore: %d %s", res.Status, res.Raw)
	}
	if res.Body["revokedAt"] != nil {
		t.Fatalf("revokedAt should be cleared, got %v", res.Body["revokedAt"])
	}
	if got := res.Body["discountPercent"]; got != float64(35) {
		t.Fatalf("discountPercent = %v, want 35", got)
	}

	// Live again means usable at checkout, not merely un-flagged in the list.
	quote := h.get("/api/coupons/quote?code=COMEBACK&productCode=CREDIT_ANALYSIS", admin)
	if quote.Status != http.StatusOK {
		t.Fatalf("a restored coupon must quote: %d %s", quote.Status, quote.Raw)
	}
	if got := quote.Body["discountPercent"]; got != float64(35) {
		t.Fatalf("quote discountPercent = %v, want the edited 35", got)
	}
}

func TestCouponEditClearsBoundsWithSentinels(t *testing.T) {
	h := newHarness(t)
	admin := h.makeAdmin("+919000000802")

	until := time.Now().UTC().Add(48 * time.Hour).Format(time.RFC3339)
	if res := h.createCoupon(admin, map[string]any{
		"code": "BOUNDED", "discountPercent": 10,
		"productCode": "CREDIT_ANALYSIS", "maxRedemptions": 3,
		"perAccountLimit": 1, "validUntil": until,
	}); res.Status != http.StatusCreated {
		t.Fatalf("create coupon: %d %s", res.Status, res.Raw)
	}

	// "" and 0 are how the wire says "back to unbounded" — an omitted field
	// means "leave it alone", so it cannot carry that.
	res := h.do(http.MethodPatch, "/api/coupons/BOUNDED", admin, map[string]any{
		"productCode": "", "maxRedemptions": 0, "validUntil": "", "perAccountLimit": 4,
	})
	if res.Status != http.StatusOK {
		t.Fatalf("clear bounds: %d %s", res.Status, res.Raw)
	}
	for _, field := range []string{"productCode", "maxRedemptions", "validUntil"} {
		if res.Body[field] != nil {
			t.Fatalf("%s should be cleared, got %v", field, res.Body[field])
		}
	}
	if got := res.Body["perAccountLimit"]; got != float64(4) {
		t.Fatalf("perAccountLimit = %v, want 4", got)
	}
	// Untouched fields survive: a PATCH that mentions neither must not reset them.
	if got := res.Body["discountPercent"]; got != float64(10) {
		t.Fatalf("discountPercent = %v, want the original 10", got)
	}
}

func TestCouponEditRejectsBadValues(t *testing.T) {
	h := newHarness(t)
	admin := h.makeAdmin("+919000000803")

	if res := h.createCoupon(admin, map[string]any{
		"code": "GUARDED", "discountPercent": 10,
	}); res.Status != http.StatusCreated {
		t.Fatalf("create coupon: %d %s", res.Status, res.Raw)
	}

	cases := []struct {
		name string
		body map[string]any
	}{
		{"nothing to change", map[string]any{}},
		{"percent over 100", map[string]any{"discountPercent": 120}},
		{"percent at zero", map[string]any{"discountPercent": 0}},
		{"negative per-account limit", map[string]any{"perAccountLimit": -1}},
		{"unknown product", map[string]any{"productCode": "NO_SUCH_PLAN"}},
		{"unparseable expiry", map[string]any{"validUntil": "next tuesday"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := h.do(http.MethodPatch, "/api/coupons/GUARDED", admin, tc.body)
			if res.Status != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d %s", res.Status, res.Raw)
			}
		})
	}

	// A code nobody issued, and a referral code, both read as missing — the
	// same non-disclosure rule revoking follows.
	if res := h.do(http.MethodPatch, "/api/coupons/NEVERMINTED", admin,
		map[string]any{"revoked": false}); res.Status != http.StatusNotFound {
		t.Fatalf("unknown code: expected 404, got %d %s", res.Status, res.Raw)
	}
	referral := h.referralCodeOf(admin)
	if res := h.do(http.MethodPatch, "/api/coupons/"+referral, admin,
		map[string]any{"discountPercent": 50}); res.Status != http.StatusNotFound {
		t.Fatalf("referral code: expected 404, got %d %s", res.Status, res.Raw)
	}
}
