package integration

import (
	"net/http"
	"testing"
)

// The three-step email signup: POST /auth/signup/start -> /verify -> /complete.
//
// What these pin is the ordering that is the whole reason the flow exists. The
// older POST /auth/signup creates a PENDING account before the address has been
// proven; this one creates nothing until the code checks out AND a password has
// been chosen, in a single transaction at the end. Every test below therefore
// counts accounts at a point where the older flow would already have made one.

const wizardEmail = "wizard@example.com"

// startSignup runs leg one and fails the test if it is not accepted.
func (h *harness) startSignup(email string) {
	h.t.Helper()
	res := h.post("/api/auth/signup/start", "", map[string]string{"email": email})
	if res.Status != http.StatusOK {
		h.t.Fatalf("signup/start: %d %s", res.Status, res.Raw)
	}
}

// signupGrant runs legs one and two and returns the single-use signup token.
func (h *harness) signupGrant(email string) string {
	h.t.Helper()
	h.startSignup(email)
	res := h.post("/api/auth/signup/verify", "", map[string]string{
		"email": email, "otp": testMasterOTP,
	})
	if res.Status != http.StatusOK {
		h.t.Fatalf("signup/verify: %d %s", res.Status, res.Raw)
	}
	token, _ := res.Body["signupToken"].(string)
	if token == "" {
		h.t.Fatalf("no signupToken in verify response: %s", res.Raw)
	}
	return token
}

func TestEmailSignupWizard_CreatesAnActiveAccountAndASession(t *testing.T) {
	h := newHarness(t)

	grant := h.signupGrant(wizardEmail)
	res := h.post("/api/auth/signup/complete", "", map[string]string{
		"signupToken": grant, "password": "hunter2pass",
	})
	if res.Status != http.StatusOK {
		t.Fatalf("signup/complete: %d %s", res.Status, res.Raw)
	}
	_, accountID := h.tokenAndID(res)
	if accountID == 0 {
		t.Fatalf("no account created: %s", res.Raw)
	}

	// The address was proven before the account existed, so the account arrives
	// ACTIVE with nothing left for /auth/verify-email to do.
	account, _ := res.Body["account"].(map[string]any)
	if status, _ := account["status"].(string); status != "ACTIVE" {
		t.Errorf("account status = %q, want ACTIVE", status)
	}

	// And the password that was chosen is the one that works.
	login := h.post("/api/auth/login", "", map[string]string{
		"email": wizardEmail, "password": "hunter2pass",
	})
	if login.Status != http.StatusOK {
		t.Errorf("login with the new password: %d %s", login.Status, login.Raw)
	}
}

// The headline property of the redesign: an abandoned signup leaves nothing
// behind. The older /auth/signup would have a PENDING account on the table by
// this point, and one for every attempt.
func TestEmailSignupWizard_CreatesNothingBeforeTheFinalCall(t *testing.T) {
	h := newHarness(t)

	h.startSignup(wizardEmail)
	if n := h.countAccounts(); n != 0 {
		t.Fatalf("%d accounts after start, want 0", n)
	}

	h.post("/api/auth/signup/verify", "", map[string]string{
		"email": wizardEmail, "otp": testMasterOTP,
	})
	if n := h.countAccounts(); n != 0 {
		t.Errorf("%d accounts after verify, want 0 — nothing is written until complete", n)
	}
}

// The grant is single-use, enforced by a compare-and-set on consumed_at rather
// than by a read-then-write, so a replayed request cannot create a second
// account on the same proven address.
func TestEmailSignupWizard_TheGrantIsSingleUse(t *testing.T) {
	h := newHarness(t)

	grant := h.signupGrant(wizardEmail)
	body := map[string]string{"signupToken": grant, "password": "hunter2pass"}

	if first := h.post("/api/auth/signup/complete", "", body); first.Status != http.StatusOK {
		t.Fatalf("first complete: %d %s", first.Status, first.Raw)
	}
	second := h.post("/api/auth/signup/complete", "", body)
	if second.Status != http.StatusUnauthorized {
		t.Errorf("replayed complete: %d %s, want 401", second.Status, second.Raw)
	}
	if n := h.countAccounts(); n != 1 {
		t.Errorf("%d accounts after a replayed grant, want 1", n)
	}
}

// start must answer the same way whether or not the address is taken. It is the
// mirror of /auth/password/forgot, which lies in the other direction: an honest
// answer from either would let an anonymous caller sort a list of addresses
// into registered and not.
func TestEmailSignupWizard_StartDoesNotRevealARegisteredAddress(t *testing.T) {
	h := newHarness(t)

	grant := h.signupGrant(wizardEmail)
	h.post("/api/auth/signup/complete", "", map[string]string{
		"signupToken": grant, "password": "hunter2pass",
	})

	fresh := h.post("/api/auth/signup/start", "", map[string]string{"email": "nobody@example.com"})
	taken := h.post("/api/auth/signup/start", "", map[string]string{"email": wizardEmail})

	if taken.Status != fresh.Status {
		t.Errorf("status for a taken address = %d, for a free one = %d — they must match",
			taken.Status, fresh.Status)
	}
	if taken.Raw != fresh.Raw {
		t.Errorf("body for a taken address = %s, for a free one = %s — they must match",
			taken.Raw, fresh.Raw)
	}
	if n := h.countAccounts(); n != 1 {
		t.Errorf("%d accounts, want 1 — starting on a taken address creates nothing", n)
	}
}

func TestEmailSignupWizard_CarriesTheReferralCodeThroughToTheAccount(t *testing.T) {
	h := newHarness(t)

	referrerToken, referrerID := h.signInByPhone(referrerPhone, "")
	code := h.referralCodeOf(referrerToken)

	grant := h.signupGrant(wizardEmail)
	res := h.post("/api/auth/signup/complete", "", map[string]string{
		"signupToken": grant, "password": "hunter2pass", "referralCode": code,
	})
	if res.Status != http.StatusOK {
		t.Fatalf("signup/complete: %d %s", res.Status, res.Raw)
	}
	_, joinerID := h.tokenAndID(res)

	if gotReferrer, gotCode := h.attributionOf(joinerID); gotReferrer != referrerID || gotCode != code {
		t.Errorf("attribution = account %d / code %q, want %d / %q",
			gotReferrer, gotCode, referrerID, code)
	}
}

// An unknown code fails the call rather than being ignored — telling a new user
// their friend got credit when nobody did is the worse outcome. Resolved before
// the grant is burned, so the user can retry with a corrected code.
func TestEmailSignupWizard_UnknownReferralCode_CreatesNoAccount(t *testing.T) {
	h := newHarness(t)

	grant := h.signupGrant(wizardEmail)
	res := h.post("/api/auth/signup/complete", "", map[string]string{
		"signupToken": grant, "password": "hunter2pass", "referralCode": "NOPE99X",
	})
	if res.Status != http.StatusBadRequest {
		t.Fatalf("complete with a bad code: %d %s, want 400", res.Status, res.Raw)
	}
	if n := h.countAccounts(); n != 0 {
		t.Errorf("%d accounts after a rejected code, want 0", n)
	}

	// The grant survived, so correcting the code finishes the signup.
	retry := h.post("/api/auth/signup/complete", "", map[string]string{
		"signupToken": grant, "password": "hunter2pass",
	})
	if retry.Status != http.StatusOK {
		t.Errorf("retry after a bad code: %d %s — the grant must not be spent by a "+
			"failure that happened before it was burned", retry.Status, retry.Raw)
	}
}
