package service

import (
	"strings"
	"testing"
)

func TestHashEmail_StableAndNonReversible(t *testing.T) {
	a := hashEmail("user@example.com")
	b := hashEmail("user@example.com")
	if a != b {
		t.Errorf("hashEmail not stable: %q vs %q", a, b)
	}
	// Different inputs should (practically) never collide.
	c := hashEmail("other@example.com")
	if a == c {
		t.Error("hashEmail collided for distinct inputs")
	}
	// Output shape: 12 hex chars.
	if len(a) != 12 {
		t.Errorf("hashEmail len = %d, want 12", len(a))
	}
	for _, r := range a {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Errorf("hashEmail produced non-hex char %q in %q", r, a)
		}
	}
}

func TestHashEmail_DoesNotLeakAddress(t *testing.T) {
	// The digest must not contain the local or domain part of the address.
	addr := "leakme-up@contoso.com"
	h := hashEmail(addr)
	if strings.Contains(h, "leakme") || strings.Contains(h, "contoso") {
		t.Errorf("hashEmail leaked plaintext fragment into %q", h)
	}
}

func TestScrubEmail(t *testing.T) {
	addr := "user@contoso.com"
	in := "550 mailbox full for " + addr + " (smtp)"
	got := scrubEmail(in, addr)
	if strings.Contains(got, addr) {
		t.Errorf("scrubEmail left address in output: %q", got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Errorf("scrubEmail did not redact: %q", got)
	}
	// Empty email -> no-op, no panic.
	if got := scrubEmail("msg", ""); got != "msg" {
		t.Errorf("scrubEmail empty-email case = %q", got)
	}
}
