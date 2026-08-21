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

// SendOTP logs the code in full.
//
// This is the one place in the service that deliberately writes a plaintext OTP
// and an unmasked number to the log — it is the only way to complete a phone
// sign-in on a machine with no SMS provider, which is exactly what the stub is
// for. It stays safe only because a configured auth key replaces this sender
// entirely, so the branch is unreachable in any environment that can send.
func (*StubSender) SendOTP(_ context.Context, phone, otp string) error {
	slog.Warn("sms otp stubbed (no MSG91 auth key configured) — code not sent, logged instead",
		"destination", phone, "otp", otp)
	return nil
}
