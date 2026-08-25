package sms

import (
	"context"
	"log/slog"
)

// StubSender is the local-development sender, selected by sms.provider: "stub"
// or by an empty MSG91 auth key. It sends nothing at all, mirroring the
// empty-SMTP mail stub and the Cashfree stub gateway.
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
// To complete a sign-in with this sender in place, configure auth.otp.master-code
// (see config.yaml) rather than trying to recover the code from here — it is not
// written anywhere.
func (*StubSender) SendOTP(_ context.Context, phone, _ string) error {
	slog.Warn("sms otp not sent (stub sender); sign in with auth.otp.master-code",
		"destination", MaskPhone(phone))
	return nil
}
