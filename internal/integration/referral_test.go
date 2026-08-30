package integration

import (
	"net/http"
	"testing"
	"time"
)

// Numbers are per-test rather than shared: the schema is rebuilt for each test,
// but reusing one number across tests makes a failure read as "the other test
// broke this one" when the real cause is elsewhere.
const (
	referrerPhone  = "+919800000001"
	joinerPhone    = "+919800000002"
	secondReferrer = "+919800000003"
	secondJoiner   = "+919800000004"
	adminPhone     = "+919800000009"
)

// ---- the code itself ------------------------------------------------------

// The shape is the requirement: seven characters, alphanumeric, and none of the
// four glyphs people confuse when a code is read down a phone line.
func TestReferralCode_IsSevenReadableAlphanumericCharacters(t *testing.T) {
	h := newHarness(t)

	token, _ := h.signInByPhone(referrerPhone, "")
	code := h.referralCodeOf(token)

	if len(code) != 7 {
		t.Errorf("code %q is %d characters, want 7", code, len(code))
	}
	for _, r := range code {
		isUpper := r >= 'A' && r <= 'Z'
		isDigit := r >= '0' && r <= '9'
		if !isUpper && !isDigit {
			t.Errorf("code %q contains %q, which is not an upper-case letter or a digit", code, r)
		}
		switch r {
		case 'I', 'O', '0', '1':
			t.Errorf("code %q contains the ambiguous glyph %q", code, r)
		}
	}
}

// The code is permanent: an account that reads it twice must not end up with
// two, or a code it already shared would stop attributing anyone.
func TestReferralCode_IsStableAcrossReads(t *testing.T) {
	h := newHarness(t)

	token, _ := h.signInByPhone(referrerPhone, "")
	first := h.referralCodeOf(token)
	second := h.referralCodeOf(token)

	if first != second {
		t.Errorf("second read returned %q, want the same code as the first (%q)", second, first)
	}
}

// ---- phone signup, the app's primary registration path --------------------

func TestPhoneSignup_WithReferralCode_AttributesTheNewAccount(t *testing.T) {
	h := newHarness(t)

	referrerToken, referrerID := h.signInByPhone(referrerPhone, "")
	code := h.referralCodeOf(referrerToken)

	_, joinerID := h.signInByPhone(joinerPhone, code)

	gotReferrer, gotCode := h.attributionOf(joinerID)
	if gotReferrer != referrerID {
		t.Errorf("referred_by_account_id = %d, want %d", gotReferrer, referrerID)
	}
	if gotCode != code {
		t.Errorf("referred_by_code = %q, want %q", gotCode, code)
	}
}

func TestPhoneSignup_WithoutReferralCode_IsUnattributed(t *testing.T) {
	h := newHarness(t)

	_, joinerID := h.signInByPhone(joinerPhone, "")

	if referrer, _ := h.attributionOf(joinerID); referrer != 0 {
		t.Errorf("referred_by_account_id = %d, want no attribution", referrer)
	}
}

// An unknown code fails the call rather than being dropped — and because the
// server resolves it before touching the challenge, the code the user is
// holding still works on the retry. Without that ordering a typo in the
// referral box would cost them an SMS and a cooldown.
func TestPhoneSignup_UnknownReferralCode_IsRejectedAndLeavesTheOtpUsable(t *testing.T) {
	h := newHarness(t)

	sent := h.post("/api/auth/otp/phone/send", "", map[string]string{"phone": joinerPhone})
	if sent.Status != http.StatusOK {
		t.Fatalf("send otp: %d %s", sent.Status, sent.Raw)
	}

	rejected := h.post("/api/auth/otp/phone/verify", "", map[string]string{
		"phone": joinerPhone, "otp": testMasterOTP, "referralCode": "NOPE99X",
	})
	if rejected.Status != http.StatusBadRequest {
		t.Fatalf("bad referral code: %d %s, want 400", rejected.Status, rejected.Raw)
	}
	if details, ok := rejected.Body["details"].(map[string]any); !ok || details["referralCode"] == nil {
		t.Errorf("error names no referralCode field, so the app cannot point at the right box: %s",
			rejected.Raw)
	}

	// Same code, no referral: the challenge survived the rejection.
	accepted := h.post("/api/auth/otp/phone/verify", "", map[string]string{
		"phone": joinerPhone, "otp": testMasterOTP,
	})
	if accepted.Status != http.StatusOK {
		t.Fatalf("retry without a referral code: %d %s, want 200 — the rejected attempt "+
			"consumed the OTP", accepted.Status, accepted.Raw)
	}
}

// Sign-in and sign-up are one endpoint here, so a code pasted into the box by a
// returning user must not move them to a new referrer.
func TestPhoneSignIn_DoesNotReattributeAnExistingAccount(t *testing.T) {
	h := newHarness(t)

	firstToken, firstID := h.signInByPhone(referrerPhone, "")
	firstCode := h.referralCodeOf(firstToken)
	secondToken, _ := h.signInByPhone(secondReferrer, "")
	secondCode := h.referralCodeOf(secondToken)

	_, joinerID := h.signInByPhone(joinerPhone, firstCode)
	// The same number signs in again, this time carrying somebody else's code.
	h.signInByPhone(joinerPhone, secondCode)

	gotReferrer, gotCode := h.attributionOf(joinerID)
	if gotReferrer != firstID || gotCode != firstCode {
		t.Errorf("attribution moved to account %d / code %q; want it left at %d / %q",
			gotReferrer, gotCode, firstID, firstCode)
	}
}

// ---- email signup, the secondary path -------------------------------------

func TestEmailSignup_CarriesTheReferralCodeThroughToTheAccount(t *testing.T) {
	h := newHarness(t)

	referrerToken, referrerID := h.signInByPhone(referrerPhone, "")
	code := h.referralCodeOf(referrerToken)

	const email = "joiner@example.com"
	signup := h.post("/api/auth/signup", "", map[string]string{
		"email": email, "password": "hunter2pass", "referralCode": code,
	})
	if signup.Status != http.StatusCreated {
		t.Fatalf("signup: %d %s", signup.Status, signup.Raw)
	}
	verified := h.post("/api/auth/verify-email", "", map[string]string{
		"email": email, "otp": testMasterOTP,
	})
	if verified.Status != http.StatusOK {
		t.Fatalf("verify email: %d %s", verified.Status, verified.Raw)
	}
	_, joinerID := h.tokenAndID(verified)

	if gotReferrer, gotCode := h.attributionOf(joinerID); gotReferrer != referrerID || gotCode != code {
		t.Errorf("attribution = account %d / code %q, want %d / %q",
			gotReferrer, gotCode, referrerID, code)
	}
}

func TestEmailSignup_UnknownReferralCode_CreatesNoAccount(t *testing.T) {
	h := newHarness(t)

	const email = "joiner@example.com"
	res := h.post("/api/auth/signup", "", map[string]string{
		"email": email, "password": "hunter2pass", "referralCode": "NOPE99X",
	})
	if res.Status != http.StatusBadRequest {
		t.Fatalf("signup with a bad code: %d %s, want 400", res.Status, res.Raw)
	}
	if n := h.countAccounts(); n != 0 {
		t.Errorf("%d accounts exist after a rejected signup, want 0 — the code is resolved "+
			"before anything is written", n)
	}
}

// ---- PAN verification through the Digitap stub ----------------------------

// The referral is only worth something if the person it brought in gets through
// onboarding, which is what the report's "Onboarded" column reads. This walks
// that far: signup with a code, then PAN verification against the offline stub.
func TestReferredSignup_CompletesPanVerificationThroughTheStub(t *testing.T) {
	h := newHarness(t)

	referrerToken, _ := h.signInByPhone(referrerPhone, "")
	code := h.referralCodeOf(referrerToken)
	joinerToken, joinerID := h.signInByPhone(joinerPhone, code)

	res := h.verifyPAN(joinerToken, stubPAN, stubPANName)
	if res.Status != http.StatusCreated {
		t.Fatalf("submit PAN: %d %s", res.Status, res.Raw)
	}
	if status, _ := res.Body["status"].(string); status != "VERIFIED" {
		t.Errorf("PAN status = %q, want VERIFIED from the stub", status)
	}

	// The provider's spelling fills the profile, which is what lets a phone
	// signup reach the dashboard without a separate name form.
	admin := h.makeAdmin(adminPhone)
	report := h.referralReport(admin, "", "", 0)
	joiner := findReferred(t, report, joinerID)
	if completed, _ := joiner["profileCompleted"].(bool); !completed {
		t.Errorf("referred account still shows profileCompleted=false after PAN verification: %v", joiner)
	}
}

// A name the provider does not hold is a 422, not a silent pass — the stub is a
// seam for the network call, not a way around the check.
func TestPanVerification_WrongName_IsRejected(t *testing.T) {
	h := newHarness(t)

	token, _ := h.signInByPhone(joinerPhone, "")

	res := h.verifyPAN(token, stubPAN, panNameWrong)
	if res.Status != http.StatusUnprocessableEntity {
		t.Errorf("submit PAN with the wrong name: %d %s, want 422", res.Status, res.Raw)
	}
}

// ---- the admin report -----------------------------------------------------

func TestReferralReport_CountsTheWindowAndListsWhoSignedUp(t *testing.T) {
	h := newHarness(t)

	referrerToken, referrerID := h.signInByPhone(referrerPhone, "")
	code := h.referralCodeOf(referrerToken)
	_, joinerID := h.signInByPhone(joinerPhone, code)

	admin := h.makeAdmin(adminPhone)
	report := h.referralReport(admin, "", "", 0)

	if total := intOf(report["totalReferred"]); total != 1 {
		t.Errorf("totalReferred = %d, want 1", total)
	}

	referrers := listOf(report["referrers"])
	if len(referrers) != 1 {
		t.Fatalf("got %d referrers, want 1: %v", len(referrers), referrers)
	}
	row := referrers[0]
	if id := int64(intOf(row["accountId"])); id != referrerID {
		t.Errorf("referrer accountId = %d, want %d", id, referrerID)
	}
	if got, _ := row["referralCode"].(string); got != code {
		t.Errorf("referrer referralCode = %q, want %q", got, code)
	}
	if n := intOf(row["referredCount"]); n != 1 {
		t.Errorf("referredCount = %d, want 1", n)
	}

	joiner := findReferred(t, report, joinerID)
	if got, _ := joiner["referredByCode"].(string); got != code {
		t.Errorf("referredByCode = %q, want %q", got, code)
	}
}

// Drilling into one referrer must not shrink the period's headline number, or
// an operator reads their own filter as a collapse in signups.
func TestReferralReport_DrillDownKeepsThePeriodTotal(t *testing.T) {
	h := newHarness(t)

	tokenA, idA := h.signInByPhone(referrerPhone, "")
	codeA := h.referralCodeOf(tokenA)
	tokenB, _ := h.signInByPhone(secondReferrer, "")
	codeB := h.referralCodeOf(tokenB)

	h.signInByPhone(joinerPhone, codeA)
	h.signInByPhone(secondJoiner, codeB)

	admin := h.makeAdmin(adminPhone)

	all := h.referralReport(admin, "", "", 0)
	if total := intOf(all["totalReferred"]); total != 2 {
		t.Fatalf("unfiltered totalReferred = %d, want 2", total)
	}

	filtered := h.referralReport(admin, "", "", idA)
	if total := intOf(filtered["totalReferred"]); total != 2 {
		t.Errorf("filtered totalReferred = %d, want the period total 2 to be unchanged", total)
	}
	page, _ := filtered["referred"].(map[string]any)
	if n := intOf(page["total"]); n != 1 {
		t.Errorf("filtered referred.total = %d, want 1", n)
	}
	if n := len(listOf(page["items"])); n != 1 {
		t.Errorf("filtered list has %d items, want 1", n)
	}
	// The leaderboard stays whole so the drill-down never hides who else was recruiting.
	if n := len(listOf(filtered["referrers"])); n != 2 {
		t.Errorf("filtered leaderboard has %d referrers, want both", n)
	}
}

// The default window is the last 30 days; anything older is out until asked for
// by name.
func TestReferralReport_DefaultWindowIsThirtyDaysAndTheRangeIsHonoured(t *testing.T) {
	h := newHarness(t)

	referrerToken, _ := h.signInByPhone(referrerPhone, "")
	code := h.referralCodeOf(referrerToken)
	_, oldJoiner := h.signInByPhone(joinerPhone, code)
	_, recentJoiner := h.signInByPhone(secondJoiner, code)

	// Push one signup well outside the default window.
	old := time.Now().UTC().AddDate(0, 0, -60)
	h.backdateReferral(oldJoiner, old)

	admin := h.makeAdmin(adminPhone)

	def := h.referralReport(admin, "", "", 0)
	if total := intOf(def["totalReferred"]); total != 1 {
		t.Errorf("default window totalReferred = %d, want 1 — the 60-day-old signup should be out", total)
	}
	findReferred(t, def, recentJoiner)

	wide := h.referralReport(admin, old.AddDate(0, 0, -1).Format("2006-01-02"),
		time.Now().UTC().Format("2006-01-02"), 0)
	if total := intOf(wide["totalReferred"]); total != 2 {
		t.Errorf("wide window totalReferred = %d, want 2", total)
	}
	findReferred(t, wide, oldJoiner)
}

func TestReferralReport_RejectsAnInvertedRange(t *testing.T) {
	h := newHarness(t)
	admin := h.makeAdmin(adminPhone)

	res := h.get("/api/admin/referrals?from=2026-08-30&to=2026-08-01", admin)
	if res.Status != http.StatusBadRequest {
		t.Errorf("inverted range: %d %s, want 400", res.Status, res.Raw)
	}
}

func TestReferralReport_RejectsAnUnparseableDate(t *testing.T) {
	h := newHarness(t)
	admin := h.makeAdmin(adminPhone)

	res := h.get("/api/admin/referrals?from=last-tuesday", admin)
	if res.Status != http.StatusBadRequest {
		t.Errorf("unparseable date: %d %s, want 400 rather than a silent default", res.Status, res.Raw)
	}
}

// The report names every referred user's phone and email, so the permission
// gate on it is the thing keeping it from being a contact-list export.
func TestReferralReport_IsRefusedWithoutTheReferralViewPermission(t *testing.T) {
	h := newHarness(t)

	plain, _ := h.signInByPhone(joinerPhone, "")

	res := h.get("/api/admin/referrals", plain)
	if res.Status != http.StatusForbidden {
		t.Errorf("plain user: %d %s, want 403", res.Status, res.Raw)
	}
}

func TestReferralReport_IsRefusedWithoutASession(t *testing.T) {
	h := newHarness(t)

	res := h.get("/api/admin/referrals", "")
	if res.Status != http.StatusUnauthorized {
		t.Errorf("anonymous: %d %s, want 401", res.Status, res.Raw)
	}
}
