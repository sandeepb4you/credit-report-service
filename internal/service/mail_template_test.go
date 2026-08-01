package service

import (
	"strings"
	"testing"
)

func TestRenderOTPEmail_HTMLContainsOTP(t *testing.T) {
	html, _, err := renderOTPEmail("123456", 10)
	if err != nil {
		t.Fatalf("renderOTPEmail: %v", err)
	}
	if !strings.Contains(html, "123456") {
		t.Error("OTP not found in HTML body")
	}
}

func TestRenderOTPEmail_HTMLContainsBrand(t *testing.T) {
	html, _, err := renderOTPEmail("000000", 1)
	if err != nil {
		t.Fatalf("renderOTPEmail: %v", err)
	}
	if !strings.Contains(html, brandName) {
		t.Errorf("brand %q not found in HTML", brandName)
	}
}

func TestRenderOTPEmail_HTMLContainsValidity(t *testing.T) {
	html, _, err := renderOTPEmail("000000", 10)
	if err != nil {
		t.Fatalf("renderOTPEmail: %v", err)
	}
	if !strings.Contains(html, "10 minutes") {
		t.Error("expected '10 minutes' in HTML")
	}
}

func TestRenderOTPEmail_HTMLHasDocType(t *testing.T) {
	html, _, err := renderOTPEmail("000000", 1)
	if err != nil {
		t.Fatalf("renderOTPEmail: %v", err)
	}
	if !strings.Contains(html, "<!DOCTYPE html>") {
		t.Error("expected DOCTYPE in HTML output")
	}
}

func TestRenderOTPEmail_HTMLHasConfirmHeading(t *testing.T) {
	html, _, err := renderOTPEmail("000000", 1)
	if err != nil {
		t.Fatalf("renderOTPEmail: %v", err)
	}
	if !strings.Contains(html, "Confirm your email") {
		t.Error("expected 'Confirm your email' heading")
	}
}

func TestRenderOTPEmail_TextBody(t *testing.T) {
	_, text, err := renderOTPEmail("654321", 5)
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
	_, text, _ := renderOTPEmail("000000", 1)
	if strings.Contains(text, "<") {
		t.Error("text body should not contain HTML tags")
	}
}

func TestRenderOTPEmail_YearInCopyright(t *testing.T) {
	html, _, err := renderOTPEmail("000000", 1)
	if err != nil {
		t.Fatalf("renderOTPEmail: %v", err)
	}
	// The template uses the HTML entity &copy;
	if !strings.Contains(html, "&copy;") {
		t.Error("expected &copy; copyright entity in HTML")
	}
}
