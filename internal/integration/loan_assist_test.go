package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"credit-report-service/internal/models"
)

// Loan Assist (design/myscorr-loan-assist-customer-screens.html), walked
// through the real handlers: an application for a loan type and amount, its
// documents, payment, the partner's review (accept, ask again, verify,
// assign), the customer's resubmission, and reusing verified documents in a
// second application.

var (
	jpegBytes = append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F', 0}, bytes.Repeat([]byte{1}, 64)...)
	pdfBytes  = []byte("%PDF-1.4\n1 0 obj << >> endobj\ntrailer << >>\n%%EOF\n")
)

func (h *harness) multipartTo(path, token, slot, filename string, data []byte) response {
	h.t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("slot", slot)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		h.t.Fatalf("multipart: %v", err)
	}
	_, _ = part.Write(data)
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	return h.send(req)
}

func (h *harness) uploadLoanDoc(token, slot, filename string, data []byte) response {
	h.t.Helper()
	return h.multipartTo("/api/loan-assist/documents", token, slot, filename, data)
}

// startApplication opens a Business loan application for amount.
func (h *harness) startApplication(token string, amount int64, rented bool) {
	h.t.Helper()
	res := h.put("/api/loan-assist/draft", token, map[string]any{
		"loanType": "BUSINESS", "amountNeeded": amount, "rented": rented,
	})
	if res.Status != http.StatusOK {
		h.t.Fatalf("start application: %d %s", res.Status, res.Raw)
	}
}

// uploadAll fills every slot of the open application the loan type needs,
// except those in skip.
func (h *harness) uploadAll(token string, rented bool, skip ...string) {
	h.t.Helper()
	skipped := map[string]bool{}
	for _, s := range skip {
		skipped[s] = true
	}
	for _, slot := range models.FindLoanType(models.LoanTypeBusiness).RequiredSlots(rented) {
		if skipped[slot] {
			continue
		}
		name, data := strings.ToLower(slot)+".jpg", jpegBytes
		if slot == models.LoanSlotPAN {
			name, data = "pan-card.pdf", pdfBytes
		}
		if res := h.uploadLoanDoc(token, slot, name, data); res.Status != http.StatusCreated {
			h.t.Fatalf("upload %s: %d %s", slot, res.Status, res.Raw)
		}
	}
}

// payApplication submits the open application and settles its order, then
// returns its request code.
func (h *harness) payApplication(token string) string {
	h.t.Helper()
	sub := h.post("/api/loan-assist/submit", token, map[string]any{"consent": true})
	if sub.Status != http.StatusCreated {
		h.t.Fatalf("submit: %d %s", sub.Status, sub.Raw)
	}
	if st := h.deliverPaid(orderUIDOf(h.t, sub), "stub"); st != http.StatusOK {
		h.t.Fatalf("paid webhook: %d", st)
	}
	st := h.get("/api/loan-assist", token)
	apps := listOf(st.Body["applications"])
	if len(apps) == 0 {
		h.t.Fatalf("no application after payment: %s", st.Raw)
	}
	code, _ := apps[0]["requestCode"].(string)
	return code
}

// makePartner promotes an account to partner and returns a fresh token.
func (h *harness) makePartner(phone string) (string, int64) {
	h.t.Helper()
	_, id := h.signInByPhone(phone, "")
	if _, err := h.accts.SetRole(h.baseCtx, id, models.RolePartner); err != nil {
		h.t.Fatalf("promote %d to partner: %v", id, err)
	}
	token, _ := h.signInByPhone(phone, "")
	return token, id
}

func (h *harness) reviewIDOf(token, code string) int64 {
	h.t.Helper()
	for _, scope := range []string{"mine", "unassigned", "all"} {
		res := h.get("/api/loan-assist/review?scope="+scope, token)
		for _, a := range listOf(res.Body["applications"]) {
			if a["requestCode"] == code {
				return int64(intOf(a["id"]))
			}
		}
	}
	h.t.Fatalf("application %s not in the review list", code)
	return 0
}

func reviewDocs(res response) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, d := range listOf(res.Body["documents"]) {
		out[d["slot"].(string)] = d
	}
	return out
}

func TestLoanAssist_ApplyPayReviewRetakeVerify(t *testing.T) {
	h := newHarness(t)
	token, customerID := h.signInByPhone("+919600000801", "")

	// 02: a price, the loan types, nothing yet.
	st := h.get("/api/loan-assist", token)
	if st.Status != http.StatusOK {
		t.Fatalf("state: %d %s", st.Status, st.Raw)
	}
	if p, _ := st.Body["product"].(map[string]any); p["amount"] != 2999.0 || p["available"] != true {
		t.Fatalf("product = %v", p)
	}
	if n := len(listOf(st.Body["loanTypes"])); n != 5 || len(listOf(st.Body["applications"])) != 0 {
		t.Fatalf("loan types = %d, applications = %v", n, st.Body["applications"])
	}

	// Never on the Plans catalog, not buyable through POST /orders.
	if res := h.get("/api/products", token); strings.Contains(res.Raw, models.ProductLoanAssist) {
		t.Fatalf("GET /products lists Loan Assist")
	}
	if res := h.post("/api/orders/", token, map[string]string{"productCode": models.ProductLoanAssist}); res.Status != http.StatusConflict {
		t.Fatalf("POST /orders for Loan Assist: %d, want 409", res.Status)
	}

	// 03: only Business loan is active.
	if res := h.put("/api/loan-assist/draft", token, map[string]any{"loanType": "HOME", "amountNeeded": 500000}); res.Status != http.StatusBadRequest {
		t.Fatalf("an inactive loan type: %d, want 400", res.Status)
	}
	h.startApplication(token, 10_00_000, true)

	// Bytes decide the type; and an early submit names what is missing.
	if res := h.uploadLoanDoc(token, models.LoanSlotPAN, "pan.pdf", []byte("MZ not a pdf")); res.Status != http.StatusBadRequest {
		t.Fatalf("a non-document upload: %d, want 400", res.Status)
	}
	if res := h.post("/api/loan-assist/submit", token, map[string]any{"consent": true}); res.Status != http.StatusBadRequest {
		t.Fatalf("submit with no documents: %d, want 400", res.Status)
	}
	h.uploadAll(token, true)

	// Unticking rented takes the rent agreement out again.
	res := h.put("/api/loan-assist/draft", token, map[string]any{"rented": false})
	for _, d := range listOf(res.Body["documents"]) {
		if d["slot"] == models.LoanSlotRentAgreement {
			t.Fatalf("rent agreement survived unticking")
		}
	}
	if res := h.post("/api/loan-assist/submit", token, map[string]any{"consent": false}); res.Status != http.StatusBadRequest {
		t.Fatalf("submit without consent: %d, want 400", res.Status)
	}
	code := h.payApplication(token)
	if !strings.HasPrefix(code, "LA-") {
		t.Fatalf("request code = %q", code)
	}

	// 10: listed with the loan, the amount and what was paid; team mailed once.
	st = h.get("/api/loan-assist", token)
	app := listOf(st.Body["applications"])[0]
	if app["amountNeeded"] != 1000000.0 || app["amountPaid"] != 2999.0 || app["documentCount"] != 5.0 ||
		app["status"] != models.LoanAssistSubmitted || app["inReviewCount"] != 5.0 {
		t.Fatalf("summary = %v", app)
	}
	if mails := h.inv.loanMail.all(); len(mails) != 1 || len(mails[0].Documents) != 6 {
		t.Fatalf("team mails = %+v", mails)
	}

	// Customers cannot reach the review side.
	if res := h.get("/api/loan-assist/review", token); res.Status != http.StatusForbidden {
		t.Fatalf("customer reading the review list: %d, want 403", res.Status)
	}

	// A partner sees it unassigned, with the contact masked, and cannot open
	// its documents until it is theirs.
	partner, partnerID := h.makePartner("+919600000802")
	if res := h.put("/api/loan-assist/partner/profile", partner, map[string]any{
		"name": "Vikram Rao", "team": "Offline Executive", "city": "Hyderabad", "phone": "+918000000000",
	}); res.Status != http.StatusOK {
		t.Fatalf("partner profile: %d %s", res.Status, res.Raw)
	}
	id := h.reviewIDOf(partner, code)
	detail := h.get(fmt.Sprintf("/api/loan-assist/review/%d", id), partner)
	if detail.Body["canOpen"] != false || strings.Contains(detail.Raw, "9600000801") {
		t.Fatalf("an unassigned application must be masked: %s", detail.Raw)
	}
	link := fmt.Sprintf("/api/loan-assist/review/%d/documents/%s/link", id, models.LoanSlotPAN)
	if res := h.get(link, partner); res.Status != http.StatusForbidden {
		t.Fatalf("opening a document before taking it: %d, want 403", res.Status)
	}
	if res := h.post(fmt.Sprintf("/api/loan-assist/review/%d/assign", id), partner, map[string]any{"partnerId": partnerID}); res.Status != http.StatusOK {
		t.Fatalf("take the application: %d %s", res.Status, res.Raw)
	}
	if res := h.get(link, partner); res.Status != http.StatusOK || res.Body["url"] == "" {
		t.Fatalf("document link once assigned: %d %s", res.Status, res.Raw)
	}

	// Another partner no longer sees it at all.
	other, _ := h.makePartner("+919600000803")
	if res := h.get(fmt.Sprintf("/api/loan-assist/review/%d", id), other); res.Status != http.StatusNotFound {
		t.Fatalf("another partner's application: %d, want 404", res.Status)
	}

	// Review: accept four, ask for the bill again with a note.
	review := func(slot, decision, note string) response {
		return h.post(fmt.Sprintf("/api/loan-assist/review/%d/documents/%s", id, slot), partner,
			map[string]any{"decision": decision, "note": note})
	}
	if res := review(models.LoanSlotElectricityBill, "RETAKE", ""); res.Status != http.StatusBadRequest {
		t.Fatalf("a retake without a note: %d, want 400", res.Status)
	}
	for _, slot := range []string{models.LoanSlotAadhaarFront, models.LoanSlotAadhaarBack, models.LoanSlotPAN,
		models.LoanSlotUdyam, models.LoanSlotNameBoard} {
		if res := review(slot, "ACCEPT", ""); res.Status != http.StatusOK {
			t.Fatalf("accept %s: %d %s", slot, res.Status, res.Raw)
		}
	}
	const note = "This bill is older than 3 months. Upload your latest bill."
	if res := review(models.LoanSlotElectricityBill, "RETAKE", note); res.Status != http.StatusOK {
		t.Fatalf("retake: %d %s", res.Status, res.Raw)
	}
	if res := h.post(fmt.Sprintf("/api/loan-assist/review/%d/status", id), partner,
		map[string]any{"status": "DOCS_VERIFIED"}); res.Status != http.StatusConflict {
		t.Fatalf("verifying with a document outstanding: %d, want 409", res.Status)
	}
	if pushes := h.inv.loanPush.all(); len(pushes) != 1 || pushes[0].Data["route"] != "AssistApplication:"+code {
		t.Fatalf("customer pushes = %+v", pushes)
	}

	// 11/12: the customer sees one document to fix, the note, and who asked.
	st = h.get("/api/loan-assist", token)
	if st.Body["attentionCount"] != 1.0 {
		t.Fatalf("attention count = %v, want 1", st.Body["attentionCount"])
	}
	page := h.get("/api/loan-assist/applications/"+code, token)
	bill := reviewDocs(page)[models.LoanSlotElectricityBill]
	reviewer, _ := bill["reviewer"].(map[string]any)
	if bill["reviewState"] != models.LoanDocRetake || bill["reviewNote"] != note || reviewer["name"] != "Vikram Rao" {
		t.Fatalf("bill on the page = %v", bill)
	}
	if res := h.get("/api/loan-assist/applications/LA-99999", token); res.Status != http.StatusNotFound {
		t.Fatalf("someone else's code: %d, want 404", res.Status)
	}

	// 13/14: only the asked-for document can be sent again; it goes back into review.
	resubmit := "/api/loan-assist/applications/" + code + "/documents"
	if res := h.multipartTo(resubmit, token, models.LoanSlotPAN, "pan.pdf", pdfBytes); res.Status != http.StatusConflict {
		t.Fatalf("resubmitting an accepted document: %d, want 409", res.Status)
	}
	res = h.multipartTo(resubmit, token, models.LoanSlotElectricityBill, "bill.jpg", jpegBytes)
	if res.Status != http.StatusOK {
		t.Fatalf("resubmit: %d %s", res.Status, res.Raw)
	}
	bill = reviewDocs(res)[models.LoanSlotElectricityBill]
	if bill["reviewState"] != models.LoanDocPending || bill["resubmittedAt"] == nil {
		t.Fatalf("bill after resubmission = %v", bill)
	}

	// Accept it, verify; the customer now sees their expert.
	if res := review(models.LoanSlotElectricityBill, "ACCEPT", ""); res.Status != http.StatusOK {
		t.Fatalf("accept the new bill: %d", res.Status)
	}
	if res := h.post(fmt.Sprintf("/api/loan-assist/review/%d/status", id), partner,
		map[string]any{"status": "DOCS_VERIFIED"}); res.Status != http.StatusOK {
		t.Fatalf("verify: %d %s", res.Status, res.Raw)
	}
	page = h.get("/api/loan-assist/applications/"+code, token)
	expert, _ := page.Body["expert"].(map[string]any)
	if page.Body["status"] != models.LoanAssistDocsVerified || page.Body["docsVerifiedAt"] == nil ||
		expert["name"] != "Vikram Rao" || expert["team"] != "Offline Executive" || expert["phone"] != "+918000000000" {
		t.Fatalf("page after verification = %s", page.Raw)
	}

	// An admin acts on any application without taking it, and can assign it.
	admin := h.makeAdmin("+919600000804")
	if res := h.post(fmt.Sprintf("/api/loan-assist/review/%d/status", id), admin,
		map[string]any{"status": "EXPERT_CONTACTED"}); res.Status != http.StatusOK {
		t.Fatalf("admin moving it on: %d %s", res.Status, res.Raw)
	}
	if res := h.get(link, admin); res.Status != http.StatusOK {
		t.Fatalf("admin opening a document: %d", res.Status)
	}
	if res := h.post(fmt.Sprintf("/api/loan-assist/review/%d/assign", id), admin,
		map[string]any{"partnerId": customerID}); res.Status != http.StatusBadRequest {
		t.Fatalf("assigning to a customer: %d, want 400", res.Status)
	}
	if res := h.put(fmt.Sprintf("/api/loan-assist/review/%d/notes", id), admin,
		map[string]any{"notes": "Wants ₹10L for stock"}); res.Status != http.StatusNoContent {
		t.Fatalf("notes: %d %s", res.Status, res.Raw)
	}
	var partners []map[string]any
	_ = json.Unmarshal([]byte(h.get("/api/loan-assist/partners", admin).Raw), &partners)
	if len(partners) != 3 { // two partners and the admin
		t.Fatalf("partners = %v", partners)
	}
}

// A second application reuses documents a partner accepted, but not a bill
// older than three months, and replacing a reused copy leaves the original.
func TestLoanAssist_SecondApplicationReusesVerifiedDocuments(t *testing.T) {
	h := newHarness(t)
	token, accountID := h.signInByPhone("+919600000811", "")
	h.startApplication(token, 10_00_000, false)
	h.uploadAll(token, false)
	code := h.payApplication(token)

	admin := h.makeAdmin("+919600000812")
	id := h.reviewIDOf(admin, code)
	for _, slot := range models.FindLoanType(models.LoanTypeBusiness).RequiredSlots(false) {
		if res := h.post(fmt.Sprintf("/api/loan-assist/review/%d/documents/%s", id, slot), admin,
			map[string]any{"decision": "ACCEPT"}); res.Status != http.StatusOK {
			t.Fatalf("accept %s: %d", slot, res.Status)
		}
	}
	// The bill was uploaded four months ago.
	if _, err := h.pool.Exec(h.baseCtx,
		`UPDATE documents SET created_at = $2 WHERE account_id = $1 AND doc_type = 'LOAN_ELECTRICITY_BILL'`,
		accountID, time.Now().AddDate(0, -4, 0)); err != nil {
		t.Fatalf("backdate bill: %v", err)
	}

	st := h.get("/api/loan-assist", token)
	saved := map[string]map[string]any{}
	for _, d := range listOf(st.Body["savedDocuments"]) {
		saved[d["slot"].(string)] = d
	}
	if len(saved) != 6 || saved[models.LoanSlotElectricityBill]["stale"] != true || saved[models.LoanSlotPAN]["requestCode"] != code {
		t.Fatalf("saved documents = %v", saved)
	}

	h.startApplication(token, 5_00_000, false)
	if res := h.post("/api/loan-assist/draft/reuse", token, map[string]any{
		"slots": []string{models.LoanSlotElectricityBill},
	}); res.Status != http.StatusBadRequest {
		t.Fatalf("reusing a stale bill: %d, want 400", res.Status)
	}
	reuse := []string{models.LoanSlotAadhaarFront, models.LoanSlotAadhaarBack, models.LoanSlotPAN,
		models.LoanSlotUdyam, models.LoanSlotNameBoard}
	res := h.post("/api/loan-assist/draft/reuse", token, map[string]any{"slots": reuse})
	if res.Status != http.StatusOK {
		t.Fatalf("reuse: %d %s", res.Status, res.Raw)
	}
	docs := reviewDocs(res)
	if docs[models.LoanSlotPAN]["reusedFrom"] != code || docs[models.LoanSlotPAN]["reviewState"] != models.LoanDocAccepted {
		t.Fatalf("reused PAN = %v", docs[models.LoanSlotPAN])
	}
	if missing := listOfStrings(res.Body["missing"]); len(missing) != 1 || missing[0] != models.LoanSlotElectricityBill {
		t.Fatalf("missing = %v, want only the bill", missing)
	}

	// Replacing a reused copy in the new application keeps the first one's.
	if res := h.uploadLoanDoc(token, models.LoanSlotPAN, "pan-new.pdf", pdfBytes); res.Status != http.StatusCreated {
		t.Fatalf("replace reused PAN: %d", res.Status)
	}
	first := h.get("/api/loan-assist/applications/"+code, token)
	if reviewDocs(first)[models.LoanSlotPAN]["fileName"] != "pan-card.pdf" {
		t.Fatalf("the first application lost its PAN: %s", first.Raw)
	}

	h.uploadAll(token, false, reuse...)
	code2 := h.payApplication(token)
	if code2 == code {
		t.Fatalf("second application has the first's code")
	}
	if n := len(listOf(h.get("/api/loan-assist", token).Body["applications"])); n != 2 {
		t.Fatalf("applications = %d, want 2", n)
	}
}

// A coupon issued for every product discounts score checks only; one scoped
// to Loan Assist discounts it.
func TestLoanAssist_OnlyScopedCouponsApply(t *testing.T) {
	h := newHarness(t)
	admin := h.makeAdmin("+919600000821")
	if res := h.createCoupon(admin, map[string]any{"code": "EVERYTHING20", "discountPercent": 20}); res.Status != http.StatusCreated {
		t.Fatalf("create unscoped coupon: %d %s", res.Status, res.Raw)
	}
	if res := h.createCoupon(admin, map[string]any{
		"code": "LOANHALF", "discountPercent": 50, "productCode": models.ProductLoanAssist,
	}); res.Status != http.StatusCreated {
		t.Fatalf("create scoped coupon: %d %s", res.Status, res.Raw)
	}
	token, _ := h.signInByPhone("+919600000822", "")
	if res := h.get("/api/coupons/quote?code=EVERYTHING20&productCode=LOAN_ASSIST", token); res.Status == http.StatusOK {
		t.Fatalf("an unscoped coupon quoted for Loan Assist: %s", res.Raw)
	}
	h.startApplication(token, 2_00_000, false)
	h.uploadAll(token, false)
	sub := h.post("/api/loan-assist/submit", token, map[string]any{"consent": true, "couponCode": "LOANHALF"})
	if sub.Status != http.StatusCreated || sub.Body["amount"] != 1499.5 {
		t.Fatalf("submit with coupon: %d %s", sub.Status, sub.Raw)
	}
}

// Account reset takes the applications and every document with them.
func TestLoanAssist_ResetRemovesApplicationsAndDocuments(t *testing.T) {
	h := newHarness(t)
	token, accountID := h.signInByPhone("+919600000831", "")
	h.startApplication(token, 2_00_000, false)
	h.uploadAll(token, false)
	admin := h.makeAdmin("+919600000832")
	res := h.post(fmt.Sprintf("/api/admin/accounts/%d/reset", accountID), admin,
		map[string]string{"confirm": "+919600000831"})
	if res.Status != http.StatusOK {
		t.Fatalf("reset: %d %s", res.Status, res.Raw)
	}
	var n int
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT (SELECT count(*) FROM loan_assist_requests WHERE account_id = $1)
		      + (SELECT count(*) FROM documents WHERE account_id = $1)`, accountID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows left after reset = %d (err %v), want 0", n, err)
	}
}

func listOfStrings(v any) []string {
	var out []string
	if xs, ok := v.([]any); ok {
		for _, x := range xs {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// A flat ₹2,000 coupon scoped to Loan Assist takes ₹2,000 off; the same kind
// of code worth more than a ₹299 check is refused there rather than making it
// free; and a coupon switches cleanly between flat and percentage.
func TestLoanAssist_FlatAmountCoupons(t *testing.T) {
	h := newHarness(t)
	admin := h.makeAdmin("+919600000841")
	if res := h.createCoupon(admin, map[string]any{
		"code": "ASSIST2000", "discountFlat": 2000, "productCode": models.ProductLoanAssist,
	}); res.Status != http.StatusCreated || res.Body["discountFlat"] != 2000.0 || res.Body["discountPercent"] != 0.0 {
		t.Fatalf("create flat coupon: %d %s", res.Status, res.Raw)
	}
	if res := h.createCoupon(admin, map[string]any{"code": "BOTHWAYS", "discountFlat": 100, "discountPercent": 10}); res.Status != http.StatusBadRequest {
		t.Fatalf("a coupon with both a percentage and a flat amount: %d, want 400", res.Status)
	}
	if res := h.createCoupon(admin, map[string]any{
		"code": "BIGFLAT", "discountFlat": 500, "productCode": models.ProductCreditAnalysis,
	}); res.Status != http.StatusCreated {
		t.Fatalf("create big flat coupon: %d %s", res.Status, res.Raw)
	}

	token, _ := h.signInByPhone("+919600000842", "")
	q := h.get("/api/coupons/quote?code=ASSIST2000&productCode=LOAN_ASSIST", token)
	if q.Status != http.StatusOK || q.Body["discountAmount"] != 2000.0 || q.Body["payableAmount"] != 999.0 {
		t.Fatalf("flat quote: %d %s", q.Status, q.Raw)
	}
	if res := h.get("/api/coupons/quote?code=BIGFLAT&productCode=CREDIT_ANALYSIS", token); res.Status != http.StatusBadRequest {
		t.Fatalf("a flat coupon worth more than the price: %d, want 400 (%s)", res.Status, res.Raw)
	}

	h.startApplication(token, 3_00_000, false)
	h.uploadAll(token, false)
	sub := h.post("/api/loan-assist/submit", token, map[string]any{"consent": true, "couponCode": "ASSIST2000"})
	if sub.Status != http.StatusCreated || sub.Body["amount"] != 999.0 || sub.Body["discountAmount"] != 2000.0 {
		t.Fatalf("submit with flat coupon: %d %s", sub.Status, sub.Raw)
	}

	// Switching kinds: a percentage replaces the flat amount, and back.
	res := h.do(http.MethodPatch, "/api/coupons/ASSIST2000", admin, map[string]any{"discountPercent": 10})
	if res.Status != http.StatusOK || res.Body["discountFlat"] != 0.0 || res.Body["discountPercent"] != 10.0 {
		t.Fatalf("to percent: %d %s", res.Status, res.Raw)
	}
	res = h.do(http.MethodPatch, "/api/coupons/ASSIST2000", admin, map[string]any{"discountFlat": 1500})
	if res.Status != http.StatusOK || res.Body["discountFlat"] != 1500.0 || res.Body["discountPercent"] != 0.0 {
		t.Fatalf("back to flat: %d %s", res.Status, res.Raw)
	}
}

// An admin makes a customer a partner from the console; the partner then
// reaches the review, and back to user takes it away.
func TestLoanAssist_AdminMakesAUserAPartner(t *testing.T) {
	h := newHarness(t)
	admin := h.makeAdmin("+919600000851")
	_, id := h.signInByPhone("+919600000852", "")

	res := h.put(fmt.Sprintf("/api/admin/accounts/%d/role", id), admin, map[string]string{"role": "partner"})
	if res.Status != http.StatusOK || res.Body["role"] != models.RolePartner {
		t.Fatalf("make partner: %d %s", res.Status, res.Raw)
	}
	// "agent" from an older console is stored under its new name.
	if res := h.put(fmt.Sprintf("/api/admin/accounts/%d/role", id), admin, map[string]string{"role": "agent"}); res.Body["role"] != models.RolePartner {
		t.Fatalf("legacy agent role stored as %v", res.Body["role"])
	}
	partner, _ := h.signInByPhone("+919600000852", "")
	if res := h.get("/api/loan-assist/review", partner); res.Status != http.StatusOK {
		t.Fatalf("partner reaching the review: %d", res.Status)
	}

	if res := h.put(fmt.Sprintf("/api/admin/accounts/%d/role", id), admin, map[string]string{"role": "user"}); res.Status != http.StatusOK {
		t.Fatalf("back to user: %d", res.Status)
	}
	user, _ := h.signInByPhone("+919600000852", "")
	if res := h.get("/api/loan-assist/review", user); res.Status != http.StatusForbidden {
		t.Fatalf("former partner reaching the review: %d, want 403", res.Status)
	}
}
