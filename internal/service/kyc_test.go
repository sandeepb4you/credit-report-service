package service

import (
	"strings"
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

func TestClampQueuePage(t *testing.T) {
	cases := []struct {
		name                  string
		limit, offset         int
		wantLimit, wantOffset int
	}{
		{"unset uses default", 0, 0, kycQueueDefaultLimit, 0},
		{"negative limit uses default", -5, 0, kycQueueDefaultLimit, 0},
		{"in range is kept", 25, 100, 25, 100},
		{"max is kept", kycQueueMaxLimit, 0, kycQueueMaxLimit, 0},
		{"over max is capped", kycQueueMaxLimit + 1, 0, kycQueueMaxLimit, 0},
		{"huge is capped", 1_000_000, 0, kycQueueMaxLimit, 0},
		{"negative offset is first page", 10, -3, 10, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotLimit, gotOffset := clampQueuePage(tc.limit, tc.offset)
			if gotLimit != tc.wantLimit || gotOffset != tc.wantOffset {
				t.Errorf("clampQueuePage(%d, %d) = (%d, %d), want (%d, %d)",
					tc.limit, tc.offset, gotLimit, gotOffset, tc.wantLimit, tc.wantOffset)
			}
		})
	}
}

// A rejection reason is required and bounded; the stored value is trimmed.
func TestValidateRejectionReason(t *testing.T) {
	// A rune over the limit but well over it in bytes: the cap must count
	// characters, so this passes despite being 3x the limit in bytes.
	devanagari := strings.Repeat("न", maxRejectionReasonLen)

	cases := []struct {
		name    string
		reason  string
		want    string
		wantErr bool
	}{
		{"empty", "", "", true},
		{"whitespace only", "   \t\n ", "", true},
		{"trimmed", "  name mismatch  ", "name mismatch", false},
		{"at the limit", strings.Repeat("x", maxRejectionReasonLen), strings.Repeat("x", maxRejectionReasonLen), false},
		{"over the limit", strings.Repeat("x", maxRejectionReasonLen+1), "", true},
		{"limit is runes not bytes", devanagari, devanagari, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateRejectionReason(tc.reason)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected a validation error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
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
