package sms

import (
	"context"
	"log/slog"
)

// StubSender is the dev fallback used when no MSG91 auth key is configured. It
// writes the code to the log instead of sending, mirroring the empty-SMTP mail
// stub and the Cashfree stub gateway.
type StubSender struct{}

func NewStubSender() *StubSender { return &StubSender{} }

func (*StubSender) IsStub() bool { return true }

// SendOTP records that a send was skipped, WITHOUT the code.
//
// The OTP is deliberately absent: a one-time code in a log file is a live
// credential sitting in plaintext wherever those logs end up, and "it is only
// the dev stub" is not a boundary that holds — log sinks get shared, tailed and
// shipped. The number is masked for the same reason the real sender masks it.
//
// To complete a sign-in with no SMS provider configured, use the master OTP
// accepted by OTPService.Verify rather than reading the code from here.
func (*StubSender) SendOTP(_ context.Context, phone, _ string) error {
	slog.Warn("sms otp not sent (no MSG91 auth key configured); use the master OTP to sign in",
		"destination", MaskPhone(phone))
	return nil
}
