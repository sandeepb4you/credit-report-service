package service

import (
	"errors"
	"testing"
	"time"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/config"
	"credit-report-service/internal/models"
)

func testOTPConfig() config.OTPConfig {
	return config.OTPConfig{
		Length:         6,
		TTL:            5 * time.Minute,
		ResendCooldown: 60 * time.Second,
		MaxAttempts:    5,
		MaxSends:       5,
	}
}

func TestOTP_IssueAndVerify(t *testing.T) {
	s := NewOTPService(testOTPConfig())
	a := &models.RegistrationAttempt{Status: models.StatusStarted}

	plain, err := s.Issue(a)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if len(plain) != 6 {
		t.Fatalf("otp length = %d, want 6", len(plain))
	}
	if a.OTPHash == nil || a.OTPExpiresAt == nil {
		t.Fatalf("expected otp fields stamped on attempt")
	}
	if a.OTPSendCount != 1 {
		t.Fatalf("send count = %d, want 1", a.OTPSendCount)
	}

	// Wrong OTP first to exercise attempt counter.
	if err := s.Verify(a, "000000"); !isOtpFailure(err) {
		t.Fatalf("expected otp failure on wrong code, got %v", err)
	}
	if a.OTPAttempts != 1 {
		t.Fatalf("attempts after one failure = %d, want 1", a.OTPAttempts)
	}

	// Correct OTP — state advances.
	if err := s.Verify(a, plain); err != nil {
		t.Fatalf("verify correct: %v", err)
	}
	if a.Status != models.StatusOTPVerified {
		t.Fatalf("status = %s, want OTP_VERIFIED", a.Status)
	}
	if a.OTPHash != nil || a.OTPExpiresAt != nil {
		t.Fatalf("otp fields not cleared after success")
	}
}

func TestOTP_Expired(t *testing.T) {
	s := NewOTPService(testOTPConfig())
	a := &models.RegistrationAttempt{Status: models.StatusStarted}
	plain, _ := s.Issue(a)
	// Backdate expiry.
	past := time.Now().UTC().Add(-1 * time.Minute)
	a.OTPExpiresAt = &past

	if err := s.Verify(a, plain); !isOtpFailure(err) {
		t.Fatalf("expected otp failure on expired, got %v", err)
	}
}

func TestOTP_ResendCooldown(t *testing.T) {
	s := NewOTPService(testOTPConfig())
	a := &models.RegistrationAttempt{Status: models.StatusStarted}
	if _, err := s.Issue(a); err != nil {
		t.Fatalf("first issue: %v", err)
	}
	if _, err := s.Issue(a); !isConflict(err) {
		t.Fatalf("expected conflict on resend within cooldown, got %v", err)
	}
	// Move lastOtpSentAt into the past to clear cooldown.
	past := time.Now().UTC().Add(-2 * time.Minute)
	a.LastOTPSentAt = &past
	if _, err := s.Issue(a); err != nil {
		t.Fatalf("expected ok after cooldown, got %v", err)
	}
}

func TestOTP_MaxSends(t *testing.T) {
	cfg := testOTPConfig()
	cfg.MaxSends = 1
	cfg.ResendCooldown = 0
	s := NewOTPService(cfg)
	a := &models.RegistrationAttempt{Status: models.StatusStarted}
	if _, err := s.Issue(a); err != nil {
		t.Fatalf("first issue: %v", err)
	}
	if _, err := s.Issue(a); !isConflict(err) {
		t.Fatalf("expected conflict after max-sends, got %v", err)
	}
}

func TestOTP_MaxAttempts(t *testing.T) {
	cfg := testOTPConfig()
	cfg.MaxAttempts = 2
	s := NewOTPService(cfg)
	a := &models.RegistrationAttempt{Status: models.StatusStarted}
	if _, err := s.Issue(a); err != nil {
		t.Fatalf("issue: %v", err)
	}
	// Two wrong attempts allowed, third should lock.
	_ = s.Verify(a, "000000")
	_ = s.Verify(a, "111111")
	if err := s.Verify(a, "222222"); !isOtpFailure(err) {
		t.Fatalf("expected otp failure after max-attempts, got %v", err)
	}
}

func TestOTP_VerifyWrongState(t *testing.T) {
	s := NewOTPService(testOTPConfig())
	a := &models.RegistrationAttempt{Status: models.StatusOTPVerified}
	if err := s.Verify(a, "123456"); !isConflict(err) {
		t.Fatalf("expected conflict on wrong-state verify, got %v", err)
	}
}

func isOtpFailure(err error) bool {
	var of *apperr.OtpFailure
	return errors.As(err, &of)
}

func isConflict(err error) bool {
	var c *apperr.Conflict
	return errors.As(err, &c)
}
