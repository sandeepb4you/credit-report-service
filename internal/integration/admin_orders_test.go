package integration

import (
	"net/http"
	"testing"
)

// The console's purchases list, GET /api/admin/orders.
//
// What is pinned is what an operator acts on: which orders a filter returns,
// that the money totals count PAID orders only and cover the whole filtered set
// rather than the page, and that the entitlement column agrees with the rule
// the customer's own orders API uses — a support call is "I paid and got
// nothing", and the two screens must give the same answer to it.

func (h *harness) adminOrders(adminToken, query string) response {
	h.t.Helper()
	path := "/api/admin/orders"
	if query != "" {
		path += "?" + query
	}
	return h.get(path, adminToken)
}

// orderRows returns the page's rows keyed by order id.
func orderRows(res response) map[string]map[string]any {
	out := map[string]map[string]any{}
	items, _ := res.Body["items"].([]any)
	for _, raw := range items {
		row, _ := raw.(map[string]any)
		if id, _ := row["orderId"].(string); id != "" {
			out[id] = row
		}
	}
	return out
}

func orderSummaryOf(res response) map[string]any {
	s, _ := res.Body["summary"].(map[string]any)
	return s
}

func TestAdminOrdersListsFiltersAndTotals(t *testing.T) {
	h := newHarness(t)
	admin := h.makeAdmin("+919000000901")

	buyer, _ := h.signInByPhone("+919000000902", "")
	paidUID := h.buyPlan(buyer, "CREDIT_ANALYSIS")

	browser, _ := h.signInByPhone("+919000000903", "")
	pending := h.post("/api/orders/", browser, map[string]string{"productCode": "CREDIT_ANALYSIS"})
	if pending.Status != http.StatusCreated {
		t.Fatalf("create unpaid order: %d %s", pending.Status, pending.Raw)
	}
	pendingUID, _ := pending.Body["orderId"].(string)

	planUID := h.buyPlan(buyer, "SCORE_PLUS_MONTHLY")

	// ---- everything ----
	all := h.adminOrders(admin, "")
	if all.Status != http.StatusOK {
		t.Fatalf("list: %d %s", all.Status, all.Raw)
	}
	rows := orderRows(all)
	if len(rows) != 3 {
		t.Fatalf("want all 3 orders, got %d: %s", len(rows), all.Raw)
	}

	paid := rows[paidUID]
	if paid["status"] != "PAID" || paid["phone"] != "+919000000902" {
		t.Errorf("paid row = %v", paid)
	}
	// Unmasked, as on the user list: the number is here to be dialled.
	if paid["entitlement"] != "UNUSED" {
		t.Errorf("a paid, unrun check is UNUSED, got %v", paid["entitlement"])
	}
	if rows[planUID]["entitlement"] != "ACTIVE" {
		t.Errorf("a plan with runs left is ACTIVE, got %v", rows[planUID]["entitlement"])
	}
	if rows[pendingUID]["entitlement"] != nil {
		t.Errorf("an unpaid order entitles nobody to anything, got %v", rows[pendingUID]["entitlement"])
	}

	// The totals count money from PAID orders only, across the whole set.
	sum := orderSummaryOf(all)
	if sum["orders"] != float64(3) || sum["paid"] != float64(2) || sum["buyers"] != float64(1) {
		t.Errorf("summary counts = %v", sum)
	}
	wantCollected := paid["amount"].(float64) + rows[planUID]["amount"].(float64)
	if sum["collected"] != wantCollected {
		t.Errorf("collected = %v, want the two paid amounts (%v), not the unpaid one too",
			sum["collected"], wantCollected)
	}

	// ---- the summary is the set's, not the page's ----
	onePage := h.adminOrders(admin, "limit=1")
	if got := len(orderRows(onePage)); got != 1 {
		t.Fatalf("limit=1 returned %d rows", got)
	}
	if orderSummaryOf(onePage)["collected"] != wantCollected || onePage.Body["total"] != float64(3) {
		t.Errorf("a one-row page must still total the whole set: %v", onePage.Raw)
	}

	// ---- status groups ----
	if got := orderRows(h.adminOrders(admin, "status=paid")); len(got) != 2 || got[pendingUID] != nil {
		t.Errorf("paid tab = %v", keysOf(got))
	}
	if got := orderRows(h.adminOrders(admin, "status=pending")); len(got) != 1 || got[pendingUID] == nil {
		t.Errorf("pending tab = %v", keysOf(got))
	}
	if got := orderRows(h.adminOrders(admin, "status=failed")); len(got) != 0 {
		t.Errorf("failed tab should be empty, got %v", keysOf(got))
	}

	// ---- live vs test: the harness gateway is the sandbox stub ----
	if got := orderRows(h.adminOrders(admin, "mode=live")); len(got) != 0 {
		t.Errorf("no live orders were placed, got %v", keysOf(got))
	}
	if got := orderRows(h.adminOrders(admin, "mode=test")); len(got) != 3 {
		t.Errorf("every harness order is a test order, got %d", len(got))
	}

	// ---- product and search ----
	if got := orderRows(h.adminOrders(admin, "product=SCORE_PLUS_MONTHLY")); len(got) != 1 || got[planUID] == nil {
		t.Errorf("product filter = %v", keysOf(got))
	}
	// One box for everything the customer might read out: their number...
	if got := orderRows(h.adminOrders(admin, "q=9000000903")); len(got) != 1 || got[pendingUID] == nil {
		t.Errorf("search by phone = %v", keysOf(got))
	}
	// ...or the order id off their receipt.
	if got := orderRows(h.adminOrders(admin, "q="+paidUID)); len(got) != 1 || got[paidUID] == nil {
		t.Errorf("search by order id = %v", keysOf(got))
	}

	// ---- sort ----
	byAmount := h.adminOrders(admin, "sort=amount&desc=true")
	items, _ := byAmount.Body["items"].([]any)
	if first, _ := items[0].(map[string]any); first["orderId"] != planUID {
		t.Errorf("the plan is the dearest order and should lead an amount sort: %s", byAmount.Raw)
	}
}

func TestAdminOrdersRefusesBadFiltersAndNonAdmins(t *testing.T) {
	h := newHarness(t)
	admin := h.makeAdmin("+919000000911")

	for _, q := range []string{"status=refunded", "mode=staging", "sort=price", "from=yesterday"} {
		if res := h.adminOrders(admin, q); res.Status != http.StatusBadRequest {
			t.Errorf("%s: want 400 rather than an empty list, got %d %s", q, res.Status, res.Raw)
		}
	}

	user, _ := h.signInByPhone("+919000000912", "")
	if res := h.adminOrders(user, ""); res.Status != http.StatusForbidden {
		t.Errorf("a customer must not read everyone's purchases: %d", res.Status)
	}
}

func keysOf(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
