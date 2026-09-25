package service

import (
	"bytes"
	"fmt"
	"html"
	"log/slog"

	mail "gopkg.in/mail.v2"
)

// SetInvoiceFrom sets the From header invoice mail goes out under (e.g. a
// billing@ address), leaving every other mail on mail.from. Empty keeps
// mail.from. The SMTP account must be permitted to send as it — Gmail, for one,
// rewrites a From it has not been told about.
func (m *MailService) SetInvoiceFrom(from string) { m.invoiceFrom = from }

// SendInvoice emails one invoice PDF.
//
// Its own method for the same reason each report has one: the covering letter
// is what differs. It names the invoice and the amount so the mail can be found
// again by searching the mailbox for either, and a specimen says plainly in the
// subject that it is not a bill.
func (m *MailService) SendInvoice(in InvoiceMail) error {
	if len(in.PDF) == 0 {
		return fmt.Errorf("send invoice: empty pdf")
	}
	if m.cfg.Host == "" {
		slog.Info("invoice email stubbed (no SMTP configured)",
			"recipient_hash", hashEmail(in.To), "invoice", in.Number, "bytes", len(in.PDF))
		return nil
	}

	subject := fmt.Sprintf("Your %s invoice %s", brandName, in.Number)
	lead := fmt.Sprintf("Your tax invoice %s for %s (%s, GST included) is attached.",
		in.Number, in.PlanName, in.Amount)
	if in.Specimen {
		subject = fmt.Sprintf("Specimen %s invoice %s (test payment)", brandName, in.Number)
		lead = fmt.Sprintf("A specimen invoice %s for %s is attached. It comes from a test payment: "+
			"no money was charged and it is not a tax invoice.", in.Number, in.PlanName)
	}
	text := lead + "\n\nThank you for choosing " + brandName + ".\n\n" +
		"Questions about this invoice? Reply to this email or write to us from the app.\n"
	body := "<p>" + html.EscapeString(lead) + "</p>" +
		"<p>Thank you for choosing " + brandName + ".</p>" +
		"<p>Questions about this invoice? Reply to this email or write to us from the app.</p>"

	from := m.cfg.From
	if m.invoiceFrom != "" {
		from = m.invoiceFrom
	}
	msg := mail.NewMessage()
	msg.SetHeader("From", from)
	msg.SetHeader("To", in.To)
	msg.SetHeader("Subject", subject)
	msg.SetBody("text/plain", text)
	msg.AddAlternative("text/html", body)
	msg.AttachReader(in.Filename, bytes.NewReader(in.PDF),
		mail.SetHeader(map[string][]string{"Content-Type": {"application/pdf"}}))

	if err := m.dialer.DialAndSend(msg); err != nil {
		slog.Error("invoice email send failed",
			"recipient_hash", hashEmail(in.To), "invoice", in.Number,
			"error", scrubEmail(err.Error(), in.To))
		return fmt.Errorf("send invoice email: %w", err)
	}
	return nil
}
