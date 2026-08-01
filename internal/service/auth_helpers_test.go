package service

import (
	"errors"
	"testing"

	"credit-report-service/internal/apperr"
)

func TestNormalizeEmail(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"User@Example.COM", "user@example.com"},
		{"  hello@world.io  ", "hello@world.io"},
		{"UPPER@LOWER.CASE", "upper@lower.case"},
	}
	for _, tc := range cases {
		got := normalizeEmail(tc.in)
		if got != tc.want {
			t.Errorf("normalizeEmail(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	cases := []struct {
		pw   string
		want bool
	}{
		{"short", false},
		{"1234567", false},
		{"12345678", true},
		{"longpassword", true},
		{"", false},
	}
	for _, tc := range cases {
		err := validatePassword(tc.pw)
		got := err == nil
		if got != tc.want {
			t.Errorf("validatePassword(%q): err=%v, want nil=%v", tc.pw, err, tc.want)
		}
		if err != nil {
			var v *apperr.Validation
			if !errors.As(err, &v) {
				t.Errorf("expected *apperr.Validation, got %T", err)
			}
		}
	}
}

func TestIsAdminEmail(t *testing.T) {
	svc := &AuthService{
		admins: []string{"admin@example.com", "super@test.org"},
	}
	cases := []struct {
		email string
		want  bool
	}{
		{"admin@example.com", true},
		{"Admin@Example.COM", true},
		{"  admin@example.com  ", true},
		{"user@example.com", false},
		{"", false},
	}
	for _, tc := range cases {
		got := svc.isAdminEmail(tc.email)
		if got != tc.want {
			t.Errorf("isAdminEmail(%q) = %v, want %v", tc.email, got, tc.want)
		}
	}
}

func TestIsAdminEmail_EmptyList(t *testing.T) {
	svc := &AuthService{admins: nil}
	if svc.isAdminEmail("anyone@example.com") {
		t.Error("should be false with empty admin list")
	}
}
