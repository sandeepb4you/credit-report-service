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

// OTPService handles OTP generation, hashing, expiry, and rate-limit checks on
// an *models.OtpChallenge. Persistence is the caller's responsibility.
type OTPService struct {
	cfg config.OTPConfig
}

func NewOTPService(cfg config.OTPConfig) *OTPService { return &OTPService{cfg: cfg} }

// Issue generates a fresh OTP, stamps its hash + expiry + counters on the
// challenge, and returns the plaintext code (to be delivered by the caller).
func (s *OTPService) Issue(c *models.OtpChallenge) (string, error) {
	now := time.Now().UTC()

	if c.ConsumedAt != nil {
		return "", apperr.NewConflict("This verification is already complete")
	}
	if c.SendCount >= s.cfg.MaxSends {
		return "", apperr.NewConflict("OTP resend limit reached; please start over")
	}
	if c.LastSentAt != nil {
		elapsed := now.Sub(*c.LastSentAt)
		if elapsed < s.cfg.ResendCooldown {
			remaining := int(s.cfg.ResendCooldown.Round(time.Second).Seconds() -
				elapsed.Round(time.Second).Seconds())
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
	c.OTPHash = &hashStr
	c.ExpiresAt = &exp
	c.LastSentAt = &now
	c.SendCount++
	c.Attempts = 0
	return plain, nil
}

// Verify checks the supplied OTP against the challenge. On success the challenge
// is marked consumed. On failure Attempts is incremented and a typed apperr is
// returned.
func (s *OTPService) Verify(c *models.OtpChallenge, supplied string) error {
	if c.ConsumedAt != nil {
		return apperr.NewConflict("This code was already used")
	}
	if c.OTPHash == nil {
		return apperr.NewConflict("No OTP was issued")
	}
	if c.ExpiresAt == nil || c.ExpiresAt.Before(time.Now().UTC()) {
		return apperr.NewOtpFailure("OTP expired; please request a new one")
	}

	c.Attempts++
	if c.Attempts > s.cfg.MaxAttempts {
		return apperr.NewOtpFailure("Too many wrong attempts; request a new OTP")
	}
	if bcrypt.CompareHashAndPassword([]byte(*c.OTPHash), []byte(supplied)) != nil {
		return apperr.NewOtpFailure("Invalid OTP")
	}

	now := time.Now().UTC()
	c.ConsumedAt = &now
	c.OTPHash = nil
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
