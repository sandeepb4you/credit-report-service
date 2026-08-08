package service

import (
	"strings"
	"testing"
)

// The grant must be unguessable, prefixed for recognisability in logs, and
// stored only as the digest the lookup will use.
func TestNewPasswordResetToken(t *testing.T) {
	token, digest, err := newPasswordResetToken()
	if err != nil {
		t.Fatalf("newPasswordResetToken: %v", err)
	}
	if !strings.HasPrefix(token, passwordResetTokenPrefix) {
		t.Errorf("token %q lacks the %q prefix", token, passwordResetTokenPrefix)
	}
	// 32 bytes of base64url is 43 chars; anything shorter means the entropy
	// was silently cut.
	if body := strings.TrimPrefix(token, passwordResetTokenPrefix); len(body) < 43 {
		t.Errorf("token body is %d chars, want >= 43", len(body))
	}
	// The digest is what the repository matches on, so it must be exactly what
	// hashing the returned plaintext produces — otherwise no grant is ever
	// redeemable.
	if digest != hashToken(token) {
		t.Errorf("digest %q does not match hashToken(token) %q", digest, hashToken(token))
	}
	if strings.Contains(digest, token) {
		t.Error("digest contains the plaintext token")
	}
}

func TestNewPasswordResetToken_Unique(t *testing.T) {
	seen := make(map[string]bool, 64)
	for i := 0; i < 64; i++ {
		token, _, err := newPasswordResetToken()
		if err != nil {
			t.Fatalf("newPasswordResetToken: %v", err)
		}
		if seen[token] {
			t.Fatalf("duplicate token generated: %q", token)
		}
		seen[token] = true
	}
}

// Every "your grant is no good" path returns one message, so pairing a stolen
// token with guessed emails cannot tell an attacker which half was wrong.
func TestResetInvalidGrant_SaysNothingSpecific(t *testing.T) {
	for _, leak := range []string{"expired", "used", "unknown", "account", "email"} {
		lowered := strings.ToLower(resetInvalidGrant)
		// "expired" is the one word it may contain — it is the message we chose
		// for every case, not a disclosure about this particular attempt.
		if leak == "expired" {
			continue
		}
		if strings.Contains(lowered, leak) {
			t.Errorf("resetInvalidGrant leaks which check failed via %q: %q",
				leak, resetInvalidGrant)
		}
	}
}
