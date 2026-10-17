package service

import (
	"fmt"
	"html"
	"log/slog"
	"strings"

	mail "gopkg.in/mail.v2"
)

// LoanAssistTeamMail is what the Loan Assist team is told when a request is
// paid for: enough to pick up the phone, and where to find the documents.
type LoanAssistTeamMail struct {
	To          []string
	RequestCode string
	// Loan is the loan type and amount asked for ("Business loan · ₹10,00,000").
	Loan       string
	Customer   string
	Phone      string
	Email      string
	AmountPaid string
	PaidAt     string
	Rented     bool
	Documents  []string // slot labels, in screen order
	ConsoleURL string
}

// SendLoanAssistRequest tells the team about a newly paid Loan Assist request.
//
// The documents are listed, never attached. They include Aadhaar copies, and
// a mailbox is the worst place for one: it is forwarded, searched, synced to
// phones and kept for ever, none of it access-controlled. The team opens them
// in the admin console, behind the loan-assist permission, through presigned
// links that expire in minutes.
func (m *MailService) SendLoanAssistRequest(in LoanAssistTeamMail) error {
	if len(in.To) == 0 {
		return fmt.Errorf("send loan assist request: no recipients")
	}
	if m.cfg.Host == "" {
		slog.Info("loan-assist team email stubbed (no SMTP configured)",
			"request_code", in.RequestCode, "recipients", len(in.To))
		return nil
	}

	premises := "own premises"
	if in.Rented {
		premises = "rented premises (rent agreement attached)"
	}
	lines := []string{
		"A new Loan Assist application has been paid for. Assign it to a partner and reach out to the customer.",
		"",
		"Request ID: " + in.RequestCode,
		"Loan:       " + orDash(in.Loan),
		"Customer:   " + orDash(in.Customer),
		"Phone:      " + orDash(in.Phone),
		"Email:      " + orDash(in.Email),
		"Paid:       " + in.AmountPaid + " on " + in.PaidAt,
		"Premises:   " + premises,
		"",
		"Documents received (" + fmt.Sprint(len(in.Documents)) + "):",
	}
	for _, d := range in.Documents {
		lines = append(lines, "  - "+d)
	}
	lines = append(lines, "",
		"Open the application and its documents under Assist in the console: "+in.ConsoleURL,
		"",
		"The documents include Aadhaar copies. Do not download, forward or print "+
			"them outside the app.")
	text := strings.Join(lines, "\n") + "\n"

	var b strings.Builder
	b.WriteString("<p>A new Loan Assist application has been paid for. Assign it to a partner and reach out to the customer.</p><table>")
	row := func(k, v string) {
		b.WriteString("<tr><td style=\"padding:2px 12px 2px 0;color:#5B6B7B\">" + k +
			"</td><td><b>" + html.EscapeString(v) + "</b></td></tr>")
	}
	row("Request ID", in.RequestCode)
	row("Loan", orDash(in.Loan))
	row("Customer", orDash(in.Customer))
	row("Phone", orDash(in.Phone))
	row("Email", orDash(in.Email))
	row("Paid", in.AmountPaid+" on "+in.PaidAt)
	row("Premises", premises)
	b.WriteString("</table><p>Documents received (" + fmt.Sprint(len(in.Documents)) + "):</p><ul>")
	for _, d := range in.Documents {
		b.WriteString("<li>" + html.EscapeString(d) + "</li>")
	}
	b.WriteString("</ul><p><a href=\"" + html.EscapeString(in.ConsoleURL) +
		"\">Open the application under Assist</a></p>" +
		"<p style=\"color:#5B6B7B\">The documents include Aadhaar copies. Do not download, " +
		"forward or print them outside the app.</p>")

	msg := mail.NewMessage()
	msg.SetHeader("From", m.cfg.From)
	msg.SetHeader("To", in.To...)
	msg.SetHeader("Subject", fmt.Sprintf("Loan Assist %s: new paid request", in.RequestCode))
	msg.SetBody("text/plain", text)
	msg.AddAlternative("text/html", b.String())

	if err := m.dialer.DialAndSend(msg); err != nil {
		slog.Error("loan-assist team email send failed",
			"request_code", in.RequestCode, "error", err.Error())
		return fmt.Errorf("send loan assist team email: %w", err)
	}
	slog.Info("loan-assist team notified", "request_code", in.RequestCode, "recipients", len(in.To))
	return nil
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
