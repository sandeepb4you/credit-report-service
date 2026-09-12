package integration

import (
	"net/http"
	"testing"
)

// Linking an email onto a phone account, then giving that address a password in
// the same sitting: POST /auth/email/send -> /email/verify -> /password/set.
//
// What these pin is the seam between the two halves. VerifyEmailLink writes the
// identity row with a NULL password hash on purpose, so before this endpoint
// existed the address was linked but unusable for sign-in until the user ran
// forgot-password against a mailbox they had just proved. The tests below assert
// both ends of that: login fails immediately after the link, and succeeds once
// the password is set.

const (
	linkPhone    = "+919000000451"
	linkEmail    = "linked@example.com"
	linkPassword = "hunter2pass"
)

// linkEmailTo runs both legs of the link flow for an already signed-in account.
func (h *harness) linkEmailTo(token, email string) {
	h.t.Helper()

	sent := h.post("/api/auth/email/send", token, map[string]string{"email": email})
	if sent.Status != http.StatusOK {
		h.t.Fatalf("email/send: %d %s", sent.Status, sent.Raw)
	}
	verified := h.post("/api/auth/email/verify", token, map[string]string{
		"email": email, "otp": testMasterOTP,
	})
	if verified.Status != http.StatusOK {
		h.t.Fatalf("email/verify: %d %s", verified.Status, verified.Raw)
	}
}

// profileHasPassword reads GET /profile and reports its hasPassword flag.
func (h *harness) profileHasPassword(token string) bool {
	h.t.Helper()

	res := h.get("/api/profile", token)
	if res.Status != http.StatusOK {
		h.t.Fatalf("get profile: %d %s", res.Status, res.Raw)
	}
	has, _ := res.Body["hasPassword"].(bool)
	return has
}

func TestSetInitialPassword_MakesTheLinkedEmailUsableForLogin(t *testing.T) {
	h := newHarness(t)

	token, _ := h.signInByPhone(linkPhone, "")
	h.linkEmailTo(token, linkEmail)

	// The link alone is not a credential: the identity row carries no hash, and
	// login compares against nil.
	before := h.post("/api/auth/login", "", map[string]string{
		"email": linkEmail, "password": linkPassword,
	})
	if before.Status != http.StatusUnauthorized {
		t.Fatalf("login before the password was set: want 401, got %d %s",
			before.Status, before.Raw)
	}

	set := h.post("/api/auth/password/set", token, map[string]string{"password": linkPassword})
	if set.Status != http.StatusOK {
		t.Fatalf("password/set: %d %s", set.Status, set.Raw)
	}

	after := h.post("/api/auth/login", "", map[string]string{
		"email": linkEmail, "password": linkPassword,
	})
	if after.Status != http.StatusOK {
		t.Fatalf("login after the password was set: %d %s", after.Status, after.Raw)
	}
}

// The session that set the password survives it. A reset revokes every session
// because it is a recovery flow; this one runs inside the account holder's own
// live session and only adds a credential, so signing them out would strand
// them mid-onboarding.
func TestSetInitialPassword_KeepsTheCallersSession(t *testing.T) {
	h := newHarness(t)

	token, _ := h.signInByPhone(linkPhone, "")
	h.linkEmailTo(token, linkEmail)
	if res := h.post("/api/auth/password/set", token,
		map[string]string{"password": linkPassword}); res.Status != http.StatusOK {
		t.Fatalf("password/set: %d %s", res.Status, res.Raw)
	}

	if res := h.get("/api/profile", token); res.Status != http.StatusOK {
		t.Fatalf("profile with the same token afterwards: %d %s", res.Status, res.Raw)
	}
}

// One-way: the endpoint fills a NULL hash and refuses to replace one, so a
// stolen access token cannot be turned into a permanent credential.
func TestSetInitialPassword_RefusesASecondTime(t *testing.T) {
	h := newHarness(t)

	token, _ := h.signInByPhone(linkPhone, "")
	h.linkEmailTo(token, linkEmail)
	if res := h.post("/api/auth/password/set", token,
		map[string]string{"password": linkPassword}); res.Status != http.StatusOK {
		t.Fatalf("password/set: %d %s", res.Status, res.Raw)
	}

	again := h.post("/api/auth/password/set", token, map[string]string{"password": "another1pass"})
	if again.Status != http.StatusConflict {
		t.Fatalf("second password/set: want 409, got %d %s", again.Status, again.Raw)
	}

	// And the first password still works, i.e. the refusal changed nothing.
	login := h.post("/api/auth/login", "", map[string]string{
		"email": linkEmail, "password": linkPassword,
	})
	if login.Status != http.StatusOK {
		t.Fatalf("login with the original password: %d %s", login.Status, login.Raw)
	}
}

// GET /profile reports whether a password exists, which is the only way the app
// can decide whether to offer the "set a password" row on the account screen.
// Three states, in the order a phone signup passes through them.
func TestProfileReportsWhetherAPasswordExists(t *testing.T) {
	h := newHarness(t)

	token, _ := h.signInByPhone(linkPhone, "")
	if got := h.profileHasPassword(token); got {
		t.Fatalf("a fresh phone account has no password, got hasPassword=%v", got)
	}

	h.linkEmailTo(token, linkEmail)
	if got := h.profileHasPassword(token); got {
		t.Fatalf("a linked address carries no password until one is set, got hasPassword=%v", got)
	}

	if res := h.post("/api/auth/password/set", token,
		map[string]string{"password": linkPassword}); res.Status != http.StatusOK {
		t.Fatalf("password/set: %d %s", res.Status, res.Raw)
	}
	if got := h.profileHasPassword(token); !got {
		t.Fatalf("after password/set the profile must say so, got hasPassword=%v", got)
	}
}

// The same flag on the response password/set itself returns, so a client can act
// on it without a follow-up read.
func TestSetPasswordResponseReportsTheNewState(t *testing.T) {
	h := newHarness(t)

	token, _ := h.signInByPhone(linkPhone, "")
	h.linkEmailTo(token, linkEmail)

	res := h.post("/api/auth/password/set", token, map[string]string{"password": linkPassword})
	if res.Status != http.StatusOK {
		t.Fatalf("password/set: %d %s", res.Status, res.Raw)
	}
	if has, _ := res.Body["hasPassword"].(bool); !has {
		t.Fatalf("password/set should return hasPassword=true: %s", res.Raw)
	}
}

// No address, nothing for the password to belong to. A phone account that has
// not linked an email is refused rather than given a credential with no
// identifier to present it with.
func TestSetInitialPassword_RequiresALinkedEmail(t *testing.T) {
	h := newHarness(t)

	token, _ := h.signInByPhone(linkPhone, "")
	res := h.post("/api/auth/password/set", token, map[string]string{"password": linkPassword})
	if res.Status != http.StatusConflict {
		t.Fatalf("password/set with no email linked: want 409, got %d %s", res.Status, res.Raw)
	}
}

// The service's shared password rules apply here as they do to signup and reset.
func TestSetInitialPassword_RejectsAShortPassword(t *testing.T) {
	h := newHarness(t)

	token, _ := h.signInByPhone(linkPhone, "")
	h.linkEmailTo(token, linkEmail)

	res := h.post("/api/auth/password/set", token, map[string]string{"password": "short"})
	if res.Status != http.StatusBadRequest {
		t.Fatalf("password/set with a 5-character password: want 400, got %d %s",
			res.Status, res.Raw)
	}
}

func TestSetInitialPassword_RequiresAuthentication(t *testing.T) {
	h := newHarness(t)

	res := h.post("/api/auth/password/set", "", map[string]string{"password": linkPassword})
	if res.Status != http.StatusUnauthorized {
		t.Fatalf("password/set with no token: want 401, got %d %s", res.Status, res.Raw)
	}
}
