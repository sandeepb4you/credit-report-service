// User-initiated account deletion, end to end against real handlers and a real
// Postgres: send a code, redeem it for a grant, schedule the purge, then drive
// the sweeper and check what is left.
//
// The properties worth pinning are the ones that are invisible in a passing
// happy path:
//
//   - the public send route answers IDENTICALLY for a registered and an
//     unregistered identifier, because it is the one thing standing between an
//     anonymous caller and a list of which phone numbers have accounts;
//   - signing in cancels a scheduled deletion, which is the entire safety net
//     behind handing that power to a single SMS code;
//   - the purge destroys the person and KEEPS the money, which is the promise
//     the web page makes to the user and the one the tax rules make to us.
package integration

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"credit-report-service/internal/repository"
	"credit-report-service/internal/service"
)

// requestDeletion runs send + verify and returns the single-use grant.
func (h *harness) requestDeletion(identifier string) string {
	h.t.Helper()

	sent := h.post("/api/auth/account-deletion/send", "",
		map[string]string{"identifier": identifier})
	if sent.Status != http.StatusOK {
		h.t.Fatalf("send deletion code for %s: %d %s", identifier, sent.Status, sent.Raw)
	}

	verified := h.post("/api/auth/account-deletion/verify", "",
		map[string]string{"identifier": identifier, "otp": testMasterOTP})
	if verified.Status != http.StatusOK {
		h.t.Fatalf("verify deletion code for %s: %d %s", identifier, verified.Status, verified.Raw)
	}
	token, _ := verified.Body["deletionToken"].(string)
	if token == "" {
		h.t.Fatalf("no deletionToken in verify response: %s", verified.Raw)
	}
	return token
}

// deletionStatus reads the request row's status straight from the database:
// there is no read endpoint, and the point of most of these assertions is what
// the row says rather than what an API chose to report.
func (h *harness) deletionStatus(accountID int64) string {
	h.t.Helper()
	var status string
	err := h.pool.QueryRow(h.baseCtx,
		`SELECT status FROM account_deletion_requests
		  WHERE account_id = $1 ORDER BY id DESC LIMIT 1`, accountID).Scan(&status)
	if err != nil {
		return ""
	}
	return status
}

// dueNow drags a pending request's deadline into the past so the sweeper will
// act on it. Backdating the row rather than waiting fourteen days, and rather
// than making the grace period configurable purely so a test can shorten it.
func (h *harness) dueNow(accountID int64) {
	h.t.Helper()
	if _, err := h.pool.Exec(h.baseCtx,
		`UPDATE account_deletion_requests SET scheduled_for = now() - interval '1 minute'
		  WHERE account_id = $1 AND status = 'PENDING'`, accountID); err != nil {
		h.t.Fatalf("backdate deletion request: %v", err)
	}
}

// sweeper builds the worker over the harness's pool, wired as main.go does but
// with no object store — the S3 seam is stubbed everywhere in these tests, and
// the sweeper's contract is that a missing store is loud, not fatal.
func (h *harness) sweeper() *service.AccountDeletionSweeper {
	h.t.Helper()
	return service.NewAccountDeletionSweeper(
		repository.NewAccountRepo(h.pool), nil, time.Hour)
}

// recordingObjects stands in for the S3 store and records what the sweeper
// asked it to delete, which is the only way to see an object the database has
// no row for.
type recordingObjects struct{ deleted []string }

func (r *recordingObjects) Delete(_ context.Context, keyOrURI string) error {
	r.deleted = append(r.deleted, keyOrURI)
	return nil
}
func (r *recordingObjects) IsStub() bool { return false }

func TestAccountDeletionSchedulesAndPurges(t *testing.T) {
	h := newHarness(t)

	const phone = "+919000000601"
	token, accountID := h.signInByPhone(phone, "")

	// A real purchase, so the test covers the two things a bare account cannot:
	// the scheduled_score_checks rows whose order_id foreign key broke the
	// admin reset in production, and an orders row that must SURVIVE the purge.
	orderUID := h.buyPlan(token, "SCORE_PLUS_MONTHLY")

	// The personal data that retained rows can carry, seeded in the shapes the
	// live system produces. The harness webhook is minimal; Cashfree's real one
	// echoes back the customer_details the order was created with, plus the
	// payment instrument.
	const upiID = "asha.rao@okhdfc"
	if _, err := h.pool.Exec(h.baseCtx,
		`UPDATE payment_webhook_events
		    SET payload = jsonb_set(jsonb_set(payload,
		        '{data,customer_details}', $2::jsonb),
		        '{data,payment,payment_method}', $3::jsonb)
		  WHERE order_uid = $1`,
		orderUID,
		`{"customer_name":"Asha Rao","customer_phone":"9000000601","customer_email":"asha@example.com","customer_id":"acct_x"}`,
		fmt.Sprintf(`{"upi":{"channel":"collect","upi_id":%q}}`, upiID),
	); err != nil {
		t.Fatalf("seed webhook customer details: %v", err)
	}
	// Guard against a vacuous pass: the "must not contain" checks after the
	// purge prove nothing unless the personal data was really there before it.
	var seeded string
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT payload::text FROM payment_webhook_events WHERE order_uid = $1`, orderUID,
	).Scan(&seeded); err != nil || !strings.Contains(seeded, "Asha Rao") || !strings.Contains(seeded, upiID) {
		t.Fatalf("seeded webhook does not carry the customer details (err=%v): %s", err, seeded)
	}
	// A payout requested and not yet paid, naming the bank account holder.
	if _, err := h.pool.Exec(h.baseCtx,
		`INSERT INTO referral_withdrawals (account_id, amount_paise, holder_name, account_last4, ifsc)
		 VALUES ($1, 50000, 'Asha Rao', '4321', 'HDFC0001234')`, accountID); err != nil {
		t.Fatalf("seed withdrawal: %v", err)
	}
	// The account's own referral code, minted on first read.
	referralCode := h.referralCodeOf(token)
	// A stored report, whose advanced PDF lives at a key no row records.
	reportID := h.insertScoredReport(accountID, 720)

	grant := h.requestDeletion(phone)

	confirmed := h.post("/api/auth/account-deletion/confirm", "",
		map[string]string{"deletionToken": grant})
	if confirmed.Status != http.StatusOK {
		t.Fatalf("confirm deletion: %d %s", confirmed.Status, confirmed.Raw)
	}
	if got := h.deletionStatus(accountID); got != "PENDING" {
		t.Fatalf("deletion status after confirm = %q, want PENDING", got)
	}

	// The date must be roughly fourteen days out. Checked as a range rather
	// than an equality because the server stamps it from its own clock.
	scheduled, _ := confirmed.Body["scheduledFor"].(string)
	when, err := time.Parse(time.RFC3339, scheduled)
	if err != nil {
		t.Fatalf("scheduledFor %q is not a timestamp: %v", scheduled, err)
	}
	if d := time.Until(when); d < 13*24*time.Hour || d > 15*24*time.Hour {
		t.Errorf("scheduled %v from now, want ~14 days", d)
	}

	// Confirming signs every device out — checked against the sessions table,
	// not by calling an endpoint with the old access token.
	//
	// That distinction is the real behaviour and worth stating: revocation
	// acts on the REFRESH half of the pair, and an access token already minted
	// keeps working on ordinary routes (GET /profile among them) until it
	// expires at auth.access-ttl. Only the permission-gated routes re-check
	// token_epoch. An assertion that /profile starts failing here would be
	// asserting something this service does not do — the same caveat the admin
	// account reset carries.
	if n := h.liveSessions(accountID); n != 0 {
		t.Errorf("%d sessions still live after deletion was confirmed", n)
	}

	// Nothing has actually been deleted yet — that is the whole point of the
	// grace period, and a purge that ran at confirm time would still pass
	// every assertion above.
	if !h.loginAlive(accountID) {
		t.Fatalf("the login was destroyed before the grace period elapsed")
	}

	h.dueNow(accountID)
	objects := &recordingObjects{}
	service.NewAccountDeletionSweeper(repository.NewAccountRepo(h.pool), objects, time.Hour).
		Sweep(h.baseCtx)

	if got := h.deletionStatus(accountID); got != "COMPLETED" {
		t.Fatalf("deletion status after sweep = %q, want COMPLETED", got)
	}

	// The advanced PDF is unencrypted and named by nothing in the database; the
	// sweep has to ask for it by its derived key.
	wantKey := fmt.Sprintf("advanced-reports/%d/%d.pdf", accountID, reportID)
	found := false
	for _, k := range objects.deleted {
		found = found || k == wantKey
	}
	if !found {
		t.Errorf("advanced report %s was not deleted; asked for %v", wantKey, objects.deleted)
	}

	// Retained rows must be anonymous in content, not only in link. This is
	// what the privacy policy's retention section promises.
	var payload string
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT payload::text FROM payment_webhook_events WHERE order_uid = $1`, orderUID,
	).Scan(&payload); err != nil {
		t.Fatalf("read retained webhook: %v", err)
	}
	for _, personal := range []string{"Asha Rao", "9000000601", "asha@example.com", upiID, "customer_details", "acct_x"} {
		if strings.Contains(payload, personal) {
			t.Errorf("retained webhook payload still carries %q: %s", personal, payload)
		}
	}
	for _, needed := range []string{orderUID, "stub-pay-1", "SUCCESS", "PAYMENT_SUCCESS_WEBHOOK"} {
		if !strings.Contains(payload, needed) {
			t.Errorf("scrub removed %q, which reconciling the payment needs: %s", needed, payload)
		}
	}

	var wStatus, holder, last4 string
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT status, holder_name, account_last4 FROM referral_withdrawals WHERE account_id = $1`,
		accountID).Scan(&wStatus, &holder, &last4); err != nil {
		t.Fatalf("read retained withdrawal: %v", err)
	}
	if holder == "Asha Rao" {
		t.Errorf("withdrawal kept the account holder's name")
	}
	if wStatus != "REJECTED" {
		t.Errorf("unpaid withdrawal status = %q, want REJECTED: its payout account was deleted", wStatus)
	}
	if last4 != "4321" {
		t.Errorf("account_last4 = %q; it is kept to match the payout to a bank statement", last4)
	}

	var revoked bool
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT revoked_at IS NOT NULL FROM coupons WHERE code = $1`, referralCode,
	).Scan(&revoked); err != nil {
		t.Fatalf("read referral code: %v", err)
	}
	if !revoked {
		t.Errorf("referral code %s still live on a deleted account", referralCode)
	}

	// Including the challenge raised before the account existed, whose
	// account_id is NULL — only the destination still names the number.
	var challenges int
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT count(*) FROM otp_challenges WHERE destination = $1`, phone,
	).Scan(&challenges); err != nil {
		t.Fatalf("count otp challenges: %v", err)
	}
	if challenges != 0 {
		t.Errorf("%d otp challenges still carry the deleted account's number", challenges)
	}

	// The person is gone.
	var status string
	var email, phoneCol, first, dob *string
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT status, primary_email, primary_phone, first_name, date_of_birth::text
		   FROM accounts WHERE id = $1`, accountID,
	).Scan(&status, &email, &phoneCol, &first, &dob); err != nil {
		t.Fatalf("read purged account: %v", err)
	}
	if status != "DELETED" {
		t.Errorf("account status = %q, want DELETED", status)
	}
	for name, v := range map[string]*string{
		"primary_email": email, "primary_phone": phoneCol,
		"first_name": first, "date_of_birth": dob,
	} {
		if v != nil {
			t.Errorf("%s survived the purge: %q", name, *v)
		}
	}
	if h.loginAlive(accountID) {
		t.Errorf("the login survived the purge")
	}
	if n := h.countIdentities(accountID); n != 0 {
		t.Errorf("%d auth_identities survived the purge", n)
	}

	// The money stayed.
	if n := h.countRows("orders", accountID); n == 0 {
		t.Errorf("orders were deleted; the financial record must outlive the account")
	}
	// ...and the entitlements did not.
	if n := h.countRows("scheduled_score_checks", accountID); n != 0 {
		t.Errorf("%d scheduled_score_checks survived the purge", n)
	}
}

// The number must be reusable afterwards. Nulling primary_phone is what
// releases its UNIQUE constraint; leaving it set would lock the number out of
// the service forever with an error nobody could diagnose from the app.
func TestAccountDeletionFreesTheNumber(t *testing.T) {
	h := newHarness(t)

	const phone = "+919000000602"
	_, firstID := h.signInByPhone(phone, "")

	grant := h.requestDeletion(phone)
	h.post("/api/auth/account-deletion/confirm", "", map[string]string{"deletionToken": grant})
	h.dueNow(firstID)
	h.sweeper().Sweep(h.baseCtx)

	_, secondID := h.signInByPhone(phone, "")
	if secondID == firstID {
		t.Fatalf("signing in after a purge reused account %d; it should be a new one", firstID)
	}
}

// Signing in cancels a scheduled deletion. This is the safety net that makes a
// single SMS code an acceptable authenticator for erasing an account, so it is
// pinned against the real sign-in path rather than the service method.
func TestSignInCancelsScheduledDeletion(t *testing.T) {
	h := newHarness(t)

	const phone = "+919000000603"
	_, accountID := h.signInByPhone(phone, "")

	grant := h.requestDeletion(phone)
	h.post("/api/auth/account-deletion/confirm", "", map[string]string{"deletionToken": grant})
	if got := h.deletionStatus(accountID); got != "PENDING" {
		t.Fatalf("deletion status = %q, want PENDING", got)
	}

	h.signInByPhone(phone, "")

	if got := h.deletionStatus(accountID); got != "CANCELLED" {
		t.Fatalf("deletion status after signing in = %q, want CANCELLED", got)
	}

	// And a sweep must then leave it entirely alone, even past the deadline.
	h.dueNow(accountID) // a no-op on a CANCELLED row; proves the sweep filters on status
	h.sweeper().Sweep(h.baseCtx)
	if !h.loginAlive(accountID) {
		t.Errorf("a cancelled request was still purged")
	}
}

// The public send route must not become an account-enumeration oracle: a
// number with an account and one without have to be indistinguishable from
// outside. /auth/otp/phone/status is the service's one deliberate exception
// and this must never quietly become the second.
func TestAccountDeletionSendDoesNotEnumerate(t *testing.T) {
	h := newHarness(t)

	const registered = "+919000000604"
	const unknown = "+919000000605"
	h.signInByPhone(registered, "")

	known := h.post("/api/auth/account-deletion/send", "",
		map[string]string{"identifier": registered})
	stranger := h.post("/api/auth/account-deletion/send", "",
		map[string]string{"identifier": unknown})

	if known.Status != stranger.Status {
		t.Errorf("status differs by registration: known %d, unknown %d",
			known.Status, stranger.Status)
	}
	if known.Raw != stranger.Raw {
		t.Errorf("body differs by registration:\n known:   %s\n unknown: %s",
			known.Raw, stranger.Raw)
	}

	// An unknown number must also not be verifiable into a grant — the
	// identical 200 above is a deliberate lie, not a real challenge.
	res := h.post("/api/auth/account-deletion/verify", "",
		map[string]string{"identifier": unknown, "otp": testMasterOTP})
	if res.Status == http.StatusOK {
		t.Errorf("verify succeeded for an unregistered number: %s", res.Raw)
	}
}

// The grant is single-use, and a second confirm on an account that is already
// scheduled returns the ORIGINAL date rather than moving the deadline. Someone
// holding one stolen code must not be able to walk the window down.
func TestAccountDeletionGrantIsSingleUse(t *testing.T) {
	h := newHarness(t)

	const phone = "+919000000606"
	h.signInByPhone(phone, "")

	grant := h.requestDeletion(phone)
	first := h.post("/api/auth/account-deletion/confirm", "",
		map[string]string{"deletionToken": grant})
	if first.Status != http.StatusOK {
		t.Fatalf("first confirm: %d %s", first.Status, first.Raw)
	}

	replay := h.post("/api/auth/account-deletion/confirm", "",
		map[string]string{"deletionToken": grant})
	if replay.Status == http.StatusOK {
		t.Fatalf("a consumed deletion grant was accepted a second time: %s", replay.Raw)
	}

	// A fresh grant on an already-scheduled account is not an error, but it
	// must report the date that was already set.
	second := h.post("/api/auth/account-deletion/confirm", "",
		map[string]string{"deletionToken": h.requestDeletion(phone)})
	if second.Status != http.StatusOK {
		t.Fatalf("second confirm with a fresh grant: %d %s", second.Status, second.Raw)
	}
	if already, _ := second.Body["alreadyPending"].(bool); !already {
		t.Errorf("alreadyPending = false on a re-confirm: %s", second.Raw)
	}
	if first.Body["scheduledFor"] != second.Body["scheduledFor"] {
		t.Errorf("deadline moved on re-confirm: %v -> %v",
			first.Body["scheduledFor"], second.Body["scheduledFor"])
	}
}

// loginAlive reports whether anything is left that could sign this account in.
//
// Not a count of auth_identities, which is the obvious probe and wrong here: a
// phone account has NO identity row at all — the number lives on
// accounts.primary_phone, and that column is both the credential and the
// UNIQUE key. An identity-row check would therefore pass on an account whose
// login was untouched, and pass just as happily on one that was never
// deletable in the first place.
func (h *harness) loginAlive(accountID int64) bool {
	h.t.Helper()
	var alive bool
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT (a.status <> 'DELETED')
		    AND (a.primary_phone IS NOT NULL
		         OR a.primary_email IS NOT NULL
		         OR EXISTS (SELECT 1 FROM auth_identities i WHERE i.account_id = a.id))
		   FROM accounts a WHERE a.id = $1`, accountID).Scan(&alive); err != nil {
		h.t.Fatalf("read login state for account %d: %v", accountID, err)
	}
	return alive
}

// liveSessions counts the account's unrevoked sessions.
func (h *harness) liveSessions(accountID int64) int {
	h.t.Helper()
	var n int
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT count(*) FROM sessions WHERE account_id = $1 AND revoked_at IS NULL`,
		accountID).Scan(&n); err != nil {
		h.t.Fatalf("count live sessions: %v", err)
	}
	return n
}

// countIdentities and countRows read the database directly; the assertions
// above are about rows, not about anything an endpoint reports.
func (h *harness) countIdentities(accountID int64) int {
	h.t.Helper()
	return h.countRows("auth_identities", accountID)
}

func (h *harness) countRows(table string, accountID int64) int {
	h.t.Helper()
	var n int
	// table is a literal from this file only — never user input.
	if err := h.pool.QueryRow(h.baseCtx,
		`SELECT count(*) FROM `+table+` WHERE account_id = $1`, accountID).Scan(&n); err != nil {
		h.t.Fatalf("count %s: %v", table, err)
	}
	return n
}
