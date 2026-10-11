package integration

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The console's retargeting lists, /api/admin/audiences.
//
// What is pinned is what decides who gets a campaign message: who is in a list
// and who is kept out of every list, that an export only ever carries accounts
// from the list it was asked for, that a channel leaves out the accounts it
// cannot reach and says how many, and that every file generated is audited.

func (h *harness) audienceRows(adminToken, id string) map[float64]map[string]any {
	h.t.Helper()
	res := h.get("/api/admin/audiences/"+id, adminToken)
	if res.Status != http.StatusOK {
		h.t.Fatalf("list %s: %d %s", id, res.Status, res.Raw)
	}
	out := map[float64]map[string]any{}
	rows, _ := res.Body["rows"].([]any)
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		if aid, _ := row["accountId"].(float64); aid != 0 {
			out[aid] = row
		}
	}
	return out
}

func (h *harness) audienceExport(adminToken, id, channel string, ids ...int64) response {
	h.t.Helper()
	return h.post("/api/admin/audiences/"+id+"/export", adminToken,
		map[string]any{"channel": channel, "accountIds": ids})
}

func fieldsOf(row map[string]any) map[string]any {
	f, _ := row["fields"].(map[string]any)
	return f
}

func TestAudiencesNeedTheirOwnPermission(t *testing.T) {
	h := newHarness(t)
	user, _ := h.signInByPhone("+919000001101", "")
	if res := h.get("/api/admin/audiences", user); res.Status != http.StatusForbidden {
		t.Fatalf("a customer must not read the lists: %d %s", res.Status, res.Raw)
	}
}

func TestAudienceListsWhoIsInAndWhoIsKeptOut(t *testing.T) {
	h := newHarness(t)
	admin := h.makeAdmin("+919000001100")

	// A: signed up, never ordered.
	_, fresh := h.signInByPhone("+919000001102", "")

	// B: left a checkout over half an hour ago.
	leaver, leaverID := h.signInByPhone("+919000001103", "")
	created := h.post("/api/orders/", leaver, map[string]string{"productCode": "CREDIT_ANALYSIS"})
	if created.Status != http.StatusCreated {
		t.Fatalf("create order: %d %s", created.Status, created.Raw)
	}
	// The harness's gateway runs in sandbox mode, and the lists leave sandbox
	// orders out on purpose (the internal app's test payments are not
	// customers) — so these are marked as the live orders they stand for.
	if _, err := h.pool.Exec(h.baseCtx,
		`UPDATE orders SET created_at = now() - interval '2 hours', payment_mode = 'production'
		  WHERE account_id = $1`, leaverID); err != nil {
		t.Fatal(err)
	}

	// C: paid, no PAN yet.
	buyer, buyerID := h.signInByPhone("+919000001104", "")
	h.buyPlan(buyer, "CREDIT_ANALYSIS")
	if _, err := h.pool.Exec(h.baseCtx,
		`UPDATE orders SET payment_mode = 'production' WHERE account_id = $1`, buyerID); err != nil {
		t.Fatal(err)
	}

	// Pending deletion: would be in A, must be in nothing.
	_, leavingID := h.signInByPhone("+919000001105", "")
	grant := h.requestDeletion("+919000001105")
	if res := h.post("/api/auth/account-deletion/confirm", "",
		map[string]string{"deletionToken": grant}); res.Status != http.StatusOK {
		t.Fatalf("confirm deletion: %d %s", res.Status, res.Raw)
	}

	a := h.audienceRows(admin, "A")
	if _, ok := a[float64(fresh)]; !ok {
		t.Errorf("a signup with no order belongs in A: %v", a)
	}
	if _, ok := a[float64(leaverID)]; ok {
		t.Errorf("someone who tried to pay is B's, not A's")
	}
	if _, ok := a[float64(leavingID)]; ok {
		t.Errorf("an account counting down to deletion must be in no list")
	}
	for id, row := range a {
		if row["phone"] == "+919000001100" {
			t.Errorf("staff accounts are not an audience (account %v)", id)
		}
	}

	b := h.audienceRows(admin, "B")
	if row, ok := b[float64(leaverID)]; !ok {
		t.Errorf("an abandoned checkout belongs in B: %v", b)
	} else if fieldsOf(row)["reason"] != "Left checkout" || fieldsOf(row)["amount"] == "" {
		t.Errorf("B row fields = %v", fieldsOf(row))
	}
	if _, ok := b[float64(buyerID)]; ok {
		t.Errorf("a paid order is not an abandoned one")
	}

	c := h.audienceRows(admin, "C")
	if _, ok := c[float64(buyerID)]; !ok {
		t.Fatalf("paid with no PAN belongs in C: %v", c)
	}
	// Verifying the PAN moves them on: C is "paid, nothing delivered".
	if res := h.verifyPAN(buyer, "ABCDE1234F", "JOHN DOE"); res.Status != http.StatusCreated {
		t.Fatalf("verify PAN: %d %s", res.Status, res.Raw)
	}
	if _, ok := h.audienceRows(admin, "C")[float64(buyerID)]; ok {
		t.Errorf("a verified PAN leaves C")
	}

	// The catalogue counts agree with the rows.
	lists := h.get("/api/admin/audiences", admin)
	var catalogue []map[string]any
	if err := json.Unmarshal([]byte(lists.Raw), &catalogue); err != nil || len(catalogue) != 11 {
		t.Fatalf("want 11 lists, got %d (%v): %s", len(catalogue), err, lists.Raw)
	}
	for _, l := range catalogue {
		if l["id"] == "A" && l["count"] != float64(len(h.audienceRows(admin, "A"))) {
			t.Errorf("A's count %v disagrees with its rows", l["count"])
		}
	}
}

// A sandbox purchase is a tester's, not a customer's: it never puts anyone in
// the abandoned-checkout or paid-without-PAN lists.
func TestSandboxOrdersStayOutOfTheOrderLists(t *testing.T) {
	h := newHarness(t)
	admin := h.makeAdmin("+919000001150")
	tester, testerID := h.signInByPhone("+919000001151", "")
	h.buyPlan(tester, "CREDIT_ANALYSIS") // sandbox, as the harness gateway is
	if _, ok := h.audienceRows(admin, "C")[float64(testerID)]; ok {
		t.Errorf("a sandbox purchase must not land in C")
	}
}

func TestAudienceScoreListsAndTheBusinessLoanMatch(t *testing.T) {
	h := newHarness(t)
	admin := h.makeAdmin("+919000001200")

	// F: one-off buyer whose score is 45 days old.
	_, oldID := h.signInByPhone("+919000001201", "")
	rid := h.insertScoredReport(oldID, 655)
	if _, err := h.pool.Exec(h.baseCtx,
		`UPDATE credit_analytics_requests SET created_at = now() - interval '45 days' WHERE id = $1`, rid); err != nil {
		t.Fatal(err)
	}
	// I: a clean file at 760 is an Excellent business-loan match.
	_, cleanID := h.signInByPhone("+919000001202", "")
	h.insertScoredReport(cleanID, 760)

	f := h.audienceRows(admin, "F")
	row, ok := f[float64(oldID)]
	if !ok {
		t.Fatalf("a 45-day-old score with no plan belongs in F: %v", f)
	}
	if got := fieldsOf(row)["band"]; got != "650 to 699" {
		t.Errorf("band = %v, want 650 to 699", got)
	}
	if _, ok := f[float64(cleanID)]; ok {
		t.Errorf("a fresh score is not getting old")
	}

	i := h.audienceRows(admin, "I")
	if row, ok := i[float64(cleanID)]; !ok {
		t.Errorf("a clean 760 file matches a business loan: %v", i)
	} else if fieldsOf(row)["match"] != "Excellent" || fieldsOf(row)["provider"] != "Muthoot FinCorp" {
		t.Errorf("I row fields = %v", fieldsOf(row))
	}
	// 655 on a clean file passes every rule but sits under the Excellent line.
	if row, ok := i[float64(oldID)]; !ok || fieldsOf(row)["match"] != "Good" {
		t.Errorf("a clean 655 file is a Good match, got %v", row)
	}
}

func TestAudienceExportsAreShapedFilteredAndAudited(t *testing.T) {
	h := newHarness(t)
	admin := h.makeAdmin("+919000001300")
	_, phoneOnly := h.signInByPhone("+919000001301", "")
	_, outsider := h.signInByPhone("+919000001302", "")
	// The outsider places an order, so is not in A — and exporting A must not
	// carry them even when their id is sent.
	o, _ := h.signInByPhone("+919000001302", "")
	if res := h.post("/api/orders/", o, map[string]string{"productCode": "CREDIT_ANALYSIS"}); res.Status != http.StatusCreated {
		t.Fatalf("order: %d %s", res.Status, res.Raw)
	}

	wa := h.audienceExport(admin, "A", "whatsapp", phoneOnly, outsider)
	if wa.Status != http.StatusOK {
		t.Fatalf("export: %d %s", wa.Status, wa.Raw)
	}
	csv, _ := wa.Body["csv"].(string)
	if !strings.Contains(csv, "phone,first_name") || !strings.Contains(csv, "+919000001301,there") {
		t.Errorf("WhatsApp file should carry the number and \"there\" for a nameless signup:\n%s", csv)
	}
	if strings.Contains(csv, "+919000001302") {
		t.Errorf("an id outside the list must never reach the file:\n%s", csv)
	}
	if wa.Body["selected"] != float64(1) || wa.Body["included"] != float64(1) {
		t.Errorf("counts = %v", wa.Body)
	}

	// Email: a phone signup has no address, so it is left out and counted.
	em := h.audienceExport(admin, "A", "email", phoneOnly)
	if em.Body["included"] != float64(0) || em.Body["leftOut"] != float64(1) ||
		em.Body["leftOutReason"] != "no email address" {
		t.Errorf("email export = %v", em.Body)
	}

	if bad := h.audienceExport(admin, "A", "fax", phoneOnly); bad.Status != http.StatusBadRequest {
		t.Errorf("an unknown channel is a 400, got %d", bad.Status)
	}

	var audits, rows int
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT COUNT(*), COALESCE(SUM(row_count), 0) FROM admin_audience_exports`).Scan(&audits, &rows); err != nil {
		t.Fatal(err)
	}
	if audits != 2 || rows != 1 {
		t.Errorf("every generated file is audited: %d audits, %d rows", audits, rows)
	}
}
