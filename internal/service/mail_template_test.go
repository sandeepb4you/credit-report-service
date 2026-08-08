package service

import (
	"strings"
	"testing"
)

func TestRenderOTPEmail_HTMLContainsOTP(t *testing.T) {
	html, _, err := renderOTPEmail("123456", 10, otpKindSignup)
	if err != nil {
		t.Fatalf("renderOTPEmail: %v", err)
	}
	if !strings.Contains(html, "123456") {
		t.Error("OTP not found in HTML body")
	}
}

func TestRenderOTPEmail_HTMLContainsBrand(t *testing.T) {
	html, _, err := renderOTPEmail("000000", 1, otpKindSignup)
	if err != nil {
		t.Fatalf("renderOTPEmail: %v", err)
	}
	if !strings.Contains(html, brandName) {
		t.Errorf("brand %q not found in HTML", brandName)
	}
}

func TestRenderOTPEmail_HTMLContainsValidity(t *testing.T) {
	html, _, err := renderOTPEmail("000000", 10, otpKindSignup)
	if err != nil {
		t.Fatalf("renderOTPEmail: %v", err)
	}
	if !strings.Contains(html, "10 minutes") {
		t.Error("expected '10 minutes' in HTML")
	}
}

func TestRenderOTPEmail_HTMLHasDocType(t *testing.T) {
	html, _, err := renderOTPEmail("000000", 1, otpKindSignup)
	if err != nil {
		t.Fatalf("renderOTPEmail: %v", err)
	}
	if !strings.Contains(html, "<!DOCTYPE html>") {
		t.Error("expected DOCTYPE in HTML output")
	}
}

func TestRenderOTPEmail_HTMLHasConfirmHeading(t *testing.T) {
	html, _, err := renderOTPEmail("000000", 1, otpKindSignup)
	if err != nil {
		t.Fatalf("renderOTPEmail: %v", err)
	}
	if !strings.Contains(html, "Confirm your email") {
		t.Error("expected 'Confirm your email' heading")
	}
}

func TestRenderOTPEmail_TextBody(t *testing.T) {
	_, text, err := renderOTPEmail("654321", 5, otpKindSignup)
	if err != nil {
		t.Fatalf("renderOTPEmail: %v", err)
	}
	if !strings.Contains(text, "654321") {
		t.Error("OTP not found in text body")
	}
	if !strings.Contains(text, "5 minutes") {
		t.Error("expected '5 minutes' in text body")
	}
	if !strings.Contains(text, brandName) {
		t.Errorf("brand %q not found in text body", brandName)
	}
}

func TestRenderOTPEmail_TextBodyNotHTML(t *testing.T) {
	_, text, _ := renderOTPEmail("000000", 1, otpKindSignup)
	if strings.Contains(text, "<") {
		t.Error("text body should not contain HTML tags")
	}
}

func TestRenderOTPEmail_YearInCopyright(t *testing.T) {
	html, _, err := renderOTPEmail("000000", 1, otpKindSignup)
	if err != nil {
		t.Fatalf("renderOTPEmail: %v", err)
	}
	// The template uses the HTML entity &copy;
	if !strings.Contains(html, "&copy;") {
		t.Error("expected &copy; copyright entity in HTML")
	}
}

// A reset code must not read like a signup code: a user who did not ask for it
// has to recognise from the wording that someone is trying to take the account.
func TestRenderOTPEmail_PasswordResetCopy(t *testing.T) {
	html, text, err := renderOTPEmail("246810", 15, otpKindPasswordReset)
	if err != nil {
		t.Fatalf("renderOTPEmail: %v", err)
	}
	for _, want := range []string{"Reset your password", "246810", "15 minutes"} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
	if strings.Contains(html, "Confirm your email") {
		t.Error("reset email reused the signup heading")
	}
	if !strings.Contains(text, "reset the password") {
		t.Errorf("text body does not say what the code is for: %q", text)
	}
}

// Both kinds must fill the same slots, so adding a kind can't silently ship an
// email with an empty heading or a raw %s where the brand should be.
func TestOTPKinds_CopyIsComplete(t *testing.T) {
	for _, k := range []otpKind{otpKindSignup, otpKindPasswordReset} {
		if k.slug == "" || k.subject == "" || k.heading == "" ||
			k.intro == "" || k.disclaimer == "" {
			t.Errorf("otpKind %q has an empty field: %+v", k.slug, k)
		}
		html, _, err := renderOTPEmail("000000", 1, k)
		if err != nil {
			t.Fatalf("renderOTPEmail(%s): %v", k.slug, err)
		}
		if strings.Contains(html, "%s") {
			t.Errorf("otpKind %q left an unformatted %%s in the body", k.slug)
		}
	}
}
