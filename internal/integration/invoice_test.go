// Tax invoices, end to end: a purchase settles through the real webhook path,
// fulfilment issues the invoice, and the orders API, the download and email
// endpoints and the delivery worker are driven over HTTP against a real
// Postgres. Only the renderer, the bucket and the mailer are faked (see
// invoice_fakes_test.go) — the numbering, the tax columns and the retention
// rules are the database's, and are asserted there.
package integration

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"credit-report-service/internal/models"
)

// fy is the financial year the harness's IST clock is in now: "26-27".
func fy() string {
	now := time.Now().In(istLocation())
	start := now.Year()
	if now.Month() < time.April {
		start--
	}
	return twoDigits(start) + "-" + twoDigits(start+1)
}

func istLocation() *time.Location {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		return time.FixedZone("IST", 5*3600+1800)
	}
	return loc
}

func twoDigits(y int) string {
	s := itoa(int64(y % 100))
	if len(s) == 1 {
		return "0" + s
	}
	return s
}

// paidOrder returns the one order in GET /orders with this uid.
func (h *harness) paidOrder(token, orderUID string) map[string]any {
	h.t.Helper()
	res := h.do(http.MethodGet, "/api/orders/", token, nil)
	if res.Status != http.StatusOK {
		h.t.Fatalf("list orders: %d %s", res.Status, res.Raw)
	}
	var list []map[string]any
	if err := json.Unmarshal([]byte(res.Raw), &list); err != nil {
		h.t.Fatalf("decode orders: %v", err)
	}
	for _, o := range list {
		if o["orderId"] == orderUID {
			return o
		}
	}
	h.t.Fatalf("order %s not in the list: %s", orderUID, res.Raw)
	return nil
}

func (h *harness) invoiceColumn(orderUID, column string) string {
	h.t.Helper()
	var v *string
	// column is a literal from this file only.
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT `+column+`::text FROM invoices WHERE order_uid = $1`, orderUID).Scan(&v); err != nil {
		h.t.Fatalf("read invoices.%s for %s: %v", column, orderUID, err)
	}
	if v == nil {
		return "<null>"
	}
	return *v
}

func (h *harness) giveEmail(accountID int64, email string) {
	h.t.Helper()
	if _, err := h.pool.Exec(h.baseCtx,
		`UPDATE accounts SET primary_email = $2 WHERE id = $1`, accountID, email); err != nil {
		h.t.Fatalf("set email: %v", err)
	}
}

func (h *harness) emailInvoice(token, orderUID, to string) response {
	h.t.Helper()
	body := map[string]string{}
	if to != "" {
		body["email"] = to
	}
	return h.post("/api/orders/"+orderUID+"/invoice/email", token, body)
}

// Clears the resend cooldown, which is the one limit a test would otherwise
// have to sleep through.
func (h *harness) ageInvoiceSends(orderUID string) {
	h.t.Helper()
	if _, err := h.pool.Exec(h.baseCtx,
		`UPDATE invoice_email_sends SET sent_at = sent_at - interval '1 minute'
		  WHERE invoice_id = (SELECT id FROM invoices WHERE order_uid = $1)`, orderUID); err != nil {
		h.t.Fatalf("age sends: %v", err)
	}
}

func TestInvoice_ASandboxPurchaseGetsASpecimenFromItsOwnSeries(t *testing.T) {
	h := newHarness(t)
	token, _ := h.planAccount("+919600000701")

	first := h.buyPlan(token, "CREDIT_ANALYSIS")
	order := h.paidOrder(token, first)
	inv, _ := order["invoice"].(map[string]any)
	if inv == nil {
		t.Fatalf("paid order carries no invoice: %v", order)
	}
	if got, want := inv["number"], "TST/"+fy()+"/000001"; got != want {
		t.Fatalf("invoice number = %v, want %v", got, want)
	}
	if inv["specimen"] != true {
		t.Fatalf("a sandbox order's invoice must be a specimen: %v", inv)
	}
	if inv["paidVia"] != "UPI" {
		t.Fatalf("paidVia = %v, want UPI (from the webhook's payment group)", inv["paidVia"])
	}
	if order["entitlement"] != models.EntitlementUnused {
		t.Fatalf("an unspent one-time check reads %v, want UNUSED", order["entitlement"])
	}
	// No address on a phone signup: nothing is mailed, and nothing is owed.
	if got := h.invoiceColumn(first, "auto_email"); got != models.InvoiceEmailNoAddress {
		t.Fatalf("auto_email = %s, want NO_ADDRESS", got)
	}

	// The tax columns are the design's arithmetic for ₹299.
	for col, want := range map[string]string{
		"total_paise": "29900", "taxable_paise": "25339", "cgst_paise": "2280",
		"sgst_paise": "2281", "igst_paise": "0", "billed_to_name": "JOHN DOE",
	} {
		if got := h.invoiceColumn(first, col); got != want {
			t.Errorf("invoices.%s = %s, want %s", col, got, want)
		}
	}

	second := h.buyPlan(token, "CREDIT_ANALYSIS")
	if got := h.invoiceColumn(second, "invoice_number"); got != "TST/"+fy()+"/000002" {
		t.Fatalf("second invoice = %s, want the next number in the series", got)
	}

	// The same order paid twice (a redelivered webhook) is still one invoice.
	var n int
	if err := h.pool.QueryRow(h.baseCtx, `SELECT count(*) FROM invoices`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("invoices = %d (err %v), want 2", n, err)
	}
}

// A live order gets a real tax invoice, and a sandbox order in the same
// deployment does not take a number from the tax series.
func TestInvoice_LiveAndSandboxOrdersNumberSeparately(t *testing.T) {
	h, live, sandbox := liveDeployment(t)
	token, _ := h.planAccount("+919600000702")

	liveUID := orderUIDOf(t, h.createOrder(token, "CREDIT_ANALYSIS", ""))
	if st := h.deliverPaid(liveUID, live.signature()); st != http.StatusOK {
		t.Fatalf("live webhook: %d", st)
	}
	testUID := orderUIDOf(t, h.createOrder(token, "CREDIT_ANALYSIS", testPaymentsKey))
	if st := h.deliverPaid(testUID, sandbox.signature()); st != http.StatusOK {
		t.Fatalf("sandbox webhook: %d", st)
	}

	if got := h.invoiceColumn(liveUID, "invoice_number"); got != "MSC/"+fy()+"/000001" {
		t.Fatalf("live invoice = %s, want the first MSC number", got)
	}
	if got := h.invoiceColumn(liveUID, "specimen"); got != "false" {
		t.Fatalf("a live order's invoice is a specimen")
	}
	if got := h.invoiceColumn(liveUID, "supplier_gstin"); got != "29ABCDE1234F1ZW" {
		t.Fatalf("supplier_gstin = %s", got)
	}
	if got := h.invoiceColumn(testUID, "invoice_number"); got != "TST/"+fy()+"/000001" {
		t.Fatalf("sandbox invoice in a live deployment = %s, want TST/…/000001", got)
	}
}

func TestInvoice_AnAccountWithAnEmailIsSentItsInvoiceOnPayment(t *testing.T) {
	h := newHarness(t)
	token, accountID := h.planAccount("+919600000703")
	h.giveEmail(accountID, "meera@example.com")

	uid := h.buyPlan(token, "SCORE_PLUS_MONTHLY")
	if got := h.invoiceColumn(uid, "auto_email"); got != models.InvoiceEmailPending {
		t.Fatalf("auto_email = %s, want PENDING", got)
	}

	// A renderer outage is retried, not dropped.
	h.inv.renderer.setDown(true)
	h.inv.deliverer.Pass(h.baseCtx)
	if got := h.invoiceColumn(uid, "auto_email"); got != models.InvoiceEmailPending {
		t.Fatalf("after a failed render auto_email = %s, want still PENDING", got)
	}
	if got := h.invoiceColumn(uid, "auto_email_attempts"); got != "1" {
		t.Fatalf("attempts = %s, want 1", got)
	}
	if len(h.inv.mailer.to()) != 0 {
		t.Fatalf("mailed with no PDF to attach")
	}

	h.inv.renderer.setDown(false)
	if _, err := h.pool.Exec(h.baseCtx,
		`UPDATE invoices SET next_attempt_at = now() WHERE order_uid = $1`, uid); err != nil {
		t.Fatal(err)
	}
	h.inv.deliverer.Pass(h.baseCtx)
	if got := h.inv.mailer.to(); len(got) != 1 || got[0] != "meera@example.com" {
		t.Fatalf("mailed to %v, want the account's address once", got)
	}
	if got := h.invoiceColumn(uid, "auto_email"); got != models.InvoiceEmailSent {
		t.Fatalf("auto_email = %s, want SENT", got)
	}
	// Mailed once, stored once: the download that follows is a presign.
	if got := h.invoiceColumn(uid, "pdf_uri"); !strings.HasPrefix(got, "s3://") {
		t.Fatalf("pdf_uri = %s, want the stored object", got)
	}

	// The plan card: its schedule, from the order's own dates.
	page := h.inv.renderer.last()
	for _, want := range []string{"Valid ", "Next refresh ", "CGST @ 9%", "Rupees Nine Hundred Ninety Nine Only"} {
		if !strings.Contains(page, want) {
			t.Errorf("rendered invoice lacks %q", want)
		}
	}
	h.inv.deliverer.Pass(h.baseCtx)
	if got := h.inv.mailer.to(); len(got) != 1 {
		t.Fatalf("a SENT invoice was mailed again: %v", got)
	}
	if order := h.paidOrder(token, uid); order["entitlement"] != models.EntitlementActive {
		t.Fatalf("a plan with runs left reads %v, want ACTIVE", order["entitlement"])
	}
}

func TestInvoice_DownloadRendersOnTheFirstRequest(t *testing.T) {
	h := newHarness(t)
	token, _ := h.planAccount("+919600000704")
	uid := h.buyPlan(token, "CREDIT_ANALYSIS")

	res := h.get("/api/orders/"+uid+"/invoice", token)
	if res.Status != http.StatusOK {
		t.Fatalf("download: %d %s", res.Status, res.Raw)
	}
	if url, _ := res.Body["url"].(string); !strings.HasPrefix(url, "https://signed.example/") {
		t.Fatalf("url = %v", res.Body["url"])
	}
	if got, want := res.Body["filename"], "myScorr_Invoice_TST-"+fy()+"-000001.pdf"; got != want {
		t.Fatalf("filename = %v, want %v", got, want)
	}
	if !strings.Contains(h.inv.renderer.last(), "SPECIMEN") {
		t.Fatalf("a sandbox invoice rendered without its specimen marking")
	}

	// Someone else's order is not found, not forbidden.
	other, _ := h.signInByPhone("+919600000705", "")
	if res := h.get("/api/orders/"+uid+"/invoice", other); res.Status != http.StatusNotFound {
		t.Fatalf("another account's invoice: %d, want 404", res.Status)
	}
}

func TestInvoice_EmailOnRequest(t *testing.T) {
	h := newHarness(t)
	token, _ := h.planAccount("+919600000706")
	uid := h.buyPlan(token, "CREDIT_ANALYSIS")

	// No address anywhere: the app is told to ask for one.
	if res := h.emailInvoice(token, uid, ""); res.Status != http.StatusConflict {
		t.Fatalf("email with no address: %d %s, want 409", res.Status, res.Raw)
	}
	if res := h.emailInvoice(token, uid, "not-an-email"); res.Status != http.StatusBadRequest {
		t.Fatalf("malformed address: %d, want 400", res.Status)
	}

	res := h.emailInvoice(token, uid, "Karthik@Example.com")
	if res.Status != http.StatusOK {
		t.Fatalf("send once: %d %s", res.Status, res.Raw)
	}
	if res.Body["saved"] != false || res.Body["sentTo"] != "karthik@example.com" ||
		res.Body["from"] != "billing@myscorr.com" {
		t.Fatalf("send-once result: %v", res.Body)
	}

	// Straight away again: the cooldown, with a Retry-After the app can count.
	again := h.emailInvoice(token, uid, "karthik@example.com")
	if again.Status != http.StatusTooManyRequests {
		t.Fatalf("resend inside the cooldown: %d, want 429", again.Status)
	}

	// Three sends a day to addresses the account does not hold, then no more.
	for i := 0; i < 2; i++ {
		h.ageInvoiceSends(uid)
		if r := h.emailInvoice(token, uid, "karthik@example.com"); r.Status != http.StatusOK {
			t.Fatalf("one-time send %d: %d %s", i+2, r.Status, r.Raw)
		}
	}
	h.ageInvoiceSends(uid)
	if r := h.emailInvoice(token, uid, "karthik@example.com"); r.Status != http.StatusTooManyRequests {
		t.Fatalf("fourth one-time send today: %d, want 429", r.Status)
	}
	if got := len(h.inv.mailer.to()); got != 3 {
		t.Fatalf("mails sent = %d, want 3", got)
	}
	// Nothing was saved to the account on any of those.
	var email *string
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT primary_email FROM accounts WHERE id = (SELECT account_id FROM invoices WHERE order_uid = $1)`,
		uid).Scan(&email); err != nil || email != nil {
		t.Fatalf("send-once saved an address (%v, %v)", email, err)
	}
}

// A reset deletes the order and keeps the invoice, so the series has no hole;
// a purge keeps it too, and strips it of the person.
func TestInvoice_SurvivesResetAndIsScrubbedByDeletion(t *testing.T) {
	h := newHarness(t)
	const phone = "+919600000707"
	token, accountID := h.planAccount(phone)
	h.giveEmail(accountID, "meera@example.com")
	uid := h.buyPlan(token, "CREDIT_ANALYSIS")
	if res := h.get("/api/orders/"+uid+"/invoice", token); res.Status != http.StatusOK {
		t.Fatalf("download: %d", res.Status)
	}
	stored := h.invoiceColumn(uid, "pdf_uri")

	res, err := h.accts.PurgeAccount(h.baseCtx, accountID)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	for _, col := range []string{"billed_to_name", "billed_to_email", "billed_to_phone", "pdf_uri"} {
		if got := h.invoiceColumn(uid, col); got != "<null>" {
			t.Errorf("after purge invoices.%s = %s, want NULL", col, got)
		}
	}
	if got := h.invoiceColumn(uid, "invoice_number"); got != "TST/"+fy()+"/000001" {
		t.Fatalf("the purge lost the invoice number: %s", got)
	}
	if got := h.invoiceColumn(uid, "auto_email"); got != models.InvoiceEmailCancelled {
		t.Fatalf("an unsent mail after purge is %s, want CANCELLED", got)
	}
	found := false
	for _, uri := range res.ObjectURIs {
		found = found || uri == stored
	}
	if !found {
		t.Fatalf("the invoice PDF %s is not among the objects the purge deletes: %v", stored, res.ObjectURIs)
	}
	if res.InvoicesScrubbed != 1 {
		t.Fatalf("InvoicesScrubbed = %d, want 1", res.InvoicesScrubbed)
	}
}

func TestInvoice_AResetKeepsTheInvoiceAndItsNumber(t *testing.T) {
	h := newHarness(t)
	const phone = "+919600000708"
	token, accountID := h.planAccount(phone)
	adminToken := h.makeAdmin("+919600000709")
	uid := h.buyPlan(token, "SCORE_PLUS_QUARTERLY")

	if res := h.resetAccount(adminToken, accountID, phone); res.Status != http.StatusOK {
		t.Fatalf("reset: %d %s", res.Status, res.Raw)
	}
	if got := h.invoiceColumn(uid, "order_id"); got != "<null>" {
		t.Fatalf("after reset invoices.order_id = %s, want NULL (the order is gone)", got)
	}
	if got := h.invoiceColumn(uid, "invoice_number"); got != "TST/"+fy()+"/000001" {
		t.Fatalf("the reset lost the invoice: %s", got)
	}

	// The account buys again: the next number, not a reused one.
	token2, _ := h.signInByPhone(phone, "")
	if res := h.verifyPAN(token2, stubPAN, stubPANName); res.Status != http.StatusCreated {
		t.Fatalf("verify PAN after reset: %d", res.Status)
	}
	next := h.buyPlan(token2, "CREDIT_ANALYSIS")
	if got := h.invoiceColumn(next, "invoice_number"); got != "TST/"+fy()+"/000002" {
		t.Fatalf("invoice after reset = %s, want 000002", got)
	}
}
