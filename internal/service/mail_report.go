package service

import (
	"bytes"
	"fmt"
	"log/slog"

	mail "gopkg.in/mail.v2"
)

// SendAdvancedReport emails the myScorr Advanced Report as an attachment.
//
// Its own method rather than a flag on SendCreditReport, because the covering
// letter is the difference: that one has to state the password rule, and this
// document has no password. Sending it under the other body would tell the
// reader to type a PAN into a file that will not ask for one.
//
// Worth knowing what is in the mailbox afterwards: this PDF is unencrypted by
// design (it is opened from inside the app), so an emailed copy is readable by
// anyone who reaches the mailbox. That is the trade the no-password decision
// makes, and the body says plainly that it is personal.
func (m *MailService) SendAdvancedReport(toEmail, filename string, pdf []byte) error {
	if len(pdf) == 0 {
		return fmt.Errorf("send advanced report: empty pdf")
	}

	if m.cfg.Host == "" {
		slog.Info("advanced-report email stubbed (no SMTP configured)",
			"recipient_hash", hashEmail(toEmail),
			"bytes", len(pdf),
			"filename", filename,
		)
		return nil
	}

	text := "Your myScorr advanced report is attached.\n\n" +
		"It is written by myScorr from your Experian credit file: your score, " +
		"what is driving it, every account on file, your payment record and your " +
		"plan.\n\n" +
		"It opens without a password, so keep it to yourself \u2014 it is personal.\n\n" +
		"If you did not request this, please contact support.\n"
	html := "<p>Your myScorr advanced report is attached.</p>" +
		"<p>It is written by myScorr from your Experian credit file: your score, " +
		"what is driving it, every account on file, your payment record and your plan.</p>" +
		"<p>It opens without a password, so keep it to yourself \u2014 it is personal.</p>" +
		"<p>If you did not request this, please contact support.</p>"

	msg := mail.NewMessage()
	msg.SetHeader("From", m.cfg.From)
	msg.SetHeader("To", toEmail)
	msg.SetHeader("Subject", fmt.Sprintf("Your %s advanced report", brandName))
	msg.SetBody("text/plain", text)
	msg.AddAlternative("text/html", html)
	msg.AttachReader(filename, bytes.NewReader(pdf),
		mail.SetHeader(map[string][]string{"Content-Type": {"application/pdf"}}))

	if err := m.dialer.DialAndSend(msg); err != nil {
		slog.Error("advanced-report email send failed",
			"recipient_hash", hashEmail(toEmail),
			"error", scrubEmail(err.Error(), toEmail),
		)
		return fmt.Errorf("send advanced report email: %w", err)
	}
	slog.Info("advanced report emailed",
		"recipient_hash", hashEmail(toEmail), "bytes", len(pdf))
	return nil
}

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
