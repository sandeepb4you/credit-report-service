package service

import (
	"bytes"
	"fmt"
	"log/slog"

	mail "gopkg.in/mail.v2"
)

// SendCreditReport emails the encrypted report PDF as an attachment.
//
// Kept apart from sendOTP rather than folded into it: that path is deliberately
// uniform so a new OTP kind cannot accidentally start logging an address, and an
// attachment is a different shape of message. What the two share — the empty-host
// dev stub, hashing the recipient instead of logging it, scrubbing the address
// out of SMTP error text — is repeated here on purpose.
//
// The body states the password RULE, never the password. A covering email gets
// forwarded, quoted and left in mailboxes; putting "your password is X" next to
// the file it opens would make the encryption decorative.
func (m *MailService) SendCreditReport(toEmail, filename string, pdf []byte) error {
	if len(pdf) == 0 {
		return fmt.Errorf("send credit report: empty pdf")
	}

	if m.cfg.Host == "" {
		slog.Info("credit-report email stubbed (no SMTP configured)",
			"recipient_hash", hashEmail(toEmail),
			"bytes", len(pdf),
			"filename", filename,
		)
		return nil
	}

	text := "Your credit report is attached.\n\n" +
		ReportPDFPasswordHint + "\n\n" +
		"If you did not request this, please contact support.\n"
	html := "<p>Your credit report is attached.</p>" +
		"<p>" + ReportPDFPasswordHint + "</p>" +
		"<p>If you did not request this, please contact support.</p>"

	msg := mail.NewMessage()
	msg.SetHeader("From", m.cfg.From)
	msg.SetHeader("To", toEmail)
	msg.SetHeader("Subject", fmt.Sprintf("Your %s credit report", brandName))
	msg.SetBody("text/plain", text)
	msg.AddAlternative("text/html", html)
	msg.AttachReader(filename, bytes.NewReader(pdf),
		mail.SetHeader(map[string][]string{"Content-Type": {"application/pdf"}}))

	if err := m.dialer.DialAndSend(msg); err != nil {
		slog.Error("credit-report email send failed",
			"recipient_hash", hashEmail(toEmail),
			"error", scrubEmail(err.Error(), toEmail),
		)
		return fmt.Errorf("send credit report email: %w", err)
	}
	slog.Info("credit-report emailed",
		"recipient_hash", hashEmail(toEmail), "bytes", len(pdf))
	return nil
}
