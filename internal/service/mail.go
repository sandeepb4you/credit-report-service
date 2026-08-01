package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gopkg.in/mail.v2"

	"credit-report-service/internal/config"
)

// Mailer sends transactional emails. The interface lets a log-only stub be
// swapped in for tests / local dev (when SMTP host is empty).
type Mailer interface {
	SendOTP(toEmail, otp string) error
}

// MailService is the SMTP-backed Mailer. When Host is empty, it logs the OTP
// instead of sending — mirroring the dev behaviour.
type MailService struct {
	cfg         config.MailConfig
	dialer      *mail.Dialer
	otpValidity time.Duration
}

// NewMailService builds the SMTP mailer. otpValidity is the OTP time-to-live,
// surfaced in the email copy ("expires in N minutes").
func NewMailService(cfg config.MailConfig, otpValidity time.Duration) *MailService {
	d := mail.NewDialer(cfg.Host, cfg.Port, cfg.Username, cfg.Password)
	d.SSL = false
	return &MailService{cfg: cfg, dialer: d, otpValidity: otpValidity}
}

func (m *MailService) SendOTP(toEmail, otp string) error {
	validMins := int(m.otpValidity.Round(time.Minute).Minutes())
	if validMins < 1 {
		validMins = 1
	}

	if m.cfg.Host == "" {
		// Dev fallback: don't fail the flow. Log only a non-reversible hash of
		// the recipient — never the OTP or the email address.
		slog.Info("otp email stubbed (no SMTP configured)",
			"recipient_hash", hashEmail(toEmail),
			"valid_minutes", validMins,
		)
		return nil
	}

	htmlBody, textBody, err := renderOTPEmail(otp, validMins)
	if err != nil {
		return err
	}

	msg := mail.NewMessage()
	msg.SetHeader("From", m.cfg.From)
	msg.SetHeader("To", toEmail)
	msg.SetHeader("Subject", fmt.Sprintf("Your %s verification code", brandName))
	// Plain text as the main body, HTML as the alternative -> multipart/alternative.
	msg.SetBody("text/plain", textBody)
	msg.AddAlternative("text/html", htmlBody)

	if err := m.dialer.DialAndSend(msg); err != nil {
		// Log the failure without the OTP value or recipient address. SMTP
		// servers sometimes echo the envelope recipient in their error text, so
		// scrub any occurrence of the address before logging.
		slog.Error("otp email send failed",
			"recipient_hash", hashEmail(toEmail),
			"error", scrubEmail(err.Error(), toEmail),
		)
		return fmt.Errorf("send otp email: %w", err)
	}
	slog.Info("otp email dispatched", "recipient_hash", hashEmail(toEmail))
	return nil
}

// scrubEmail replaces any occurrence of email in s with "[redacted]", so an
// SMTP bounce message that echoes the recipient can't leak it into the logs.
func scrubEmail(s, email string) string {
	if email == "" {
		return s
	}
	return strings.ReplaceAll(s, email, "[redacted]")
}

// hashEmail returns a short, non-reversible identifier for an email address so
// the recipient can be correlated in logs without the address itself being
// written. It is a salted SHA-256 truncated to 12 hex chars; the salt is fixed
// per-process so the same address is stable within a run but not reversible
// from the digest alone.
func hashEmail(email string) string {
	sum := sha256.Sum256([]byte(emailHashSalt + email))
	return hex.EncodeToString(sum[:])[:12]
}

// emailHashSalt mixes the hash so a precomputed rainbow table for emails can't
// reverse the digest. Constant is fine; this only needs to defeat lookup
// tables, not a targeted adversary (the logs stay inside the operator's sink).
const emailHashSalt = "crs-log-v1"
