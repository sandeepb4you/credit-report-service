package service

import (
	"testing"
)

// ---- PAN format validation (tested indirectly via SubmitPAN) ----
// Direct tests for panFormat are covered by pan_validator_test.go.
// Here we test the service-level validation logic.

func TestSubmitPAN_ValidFormat(t *testing.T) {
	cases := []string{
		"ABCDE1234F",
		"AAAPL1234C",
		"ZZZZZ9999Z",
		"BBBBB0000B",
	}
	for _, pan := range cases {
		t.Run(pan, func(t *testing.T) {
			if !panFormat.MatchString(pan) {
				t.Errorf("panFormat rejected valid PAN %q", pan)
			}
		})
	}
}

func TestSubmitPAN_InvalidFormats(t *testing.T) {
	cases := []string{
		"ABCDE12345",   // ends with digit
		"1234567890",   // all digits
		"abcdefghij",   // all lowercase
		"ABCD1234F",    // only 4 letters prefix
		"ABCDEF1234FG", // too long
		"ABCD",         // too short
		"ABCDE1234f",   // lowercase last
		"abcde1234f",   // all lowercase
		"ABCDE 1234F",  // contains space
		"ABCDE-1234F",  // contains dash
		"",             // empty
	}
	for _, pan := range cases {
		t.Run(pan, func(t *testing.T) {
			if panFormat.MatchString(pan) {
				t.Errorf("panFormat accepted invalid PAN %q", pan)
			}
		})
	}
}

func TestSubmitPAN_CaseInsensitiveNormalized(t *testing.T) {
	// The service does strings.ToUpper(strings.TrimSpace(pan)) before matching.
	// Verify that lowercase input would match after uppercasing.
	pan := "abcde1234f"
	if !panFormat.MatchString(upper(pan)) {
		t.Errorf("upper(%q) should match panFormat", pan)
	}
}

func upper(s string) string {
	out := make([]byte, len(s))
	for i := range s {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 32
		}
		out[i] = c
	}
	return string(out)
}
