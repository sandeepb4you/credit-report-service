package service

import (
	"fmt"
	"log"

	"gopkg.in/mail.v2"

	"credit-report-service/internal/config"
)

// Mailer sends transactional emails. The interface lets a log-only stub be
// swapped in for tests / local dev (when SMTP host is empty).
type Mailer interface {
	SendOTP(toEmail, otp string) error
}

// MailService is the SMTP-backed Mailer. When Host is empty, it logs the OTP
// instead of sending — mirroring the Spring dev behaviour.
type MailService struct {
	cfg config.MailConfig
	dialer *mail.Dialer
}

func NewMailService(cfg config.MailConfig) *MailService {
	d := mail.NewDialer(cfg.Host, cfg.Port, cfg.Username, cfg.Password)
	d.SSL = false
	return &MailService{cfg: cfg, dialer: d}
}

func (m *MailService) SendOTP(toEmail, otp string) error {
	if m.cfg.Host == "" {
		// Dev fallback: don't fail the flow, just log.
		log.Printf("[MAIL-STUB] OTP for %s: %s", toEmail, otp)
		return nil
	}
	msg := mail.NewMessage()
	msg.SetHeader("From", m.cfg.From)
	msg.SetHeader("To", toEmail)
	msg.SetHeader("Subject", "Your Credit Report registration OTP")
	msg.SetBody("text/plain", fmt.Sprintf(
		"Your verification code is %s. It expires in a few minutes. "+
			"If you did not request this, ignore this email.", otp))

	if err := m.dialer.DialAndSend(msg); err != nil {
		// Log the OTP locally so dev runs without SMTP can still complete the flow.
		log.Printf("[MAIL-FAIL] SMTP send to %s failed: %v; OTP for local testing: %s",
			toEmail, err, otp)
		return fmt.Errorf("send otp email: %w", err)
	}
	log.Printf("[MAIL] OTP email dispatched to %s", toEmail)
	return nil
}
