package service

import (
	"fmt"
	"log"
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
		// Dev fallback: don't fail the flow, just log.
		log.Printf("[MAIL-STUB] %s OTP for %s: %s (valid %dm)", brandName, toEmail, otp, validMins)
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
		// Log the OTP locally so dev runs without SMTP can still complete the flow.
		log.Printf("[MAIL-FAIL] SMTP send to %s failed: %v; OTP for local testing: %s",
			toEmail, err, otp)
		return fmt.Errorf("send otp email: %w", err)
	}
	log.Printf("[MAIL] %s OTP email dispatched to %s", brandName, toEmail)
	return nil
}
