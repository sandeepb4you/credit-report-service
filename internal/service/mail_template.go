package service

import (
	"bytes"
	"fmt"
	"html/template"
	"time"
)

// brandName is the product name shown in transactional emails.
const brandName = "Scorr.club"

// otpEmailData is the template context for the OTP verification email.
type otpEmailData struct {
	Brand     string
	OTP       string
	ValidMins int
	Year      int
}

// otpEmailTmpl is an email-client-safe HTML template: table layout, inline
// styles, no external assets. Kept intentionally simple so it renders in Gmail,
// Outlook, and Apple Mail alike.
var otpEmailTmpl = template.Must(template.New("otp").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>{{.Brand}} verification code</title>
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
              <h1 style="margin:0 0 12px 0; font-size:20px; color:#0f172a; font-weight:600;">Confirm your email</h1>
              <p style="margin:0 0 24px 0; font-size:15px; line-height:22px; color:#475569;">
                Use the verification code below to finish setting up your {{.Brand}} account.
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
                This code expires in <strong>{{.ValidMins}} minutes</strong>. If you didn't request it, you can safely ignore this email &mdash; no changes will be made to your account.
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

// renderOTPEmail returns the HTML and plain-text bodies for the OTP email.
func renderOTPEmail(otp string, validMins int) (htmlBody, textBody string, err error) {
	data := otpEmailData{
		Brand:     brandName,
		OTP:       otp,
		ValidMins: validMins,
		Year:      time.Now().Year(),
	}
	var buf bytes.Buffer
	if err := otpEmailTmpl.Execute(&buf, data); err != nil {
		return "", "", fmt.Errorf("render otp email: %w", err)
	}
	text := fmt.Sprintf(
		"Your %s verification code is %s.\n\n"+
			"It expires in %d minutes. If you didn't request this, you can ignore this email.\n\n"+
			"— %s",
		brandName, otp, validMins, brandName)
	return buf.String(), text, nil
}
