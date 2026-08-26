package service

import (
	"testing"
	"time"
)

// The provider's DOB is half the credit-report PDF password, and it is the only
// source of one for a phone signup — PAN verification fills the name and goes
// straight to the dashboard, so nobody ever types a date in. It still must never
// fail a PAN check that the provider already answered, hence nil over error.
func TestParsePrefillDOB(t *testing.T) {
	cases := []struct {
		in   string
		want string // "" means nil
	}{
		// The format a real response carries.
		{"24-09-1991", "1991-09-24"},
		{"02-04-1980", "1980-04-02"},
		{" 15-08-1947 ", "1947-08-15"},
		// The provider's own sheet writes the same field ISO-style.
		{"1991-09-24", "1991-09-24"},
		{"15/08/1947", "1947-08-15"},
		// Nothing usable.
		{"", ""},
		{"?", ""},
		{"not a date", ""},
		{"31-02-1991", ""}, // no such day
		{"24-13-1991", ""}, // no such month
		// Parses cleanly but is not a date of birth.
		{"24-09-1800", ""},
		{"24-09-3000", ""},
	}
	for _, tc := range cases {
		got := parsePrefillDOB(tc.in)
		if tc.want == "" {
			if got != nil {
				t.Errorf("parsePrefillDOB(%q) = %v, want nil", tc.in, got.Format("2006-01-02"))
			}
			continue
		}
		if got == nil {
			t.Errorf("parsePrefillDOB(%q) = nil, want %s", tc.in, tc.want)
			continue
		}
		if s := got.Format("2006-01-02"); s != tc.want {
			t.Errorf("parsePrefillDOB(%q) = %s, want %s", tc.in, s, tc.want)
		}
		if got.Location() != time.UTC {
			t.Errorf("parsePrefillDOB(%q) not UTC: %v", tc.in, got.Location())
		}
	}
}

// An ambiguous date must read as DD-MM, the provider's format -- never MM-DD.
// Silently transposing day and month would produce a password nobody can type.
func TestParsePrefillDOB_IsDayFirst(t *testing.T) {
	got := parsePrefillDOB("04-09-1991")
	if got == nil {
		t.Fatal("nil")
	}
	if got.Day() != 4 || got.Month() != time.September {
		t.Errorf("got %s, want 4 September (day-first)", got.Format("2006-01-02"))
	}
}
