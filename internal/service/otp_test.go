package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/config"
	"credit-report-service/internal/models"
)

// otpTestCfg is a permissive config so tests can exercise specific guards by
// constructing challenges directly.
func otpTestCfg() config.OTPConfig {
	return config.OTPConfig{
		Length:         6,
		TTL:            10 * time.Minute,
		ResendCooldown: time.Second,
		MaxAttempts:    3,
		MaxSends:       3,
	}
}

func newOTPSvc() *OTPService { return NewOTPService(otpTestCfg()) }

// freshChallenge is a blank challenge ready for its first Issue.
func freshChallenge() *models.OtpChallenge {
	return &models.OtpChallenge{Channel: models.ChannelEmail, Destination: "a@b.com", Purpose: models.OtpPurposeSignup}
}

// TestIssue_AndVerify_HappyPath: a freshly issued code verifies cleanly.
func TestIssue_AndVerify_HappyPath(t *testing.T) {
	s := newOTPSvc()
	c := freshChallenge()
	plain, err := s.Issue(c)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(plain) != 6 {
		t.Errorf("otp length = %d, want 6", len(plain))
	}
	if c.OTPHash == nil || c.ExpiresAt == nil {
		t.Error("Issue did not stamp hash/expiry")
	}
	if c.Attempts != 0 || c.SendCount != 1 {
		t.Errorf("post-Issue counters: attempts=%d sendCount=%d", c.Attempts, c.SendCount)
	}
	if err := s.Verify(c, plain); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if c.ConsumedAt == nil {
		t.Error("Verify did not mark consumed")
	}
	if c.OTPHash != nil {
		t.Error("Verify should clear the hash after success")
	}
}

// TestVerify_AlreadyConsumed: a used challenge can't be replayed.
func TestVerify_AlreadyConsumed(t *testing.T) {
	s := newOTPSvc()
	c := freshChallenge()
	plain, _ := s.Issue(c)
	if err := s.Verify(c, plain); err != nil {
		t.Fatalf("first verify: %v", err)
	}
	err := s.Verify(c, plain)
	if !isConflict(err) {
		t.Fatalf("replay should be Conflict, got %v", err)
	}
}

// TestVerify_Expired: an expired challenge rejects verification.
func TestVerify_Expired(t *testing.T) {
	s := newOTPSvc()
	c := freshChallenge()
	_, _ = s.Issue(c)
	// Force the expiry into the past.
	past := time.Now().UTC().Add(-time.Minute)
	c.ExpiresAt = &past
	err := s.Verify(c, "000000")
	if !isOtpFailure(err) {
		t.Fatalf("expired should be OtpFailure, got %v", err)
	}
}

// TestVerify_TooManyAttempts: wrong codes lock out after MaxAttempts.
func TestVerify_TooManyAttempts(t *testing.T) {
	s := newOTPSvc()
	c := freshChallenge()
	_, _ = s.Issue(c)
	// Burn through the allowed attempts with wrong codes.
	var lastErr error
	for i := 0; i < s.cfg.MaxAttempts+1; i++ {
		lastErr = s.Verify(c, "000000")
	}
	if !isOtpFailure(lastErr) {
		t.Fatalf("after MaxAttempts want OtpFailure, got %v", lastErr)
	}
}

// TestIssue_ResendCooldown: a re-issue within the cooldown is rejected.
func TestIssue_ResendCooldown(t *testing.T) {
	s := newOTPSvc()
	c := freshChallenge()
	if _, err := s.Issue(c); err != nil {
		t.Fatalf("first issue: %v", err)
	}
	if _, err := s.Issue(c); !isConflict(err) {
		t.Fatalf("cooldown re-issue want Conflict, got %v", err)
	}
}

// TestIssue_MaxSends: hitting the send cap rejects further issues.
func TestIssue_MaxSends(t *testing.T) {
	s := newOTPSvc()
	// Pre-load a challenge already at the send cap.
	c := freshChallenge()
	c.SendCount = s.cfg.MaxSends
	last := time.Now().UTC().Add(-time.Hour)
	c.LastSentAt = &last
	if _, err := s.Issue(c); !isConflict(err) {
		t.Fatalf("over MaxSends want Conflict, got %v", err)
	}
}

// TestIssue_NoOTPHashOnVerify: verifying a challenge that was never issued
// returns a Conflict, not a panic.
func TestIssue_NoOTPHashOnVerify(t *testing.T) {
	s := newOTPSvc()
	c := freshChallenge()
	if err := s.Verify(c, "123456"); !isConflict(err) {
		t.Fatalf("verify never-issued want Conflict, got %v", err)
	}
}

// TestIssue_CooldownRemainingMessage: the cooldown error carries a "wait Ns"
// hint rather than failing opaquely.
func TestIssue_CooldownRemainingMessage(t *testing.T) {
	s := newOTPSvc()
	c := freshChallenge()
	_, _ = s.Issue(c)
	_, err := s.Issue(c)
	if err == nil || !contains(err.Error(), "wait") {
		t.Errorf("cooldown error should mention wait, got: %v", err)
	}
}

// ---- helpers ----

func isConflict(err error) bool {
	var cf *apperr.Conflict
	return errors.As(err, &cf)
}

func isOtpFailure(err error) bool {
	var of *apperr.OtpFailure
	return errors.As(err, &of)
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
