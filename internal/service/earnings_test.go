package service

import (
	"testing"
)

// Masking and validation are the privacy boundary of the user-facing referral
// list: the tests pin that a full number or address can never come back, and
// that the bank form rejects what the payout would choke on.
func TestMaskPhone_KeepsPrefixFirstTwoAndLastThree(t *testing.T) {
	if got := maskPhone("+919810000001"); got != "+91 98xxx xx001" {
		t.Errorf("maskPhone = %q, want %q", got, "+91 98xxx xx001")
	}
	if got := maskPhone("9810000001"); got != "+91 98xxx xx001" {
		t.Errorf("bare 10-digit maskPhone = %q, want prefixed form", got)
	}
}

func TestMaskPhone_UnparseableIsBlank(t *testing.T) {
	for _, in := range []string{"", "123", "not-a-number", "+1 555 123 4567"} {
		if got := maskPhone(in); got != "" {
			t.Errorf("maskPhone(%q) = %q, want blank rather than a wrong mask", in, got)
		}
	}
}

func TestMaskEmail_KeepsFirstLastAndDomain(t *testing.T) {
	if got := maskEmail("rahul@gmail.com"); got != "r****l@gmail.com" {
		t.Errorf("maskEmail = %q, want r****l@gmail.com", got)
	}
}

func TestMaskEmail_UnparseableIsBlank(t *testing.T) {
	for _, in := range []string{"", "no-at-sign", "a@", "@b.com"} {
		if got := maskEmail(in); got != "" {
			t.Errorf("maskEmail(%q) = %q, want blank", in, got)
		}
	}
}

func TestValidIFSC_AcceptsBankCodeZeroBranch(t *testing.T) {
	for _, ok := range []string{"HDFC0001234", "SBIN0060234", "ICIC0000001"} {
		if !validIFSC(ok) {
			t.Errorf("validIFSC(%q) = false, want true", ok)
		}
	}
}

func TestValidIFSC_RejectsWrongShapes(t *testing.T) {
	for _, bad := range []string{"", "NOPE", "HDFC000123", "HDFC00012345", "hdfc0001234", "HDFC1001234"} {
		if validIFSC(bad) {
			t.Errorf("validIFSC(%q) = true, want false", bad)
		}
	}
}

func TestValidAccountNumber_NineToEighteenDigits(t *testing.T) {
	for _, ok := range []string{"123456789", "123456789012345678", "1234 5678 9012"} {
		if !validAccountNumber(ok) {
			t.Errorf("validAccountNumber(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "12345678", "1234567890123456789", "12345abc678", "1234-5678-90"} {
		if validAccountNumber(bad) {
			t.Errorf("validAccountNumber(%q) = true, want false", bad)
		}
	}
}

func TestLast4_TakesDigitTail(t *testing.T) {
	if got := last4("501002345821"); got != "5821" {
		t.Errorf("last4 = %q, want 5821", got)
	}
}

func TestFormatRupees_IndianGrouping(t *testing.T) {
	cases := map[int]string{
		50000:     "Rs 500",
		12500:     "Rs 125",
		125000:    "Rs 1,250",
		123456700: "Rs 12,34,567",
	}
	for in, want := range cases {
		if got := formatRupees(in); got != want {
			t.Errorf("formatRupees(%d) = %q, want %q", in, got, want)
		}
	}
}
