// The admin user list: the console's customer book, against real handlers and
// a real Postgres.
//
// What is worth pinning here is not that the endpoint answers — it is that the
// four derived figures on each row mean what the console prints them as. Each
// of them restates a rule that already lives somewhere else in the service, so
// each of them can drift:
//
//   - the score is the newest SUCCESSFUL pull carrying one. A failed request is
//     not a report (see succeededPredicate) and a score-less success must not
//     blank out the number the user last saw.
//   - "paid" and "referred who paid" both mean an order that reached PAID,
//     which is the same transition that credits a referral reward. If the list
//     and the earnings ledger ever disagree about who converted, one of them is
//     lying to an operator about money.
package integration

import (
	"net/http"
	"testing"
	"time"
)

// adminAccounts calls the route the console's user list hits.
func (h *harness) adminAccounts(adminToken, query string) response {
	h.t.Helper()
	path := "/api/admin/accounts"
	if query != "" {
		path += "?" + query
	}
	return h.get(path, adminToken)
}

// rowFor finds one account's row in a list response.
func rowFor(res response, accountID int64) map[string]any {
	items, _ := res.Body["items"].([]any)
	for _, raw := range items {
		row, _ := raw.(map[string]any)
		if id, _ := row["accountId"].(float64); int64(id) == accountID {
			return row
		}
	}
	return nil
}

func TestAdminAccountListCarriesScorePaidAndReferrals(t *testing.T) {
	h := newHarness(t)

	adminToken := h.makeAdmin("+919000000601")

	// A referrer who has bought something, with two recruits — one of whom
	// converts. The reward is owed on exactly one of them, so referredCount and
	// referredPaidCount must disagree.
	referrerToken, referrerID := h.signInByPhone("+919000000602", "")
	h.buyPlan(referrerToken, "SCORE_PLUS_MONTHLY")
	code := h.referralCodeOf(referrerToken)

	convertedToken, _ := h.signInByPhone("+919000000603", code)
	h.buyPlan(convertedToken, "SCORE_PLUS_MONTHLY")
	h.signInByPhone("+919000000604", code) // signed up, never paid

	// Two pulls: an older good one, then a score-less success. The list must
	// still report 742 — the newest row has no score to show.
	h.insertScoredReport(referrerID, 742)
	h.insertNoRecordReport(referrerID)

	res := h.adminAccounts(adminToken, "")
	if res.Status != http.StatusOK {
		t.Fatalf("list accounts: %d %s", res.Status, res.Raw)
	}

	row := rowFor(res, referrerID)
	if row == nil {
		t.Fatalf("referrer %d missing from the list: %s", referrerID, res.Raw)
	}
	if score, _ := row["creditScore"].(float64); int(score) != 742 {
		t.Errorf("creditScore = %v, want 742 (the newest SCORED pull): %s", row["creditScore"], res.Raw)
	}
	if paid, _ := row["paid"].(bool); !paid {
		t.Errorf("paid = false for an account with a PAID order: %s", res.Raw)
	}
	if n, _ := row["referredCount"].(float64); int(n) != 2 {
		t.Errorf("referredCount = %v, want 2: %s", row["referredCount"], res.Raw)
	}
	if n, _ := row["referredPaidCount"].(float64); int(n) != 1 {
		t.Errorf("referredPaidCount = %v, want 1: %s", row["referredPaidCount"], res.Raw)
	}

	// The number is here to be dialled, so it must arrive whole. A masked one
	// would make the console's copy button useless.
	if phone, _ := row["phone"].(string); phone != "+919000000602" {
		t.Errorf("phone = %q, want the unmasked number: %s", row["phone"], res.Raw)
	}
}

func TestAdminAccountListFiltersByStatusAndWindow(t *testing.T) {
	h := newHarness(t)

	adminToken := h.makeAdmin("+919000000611")
	_, accountID := h.signInByPhone("+919000000612", "")

	// A phone sign-in creates an ACTIVE account, so the opposite filter must
	// not return it — an empty list here is the filter working.
	active := h.adminAccounts(adminToken, "status=ACTIVE")
	if rowFor(active, accountID) == nil {
		t.Errorf("ACTIVE filter dropped an active account: %s", active.Raw)
	}
	pending := h.adminAccounts(adminToken, "status=PENDING")
	if rowFor(pending, accountID) != nil {
		t.Errorf("PENDING filter returned an ACTIVE account: %s", pending.Raw)
	}

	// An unknown status is a 400 rather than an empty page: a typo that renders
	// as "no users" reads as a data problem, and sends an operator hunting.
	if bad := h.adminAccounts(adminToken, "status=NOPE"); bad.Status != http.StatusBadRequest {
		t.Errorf("unknown status: %d, want 400: %s", bad.Status, bad.Raw)
	}

	// A window that ended before today cannot contain an account created now.
	// Whole UTC days, inclusive on both ends — the same bucketing as the
	// referral report, so the two screens agree about what "last 30 days" is.
	past := time.Now().UTC().AddDate(0, 0, -5).Format("2006-01-02")
	old := h.adminAccounts(adminToken, "from=2020-01-01&to="+past)
	if rowFor(old, accountID) != nil {
		t.Errorf("a window ending 5 days ago returned an account created today: %s", old.Raw)
	}

	today := time.Now().UTC().Format("2006-01-02")
	now := h.adminAccounts(adminToken, "from="+past+"&to="+today)
	if rowFor(now, accountID) == nil {
		t.Errorf("a window ending today dropped an account created today: %s", now.Raw)
	}
}

func TestAdminAccountDetailCarriesKycAndRecentChecks(t *testing.T) {
	h := newHarness(t)

	adminToken := h.makeAdmin("+919000000621")
	token, accountID := h.signInByPhone("+919000000622", "")

	// The offline prefill stub verifies this pair, so the record lands VERIFIED
	// and the detail view has something real to show.
	h.verifyPAN(token, "ABCDE1234F", "JOHN DOE")

	// Six checks; the detail view keeps five. The oldest must fall off rather
	// than the newest, which is the only ordering an operator can use.
	for i := 0; i < 6; i++ {
		h.insertScoredReport(accountID, 700+i)
	}

	res := h.get("/api/admin/accounts/"+itoa(accountID)+"/detail", adminToken)
	if res.Status != http.StatusOK {
		t.Fatalf("account detail: %d %s", res.Status, res.Raw)
	}

	kyc, _ := res.Body["kyc"].(map[string]any)
	if kyc == nil {
		t.Fatalf("no kyc block for a verified account: %s", res.Raw)
	}
	if pan, _ := kyc["pan"].(string); pan != "ABCDE1234F" {
		t.Errorf("kyc.pan = %v, want the full PAN the reviewer needs: %s", kyc["pan"], res.Raw)
	}

	checks, _ := res.Body["checks"].([]any)
	if len(checks) != 5 {
		t.Fatalf("checks = %d, want the 5 most recent: %s", len(checks), res.Raw)
	}
	newest, _ := checks[0].(map[string]any)
	if score, _ := newest["creditScore"].(float64); int(score) != 705 {
		t.Errorf("newest check scored %v, want 705 — the list is not newest-first: %s",
			newest["creditScore"], res.Raw)
	}

	if missing := h.get("/api/admin/accounts/99999999/detail", adminToken); missing.Status != http.StatusNotFound {
		t.Errorf("detail for an unknown account: %d, want 404: %s", missing.Status, missing.Raw)
	}
}

func TestAdminAccountListRefusesANonAdmin(t *testing.T) {
	h := newHarness(t)

	token, _ := h.signInByPhone("+919000000631", "")

	// The gate is the server's, not the console's: the screen is web-only, but
	// that is presentation. Every row here is somebody's phone number.
	if res := h.adminAccounts(token, ""); res.Status != http.StatusForbidden {
		t.Errorf("list accounts as an ordinary user: %d, want 403: %s", res.Status, res.Raw)
	}
	if res := h.get("/api/admin/accounts/1/detail", token); res.Status != http.StatusForbidden {
		t.Errorf("account detail as an ordinary user: %d, want 403: %s", res.Status, res.Raw)
	}
}

// ids reads the account ids off a list response, in the order the server sent
// them — which is the whole point of a sort test.
func ids(res response) []int64 {
	items, _ := res.Body["items"].([]any)
	out := make([]int64, 0, len(items))
	for _, raw := range items {
		row, _ := raw.(map[string]any)
		if id, ok := row["accountId"].(float64); ok {
			out = append(out, int64(id))
		}
	}
	return out
}

func indexOf(list []int64, want int64) int {
	for i, v := range list {
		if v == want {
			return i
		}
	}
	return -1
}

func TestAdminAccountListSortsByColumn(t *testing.T) {
	h := newHarness(t)

	adminToken := h.makeAdmin("+919000000641")
	_, low := h.signInByPhone("+919000000642", "")
	_, high := h.signInByPhone("+919000000643", "")
	_, none := h.signInByPhone("+919000000644", "") // never pulled a report

	h.insertScoredReport(low, 560)
	h.insertScoredReport(high, 820)

	desc := h.adminAccounts(adminToken, "sort=score&desc=true")
	order := ids(desc)
	if indexOf(order, high) > indexOf(order, low) {
		t.Errorf("score desc put %d (820) after %d (560): %v", high, low, order)
	}
	// A missing score is not a low score. Nulls sort last in BOTH directions,
	// or "highest score first" opens on every account that has never checked.
	if indexOf(order, none) < indexOf(order, low) {
		t.Errorf("score desc put the unscored account %d above a scored one: %v", none, order)
	}

	asc := ids(h.adminAccounts(adminToken, "sort=score&desc=false"))
	if indexOf(asc, low) > indexOf(asc, high) {
		t.Errorf("score asc put %d (560) after %d (820): %v", low, high, asc)
	}
	if indexOf(asc, none) < indexOf(asc, high) {
		t.Errorf("score asc put the unscored account %d above a scored one: %v", none, asc)
	}

	// The sort reaches an ORDER BY, so an unknown one is refused rather than
	// quietly ignored: rows in an order nobody asked for read as data.
	if bad := h.adminAccounts(adminToken, "sort=pan%3B%20DROP%20TABLE%20accounts"); bad.Status != http.StatusBadRequest {
		t.Errorf("unknown sort: %d, want 400: %s", bad.Status, bad.Raw)
	}
}

func TestAdminAccountListFiltersEachColumn(t *testing.T) {
	h := newHarness(t)

	adminToken := h.makeAdmin("+919000000651")
	payerToken, payer := h.signInByPhone("+919000000652", "")
	h.buyPlan(payerToken, "SCORE_PLUS_MONTHLY")
	h.insertScoredReport(payer, 780)
	code := h.referralCodeOf(payerToken)
	h.signInByPhone("+919000000653", code)

	_, quiet := h.signInByPhone("+919000000654", "")
	h.insertScoredReport(quiet, 520)

	// Paid is three-state: absent is "either", false is "only those who have not".
	paidOnly := ids(h.adminAccounts(adminToken, "paid=true"))
	if indexOf(paidOnly, payer) < 0 {
		t.Errorf("paid=true dropped the payer %d: %v", payer, paidOnly)
	}
	if indexOf(paidOnly, quiet) >= 0 {
		t.Errorf("paid=true returned a non-payer %d: %v", quiet, paidOnly)
	}
	unpaid := ids(h.adminAccounts(adminToken, "paid=false"))
	if indexOf(unpaid, payer) >= 0 {
		t.Errorf("paid=false returned the payer %d: %v", payer, unpaid)
	}

	// Score band, inclusive on both ends.
	band := ids(h.adminAccounts(adminToken, "minScore=700&maxScore=800"))
	if indexOf(band, payer) < 0 || indexOf(band, quiet) >= 0 {
		t.Errorf("score band 700-800 = %v, want %d and not %d", band, payer, quiet)
	}
	if bad := h.adminAccounts(adminToken, "minScore=800&maxScore=700"); bad.Status != http.StatusBadRequest {
		t.Errorf("inverted score band: %d, want 400: %s", bad.Status, bad.Raw)
	}

	// Referral counts.
	referrers := ids(h.adminAccounts(adminToken, "minReferred=1"))
	if indexOf(referrers, payer) < 0 {
		t.Errorf("minReferred=1 dropped a referrer %d: %v", payer, referrers)
	}
	// Nobody they referred has bought, so the stricter filter must drop them.
	converted := ids(h.adminAccounts(adminToken, "minConverted=1"))
	if indexOf(converted, payer) >= 0 {
		t.Errorf("minConverted=1 returned %d, whose referral never bought: %v", payer, converted)
	}

	// The text box searches name, phone and email together — an operator with
	// a number in their hand should not have to say which column it is.
	byPhone := h.adminAccounts(adminToken, "q=9000000652")
	if rowFor(byPhone, payer) == nil {
		t.Errorf("searching a phone fragment missed %d: %s", payer, byPhone.Raw)
	}
	// And the total must be counted under the same filters as the rows, or a
	// paged table reports a page count it cannot show.
	if total, _ := byPhone.Body["total"].(float64); int(total) != len(ids(byPhone)) {
		t.Errorf("total %v disagrees with the %d rows returned: %s",
			byPhone.Body["total"], len(ids(byPhone)), byPhone.Raw)
	}
}
