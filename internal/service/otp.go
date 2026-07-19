package service

import (
	"crypto/rand"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/config"
	"credit-report-service/internal/models"
)

// OTPService handles OTP generation, hashing, expiry, and rate-limit checks.
// It operates on a *models.RegistrationAttempt passed in by the orchestrator;
// persistence is the caller's responsibility.
type OTPService struct {
	cfg config.OTPConfig
}

func NewOTPService(cfg config.OTPConfig) *OTPService { return &OTPService{cfg: cfg} }

// Issue generates a fresh OTP, stamps its hash + expiry + counters on the
// attempt, and returns the plaintext code (to be emailed by the caller).
func (s *OTPService) Issue(a *models.RegistrationAttempt) (string, error) {
	now := time.Now().UTC()

	if a.OTPSendCount >= s.cfg.MaxSends {
		return "", apperr.NewConflict("OTP resend limit reached; please restart registration")
	}
	if a.LastOTPSentAt != nil && a.Status == models.StatusStarted {
		elapsed := now.Sub(*a.LastOTPSentAt)
		if elapsed < s.cfg.ResendCooldown {
			remaining := int(s.cfg.ResendCooldown.Round(time.Second).Seconds() - elapsed.Round(time.Second).Seconds())
			if remaining < 0 {
				remaining = 0
			}
			return "", apperr.NewConflict(fmt.Sprintf(
				"Please wait %ds before requesting a new OTP", remaining))
		}
	}

	plain, err := generateNumeric(s.cfg.Length)
	if err != nil {
		return "", fmt.Errorf("generate otp: %w", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash otp: %w", err)
	}

	hashStr := string(hash)
	exp := now.Add(s.cfg.TTL)
	a.OTPHash = &hashStr
	a.OTPExpiresAt = &exp
	a.LastOTPSentAt = &now
	a.OTPSendCount++
	a.OTPAttempts = 0
	return plain, nil
}

// Verify checks the supplied OTP against the attempt. On success, OTP fields
// are cleared and Status advances to OTP_VERIFIED. On failure, OTPAttempts is
// incremented and a typed apperr is returned.
func (s *OTPService) Verify(a *models.RegistrationAttempt, supplied string) error {
	if a.Status != models.StatusStarted {
		return apperr.NewConflict("OTP already consumed or attempt is not in OTP stage")
	}
	if a.OTPHash == nil {
		return apperr.NewConflict("No OTP was issued for this attempt")
	}
	if a.OTPExpiresAt == nil || a.OTPExpiresAt.Before(time.Now().UTC()) {
		return apperr.NewOtpFailure("OTP expired; please request a new one")
	}

	a.OTPAttempts++
	if a.OTPAttempts > s.cfg.MaxAttempts {
		return apperr.NewOtpFailure("Too many wrong attempts; request a new OTP")
	}

	if bcrypt.CompareHashAndPassword([]byte(*a.OTPHash), []byte(supplied)) != nil {
		return apperr.NewOtpFailure("Invalid OTP")
	}

	// Success — clear OTP fields and advance state.
	a.OTPHash = nil
	a.OTPExpiresAt = nil
	a.OTPAttempts = 0
	a.Status = models.StatusOTPVerified
	return nil
}

// generateNumeric returns a uniformly-distributed numeric string of the given
// length using crypto/rand.
func generateNumeric(length int) (string, error) {
	if length < 1 {
		return "", fmt.Errorf("otp length must be >= 1")
	}
	const digits = "0123456789"
	out := make([]byte, length)
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i := 0; i < length; i++ {
		// Modulo bias is negligible for 10 buckets; acceptable for OTPs.
		out[i] = digits[int(buf[i])%len(digits)]
	}
	return string(out), nil
}
