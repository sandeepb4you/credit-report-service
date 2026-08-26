// Package sms delivers transactional SMS behind an interface, so a log-only
// stub can stand in for the real provider in tests / local dev — mirroring the
// Mailer, payments.Gateway and ocr.Provider conventions.
//
// Only one message type exists today: the phone sign-in OTP. Keep it that way
// unless a new message genuinely needs different copy — every send goes through
// a DLT-approved template on the provider side, so "one method per template" is
// the shape that matches reality.
package sms

import (
	"context"
	"fmt"
	"strings"
)

// Sender delivers a one-time code by SMS.
type Sender interface {
	// SendOTP delivers otp to an Indian mobile number in "+91XXXXXXXXXX" form.
	// A non-nil error means the provider rejected or could not accept the
	// message; the caller decides whether that fails the whole request.
	//
	// ctx carries the request deadline, matching the other outbound HTTP
	// clients (payments, s3store, digitap) rather than the SMTP-era Mailer.
	SendOTP(ctx context.Context, phone, otp string) error
	// IsStub reports whether this sender only writes to the log. Surfaced in
	// the boot snapshot so an operator can see at a glance that no real SMS
	// will go out.
	IsStub() bool
}

// SendError carries the provider's HTTP status and response body so the service
// layer can log the detail without leaking it to clients.
//
// Body is the provider's own text. It never contains the OTP (we send that in a
// template variable, and no provider echoes it), but it can name the template
// or sender ID, which is why it goes to the log rather than to the caller.
type SendError struct {
	StatusCode int
	Body       string
}

func (e *SendError) Error() string {
	return fmt.Sprintf("msg91: HTTP %d: %s", e.StatusCode, e.Body)
}

// MaskPhone reduces a number to "+91******3210" for logging.
//
// The mail path hashes the recipient outright, but a phone number is the one
// identifier support staff have to match a "my OTP never arrived" ticket
// against a log line, and the last four digits are what the user can read back.
// Anything more than that — the full number — would put a plaintext mobile in
// every log sink, so this is the deliberate middle ground.
func MaskPhone(phone string) string {
	if len(phone) < 4 {
		return "****"
	}
	last4 := phone[len(phone)-4:]
	prefix := phone[:len(phone)-4]
	// Keep a leading "+91" readable; mask the subscriber digits in between.
	cc := ""
	if strings.HasPrefix(prefix, "+91") {
		cc, prefix = "+91", prefix[3:]
	}
	return cc + strings.Repeat("*", len(prefix)) + last4
}
