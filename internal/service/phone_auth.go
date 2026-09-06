// Phone-OTP sign-in: send a code to a mobile number, verify it, and open a
// session. Mirrors the email flow (issueAndSend / VerifyEmail) but keyed on
// the phone number, with find-or-create semantics — a first-time number gets
// an account on successful verification, an existing one signs in.
//
// SMS delivery goes through sms.Sender (MSG91 in any environment with an auth
// key, a log-only stub without one — same convention as the empty-SMTP mail
// stub). Testing on a machine with no auth key relies on the stub's log line,
// or on the master OTP accepted by OTPService.Verify.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
	"credit-report-service/internal/sms"
)

// PhoneRegistered reports whether a mobile number already has an account.
//
// It exists for one screen. design/onboarding/02.html asks the sign-in screen
// to drop the consent box and the referral field for a returning user, because
// both are addressed to someone registering: consent was given at signup, and a
// referral attributes a *new* account, so for a returning user the field is a
// box that cannot do anything. The screen has no way to make that call without
// an answer from here.
//
// **This is a customer-enumeration oracle, and it is the only one in the
// service.** Every other public auth route answers identically for a known and
// an unknown identifier for exactly that reason — /auth/otp/phone/send and
// /verify, /auth/signup/start, /auth/password/forgot all refuse to sort a list
// of contacts into registered and not. This route cannot: the UI difference it
// drives *is* the disclosure, so burying the answer in the response body would
// buy nothing. Two things narrow it instead:
//
//   - It is mounted behind a per-IP rate limit (internal/server/server.go), so
//     walking the numbering plan is slow rather than free.
//   - A bool is the entire answer. No name, no status, no account id, nothing
//     that turns "this number banks with us" into a profile.
//
// A malformed number is a 400 rather than a false, so the caller can tell "not
// a mobile number" from "not one of ours" — the app needs the distinction and
// it discloses nothing, the shape of the number being the caller's own input.
func (s *AuthService) PhoneRegistered(ctx context.Context, phone string) (bool, error) {
	normalized, err := normalizePhone(phone)
	if err != nil {
		return false, err
	}
	// Deliberately the same lookup VerifyPhoneOTP branches find-or-create on:
	// if these two disagreed, the screen would hide the consent box for a
	// number that verification then registers as new, and the signup would
	// happen with no consent recorded at all.
	if _, err := s.accounts.FindByPhone(ctx, normalized); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// SendPhoneOTP mints (or refreshes) a login challenge for the number and
// "delivers" it. Unknown numbers are allowed — verification is what creates
// the account — so unlike email signup there is nothing to pre-check beyond
// the number's shape and the OTP service's cooldown / send limits.
func (s *AuthService) SendPhoneOTP(ctx context.Context, phone string) error {
	normalized, err := normalizePhone(phone)
	if err != nil {
		return err
	}

	ch, err := s.accounts.FindActiveChallenge(ctx, normalized, models.OtpPurposeLogin)
	if errors.Is(err, repository.ErrNotFound) {
		ch = nil
	} else if err != nil {
		return err
	}
	// Same expiry rule as issueAndSend: a dead challenge must not pin its
	// exhausted send_count on the number forever.
	if ch != nil && ch.ExpiresAt != nil && ch.ExpiresAt.Before(time.Now().UTC()) {
		ch = nil
	}
	if ch == nil {
		var accountID *int64
		if acc, err := s.accounts.FindByPhone(ctx, normalized); err == nil {
			accountID = &acc.ID
		}
		ch = &models.OtpChallenge{
			AccountID:   accountID,
			Channel:     models.ChannelSMS,
			Destination: normalized,
			Purpose:     models.OtpPurposeLogin,
		}
	}

	plain, err := s.otp.Issue(ch)
	if err != nil {
		return err
	}

	tx, err := s.accounts.BeginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if ch.ID == 0 {
		if err := s.accounts.CreateChallenge(ctx, tx, ch); err != nil {
			return err
		}
	} else {
		if err := s.accounts.UpdateChallenge(ctx, tx, ch); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// Deliver only after the challenge is committed, matching issueAndSend: a
	// code the user can receive but we have not stored is worse than the
	// reverse. A provider failure therefore burns this send slot — the caller
	// sees the error and can retry once the cooldown lapses.
	return s.sms.SendOTP(ctx, normalized, plain)
}

// VerifyPhoneOTP checks the code and signs the number in, creating an active
// account on first use. The session is issued exactly like every other auth
// method (issueSession), so devices/refresh/roles all behave identically.
//
// An optional referralCode attributes a newly created account to whoever owns
// that code, matching Signup. Two ordering decisions matter here:
//
//   - The code is resolved BEFORE the OTP is checked, so an invalid one is a
//     400 that leaves the challenge intact. Resolving after would burn the
//     user's code on a typo in a different field and make them wait out the
//     resend cooldown to fix it.
//   - Attribution is applied only on the create branch. This endpoint is
//     find-or-create, so a returning user is signing in, not signing up;
//     rewriting their referrer on every login would let anyone re-attribute an
//     existing customer by pasting a code into the box.
func (s *AuthService) VerifyPhoneOTP(
	ctx context.Context, phone, otp, referralCode string, dev models.DeviceInfo,
) (*AuthResult, error) {
	normalized, err := normalizePhone(phone)
	if err != nil {
		return nil, err
	}

	referrerID, referrerCode, err := s.coupons.ResolveReferral(ctx, referralCode)
	if err != nil {
		return nil, err
	}

	ch, err := s.accounts.FindActiveChallenge(ctx, normalized, models.OtpPurposeLogin)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewOtpFailure("No pending verification; request a new code")
	}
	if err != nil {
		return nil, err
	}

	if verr := s.otp.Verify(ch, otp); verr != nil {
		// Persist the incremented attempt counter, then surface the failure.
		if tx, err := s.accounts.BeginTx(ctx); err == nil {
			_ = s.accounts.UpdateChallenge(ctx, tx, ch)
			_ = tx.Commit(ctx)
		}
		slog.Warn("phone otp verification failed",
			"destination", sms.MaskPhone(normalized),
			"attempts", ch.Attempts, "error", verr.Error())
		return nil, verr
	}

	acc, err := s.accounts.FindByPhone(ctx, normalized)
	created := false
	if errors.Is(err, repository.ErrNotFound) {
		// First sign-in from this number: verification IS the registration.
		acc = &models.Account{
			Status:       models.AccountActive,
			PrimaryPhone: &normalized,
		}
		if referrerID != 0 {
			acc.ReferredByAccountID = &referrerID
			acc.ReferredByCode = &referrerCode
		}
		created = true
	} else if err != nil {
		return nil, err
	}

	tx, err := s.accounts.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := s.accounts.UpdateChallenge(ctx, tx, ch); err != nil {
		return nil, err
	}
	if created {
		if err := s.accounts.CreateAccount(ctx, tx, acc); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	if created {
		// Re-read for the columns INSERT ... RETURNING doesn't cover (role,
		// token_epoch) — the access token is minted from these.
		if acc, err = s.accounts.FindByID(ctx, acc.ID); err != nil {
			return nil, err
		}
		slog.Info("account created via phone otp",
			"account_id", acc.ID, "referred_by", referrerID)
	}

	slog.Info("phone verified", "account_id", acc.ID)
	return s.issueSession(ctx, acc, dev)
}

// normalizePhone canonicalizes an Indian mobile number to "+91XXXXXXXXXX".
// Accepts a bare 10-digit number or one already carrying the +91 prefix.
func normalizePhone(input string) (string, error) {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, input)
	// Strip the country code only when it is one (a bare 10-digit number may
	// itself start with 91 — e.g. 91987xxxxx — and must not be mangled).
	if len(digits) == 12 && strings.HasPrefix(digits, "91") {
		digits = digits[2:]
	}
	if len(digits) != 10 {
		return "", apperr.NewValidationWith("Validation failed",
			map[string]string{"phone": "phone must be a 10-digit Indian mobile number"})
	}
	return fmt.Sprintf("+91%s", digits), nil
}
