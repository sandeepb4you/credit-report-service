package service

import (
	"strings"
	"testing"

	"credit-report-service/internal/models"
)

// Referral codes get read aloud and retyped, so the alphabet must exclude the
// glyphs people confuse, and the shape must stay recognizable.
func TestGenerateReferralCode_Shape(t *testing.T) {
	code, err := generateReferralCode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(code, models.ReferralCodePrefix+"-") {
		t.Errorf("code %q missing the REF- prefix", code)
	}
	if len(code) > models.CouponCodeMaxLen {
		t.Errorf("code %q is %d chars, over the %d column limit",
			code, len(code), models.CouponCodeMaxLen)
	}
	// Must survive the same validation a hand-typed code goes through.
	if _, err := normalizeCouponCode(code); err != nil {
		t.Errorf("generated code %q fails coupon code validation: %v", code, err)
	}
}

func TestGenerateReferralCode_AvoidsAmbiguousGlyphs(t *testing.T) {
	for i := 0; i < 200; i++ {
		code, err := generateReferralCode()
		if err != nil {
			t.Fatal(err)
		}
		body := strings.TrimPrefix(code, models.ReferralCodePrefix+"-")
		for _, bad := range []string{"I", "O", "0", "1"} {
			if strings.Contains(body, bad) {
				t.Fatalf("code %q contains ambiguous glyph %q", code, bad)
			}
		}
	}
}

// Codes must not be guessable: the code is the only thing preventing a
// stranger from mis-attributing signups to someone else.
func TestGenerateReferralCode_Unique(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		code, err := generateReferralCode()
		if err != nil {
			t.Fatal(err)
		}
		if seen[code] {
			t.Fatalf("duplicate referral code generated at iteration %d: %s", i, code)
		}
		seen[code] = true
	}
}

// normalizeCouponCode is shared by both kinds, so a referral code typed in
// lower case, or with padding, still resolves.
func TestNormalizeCouponCode_ReferralRoundTrip(t *testing.T) {
	got, err := normalizeCouponCode("  ref-7k2qm4xz  ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "REF-7K2QM4XZ" {
		t.Errorf("got %q, want REF-7K2QM4XZ", got)
	}
}

func TestNormalizeCouponCode_Rejects(t *testing.T) {
	for _, bad := range []string{"", "  ", "ab", strings.Repeat("A", 33), "SAVE 20", "SAVE$20", "REF/1"} {
		if _, err := normalizeCouponCode(bad); err == nil {
			t.Errorf("expected %q to be rejected", bad)
		}
	}
}
