package integration

import (
	"net/http"
	"testing"
)

// POST /auth/otp/phone/status — whether a number already has an account.
//
// It backs one UI decision: design/onboarding/02.html drops the consent box and
// the referral field for a returning user, because both are addressed to
// someone registering. What matters is therefore not the bool on its own but
// that it agrees with what verifying the OTP would actually do — the sign-in is
// find-or-create, so a "registered" answer for a number that verification then
// creates would mean an account made with no consent recorded at all. Both
// tests below check the answer against a real sign-in rather than in isolation.

const statusPhone = "+919000000441"

// phoneRegistered asks the endpoint and fails the test on anything but a 200.
func (h *harness) phoneRegistered(phone string) bool {
	h.t.Helper()
	res := h.post("/api/auth/otp/phone/status", "", map[string]string{"phone": phone})
	if res.Status != http.StatusOK {
		h.t.Fatalf("phone status for %s: %d %s", phone, res.Status, res.Raw)
	}
	registered, ok := res.Body["registered"].(bool)
	if !ok {
		h.t.Fatalf("no registered flag in response: %s", res.Raw)
	}
	return registered
}

func TestPhoneStatus_UnknownNumberIsNotRegistered(t *testing.T) {
	h := newHarness(t)

	if h.phoneRegistered(statusPhone) {
		t.Fatalf("a number with no account reported as registered")
	}
	// And asking did not create one: this is a read, and an account minted by a
	// lookup would be an account nobody consented to.
	if n := h.countAccounts(); n != 0 {
		t.Fatalf("lookup created accounts: %d", n)
	}
}

func TestPhoneStatus_FlipsOnceTheNumberHasSignedIn(t *testing.T) {
	h := newHarness(t)

	if h.phoneRegistered(statusPhone) {
		t.Fatalf("reported registered before the first sign-in")
	}
	h.signInByPhone(statusPhone, "")
	if !h.phoneRegistered(statusPhone) {
		t.Fatalf("reported unregistered after the number signed in")
	}
}

// The bare ten digits and the +91 form are the same number, so they must get
// the same answer — the app sends "+91XXXXXXXXXX" but the field holds ten
// digits, and a normalization gap here would hide the consent box from a new
// user or show it to a returning one depending on which spelling arrived.
func TestPhoneStatus_NormalizesTheNumber(t *testing.T) {
	h := newHarness(t)

	h.signInByPhone(statusPhone, "")
	if !h.phoneRegistered("9000000441") {
		t.Fatalf("bare 10-digit form did not match the +91 form")
	}
}

// A malformed number is a 400, not a false. The app needs to tell "not a mobile
// number" from "not one of ours", and the shape of the number is the caller's
// own input, so saying so discloses nothing.
func TestPhoneStatus_RejectsAMalformedNumber(t *testing.T) {
	h := newHarness(t)

	res := h.post("/api/auth/otp/phone/status", "", map[string]string{"phone": "12345"})
	if res.Status != http.StatusBadRequest {
		t.Fatalf("expected 400 for a short number, got %d %s", res.Status, res.Raw)
	}
}

// The route is public on purpose — it runs before anyone has a session — which
// is exactly why it is the one enumeration oracle in the service. Pinned so
// that stays a decision rather than an accident.
func TestPhoneStatus_NeedsNoSession(t *testing.T) {
	h := newHarness(t)

	res := h.post("/api/auth/otp/phone/status", "", map[string]string{"phone": statusPhone})
	if res.Status == http.StatusUnauthorized {
		t.Fatalf("status lookup required a session")
	}
}
