package service

import (
	"bytes"
	"fmt"
	"html/template"
	"time"
)

// brandName is the product name shown in transactional emails.
//
// One constant for every one of them: it is the subject line of both OTP mails, the intro
// sentence, the word in the header band, the HTML title and the footer, and an email that
// calls the product something the app on the same phone does not is an email that reads as
// a phishing attempt.
const brandName = "myScorr"

// otpKind is the copy that varies between the codes this service mails. The
// layout is shared; only the words change, so a new flow adds a value here
// rather than a second template.
//
// Subject and intro are format strings taking the brand name.
type otpKind struct {
	slug    string // for logs; never a user-visible string
	subject string
	heading string
	intro   string
	// disclaimer is the "if you didn't ask for this" line. It follows the
	// expiry sentence and is the only place the two kinds genuinely differ in
	// substance: ignoring an unexpected signup code is harmless, but an
	// unexpected reset code means someone knows the address and is trying to
	// take the account, which the user needs told plainly.
	disclaimer string
}

var (
	otpKindSignup = otpKind{
		slug:    "signup",
		subject: "Your %s verification code",
		heading: "Confirm your email",
		intro:   "Use the verification code below to finish setting up your %s account.",
		disclaimer: "If you didn't request it, you can safely ignore this email — " +
			"no changes will be made to your account.",
	}

	otpKindPasswordReset = otpKind{
		slug:    "password_reset",
		subject: "Reset your %s password",
		heading: "Reset your password",
		intro: "Someone asked to reset the password on your %s account. " +
			"Enter the code below in the app to choose a new one.",
		disclaimer: "If that wasn't you, ignore this email — your password stays as it is. " +
			"Someone may know your email address, so consider signing in and " +
			"reviewing your signed-in devices.",
	}

	// otpKindAccountDeletion carries the sharpest disclaimer of the three, and
	// it is the only one that tells the user to ACT rather than to ignore the
	// mail. Ignoring an unexpected signup or reset code costs nothing; ignoring
	// this one lets a deletion nobody asked for run to completion in fourteen
	// days. The "sign in to cancel" sentence is the whole safety net behind the
	// grace period, so it belongs in the same mail as the code.
	otpKindAccountDeletion = otpKind{
		slug:    "account_deletion",
		subject: "Confirm deleting your %s account",
		heading: "Delete your account",
		intro: "Someone asked to permanently delete your %s account. " +
			"Enter the code below to confirm. Your account will then be scheduled " +
			"for deletion, and you can still stop it by signing in before the date " +
			"we show you.",
		disclaimer: "If that wasn't you, do not enter this code — and sign in to your " +
			"account to make sure nothing is scheduled. Someone may know your " +
			"contact details, so review your signed-in devices while you are there.",
	}
)

// otpEmailData is the template context for the OTP verification email.
type otpEmailData struct {
	Brand      string
	OTP        string
	ValidMins  int
	Year       int
	Heading    string
	Intro      string
	Disclaimer string
}

// otpEmailTmpl is an email-client-safe HTML template: table layout, inline
// styles, no external assets. Kept intentionally simple so it renders in Gmail,
// Outlook, and Apple Mail alike.
var otpEmailTmpl = template.Must(template.New("otp").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>{{.Brand}} &mdash; {{.Heading}}</title>
</head>
<body style="margin:0; padding:0; background-color:#0f172a; font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;">
  <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background-color:#0f172a; padding:32px 16px;">
    <tr>
      <td align="center">
        <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:480px; background-color:#ffffff; border-radius:16px; overflow:hidden;">
          <!-- Header -->
          <tr>
            <td style="background-color:#4f46e5; padding:28px 32px; text-align:center;">
              <span style="color:#ffffff; font-size:22px; font-weight:700; letter-spacing:0.5px;">{{.Brand}}</span>
            </td>
          </tr>
          <!-- Body -->
          <tr>
            <td style="padding:36px 32px 8px 32px;">
              <h1 style="margin:0 0 12px 0; font-size:20px; color:#0f172a; font-weight:600;">{{.Heading}}</h1>
              <p style="margin:0 0 24px 0; font-size:15px; line-height:22px; color:#475569;">
                {{.Intro}}
              </p>
              <!-- OTP box -->
              <table role="presentation" width="100%" cellpadding="0" cellspacing="0">
                <tr>
                  <td align="center" style="background-color:#f1f5f9; border:1px solid #e2e8f0; border-radius:12px; padding:20px 0;">
                    <span style="font-size:34px; font-weight:700; letter-spacing:10px; color:#4f46e5; font-family:'Courier New',Courier,monospace;">{{.OTP}}</span>
                  </td>
                </tr>
              </table>
              <p style="margin:20px 0 0 0; font-size:13px; line-height:20px; color:#64748b;">
                This code expires in <strong>{{.ValidMins}} minutes</strong>. {{.Disclaimer}}
              </p>
            </td>
          </tr>
          <!-- Footer -->
          <tr>
            <td style="padding:24px 32px 32px 32px;">
              <hr style="border:none; border-top:1px solid #e2e8f0; margin:0 0 16px 0;">
              <p style="margin:0; font-size:12px; line-height:18px; color:#94a3b8;">
                &copy; {{.Year}} {{.Brand}}. This is an automated message, please do not reply.
              </p>
            </td>
          </tr>
        </table>
      </td>
    </tr>
  </table>
</body>
</html>`))

// renderOTPEmail returns the HTML and plain-text bodies for one kind of OTP
// email. Both bodies are produced together so they can never drift apart —
// a mail client showing the text alternative must read the same as the HTML.
func renderOTPEmail(otp string, validMins int, kind otpKind) (htmlBody, textBody string, err error) {
	intro := fmt.Sprintf(kind.intro, brandName)
	data := otpEmailData{
		Brand:      brandName,
		OTP:        otp,
		ValidMins:  validMins,
		Year:       time.Now().Year(),
		Heading:    kind.heading,
		Intro:      intro,
		Disclaimer: kind.disclaimer,
	}
	var buf bytes.Buffer
	if err := otpEmailTmpl.Execute(&buf, data); err != nil {
		return "", "", fmt.Errorf("render otp email: %w", err)
	}
	text := fmt.Sprintf(
		"%s\n\nYour code is %s.\n\n"+
			"It expires in %d minutes. %s\n\n"+
			"— %s",
		intro, otp, validMins, kind.disclaimer, brandName)
	return buf.String(), text, nil
}
