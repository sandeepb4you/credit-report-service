package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// The "forgot password" flow, in three calls:
//
//	1. ForgotPassword          — mails a reset OTP to a verified password identity
//	2. VerifyPasswordResetOTP  — checks the code, hands back a single-use grant
//	3. ResetPassword           — redeems the grant, sets the new password
//
// Step 2 exists so the client can move the user to a "choose a new password"
// screen the moment the code checks out, without holding a live OTP for as
// long as they take to type. See the 0012 migration for why the grant is a
// separate credential rather than the OTP itself.

const (
	// passwordResetTokenBytes is the entropy behind a reset grant. Same size as
	// a refresh token, and hashed the same way (SHA-256, see hashToken): the
	// value is full-entropy random, so there is nothing to brute-force.
	passwordResetTokenBytes = 32
	// passwordResetTokenPrefix marks the token in logs and client storage, and
	// is part of the hashed string.
	passwordResetTokenPrefix = "prt_"
	// passwordResetTTL bounds the gap between verifying the code and choosing
	// the new password. Long enough to pick a password out of a manager, short
	// enough that a grant left in a closed app is dead by the time anyone else
	// reaches the device.
	passwordResetTTL = 15 * time.Minute
)

// resetInvalidGrant is the single message every "your grant is no good" case
// returns — wrong, expired, already redeemed, or belonging to another account.
// Distinguishing them would tell an attacker which half of the pair to keep
// guessing, and the user's next step is the same in all four cases.
const resetInvalidGrant = "This password reset has expired; request a new code"

// PasswordResetGrant is what VerifyPasswordResetOTP hands back: proof that the
// caller holds the emailed code, redeemable exactly once by ResetPassword.
type PasswordResetGrant struct {
	ResetToken string    `json:"resetToken"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

// ForgotPassword mails a reset OTP to a verified password identity.
//
// An unknown address, or one that only has a Google identity, returns nil
// without sending anything: the response must not tell an anonymous caller
// which addresses have accounts. Cooldown and send-limit errors from the OTP
// service are surfaced as-is — the client needs to render "wait 43s" on the
// resend button, and reaching that state already required knowing the address.
func (s *AuthService) ForgotPassword(ctx context.Context, email string) error {
	email = normalizeEmail(email)

	ident, err := s.accounts.FindIdentity(ctx, models.ProviderPassword, email)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		// Logged without the address, like every other auth-failure path.
		slog.Warn("password reset requested for an unknown email")
		return nil
	case err != nil:
		return err
	case !ident.Verified:
		// The account never finished signup. Resetting a password nobody has
		// used would be a way to take over a half-created account, so send
		// nothing; the signup OTP is still the right route in.
		slog.Warn("password reset requested for an unverified identity",
			"account_id", ident.AccountID)
		return nil
	}

	if err := s.issueAndSend(ctx, &ident.AccountID, email, models.OtpPurposeReset); err != nil {
		return err
	}
	slog.Info("password reset code sent", "account_id", ident.AccountID)
	return nil
}

// VerifyPasswordResetOTP checks the emailed code and, on success, consumes the
// challenge and mints the single-use grant that ResetPassword redeems.
func (s *AuthService) VerifyPasswordResetOTP(
	ctx context.Context, email, otp string,
) (*PasswordResetGrant, error) {
	email = normalizeEmail(email)

	ch, err := s.accounts.FindActiveChallenge(ctx, email, models.OtpPurposeReset)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewOtpFailure("No password reset in progress; request a new code")
	}
	if err != nil {
		return nil, err
	}

	if verr := s.otp.Verify(ch, otp); verr != nil {
		// Persist the incremented attempt counter, then surface the failure —
		// otherwise the lockout never advances and the code can be guessed
		// indefinitely.
		if tx, err := s.accounts.BeginTx(ctx); err == nil {
			_ = s.accounts.UpdateChallenge(ctx, tx, ch)
			_ = tx.Commit(ctx)
		}
		aid := int64(0)
		if ch.AccountID != nil {
			aid = *ch.AccountID
		}
		slog.Warn("password reset otp verification failed",
			"account_id", aid, "attempts", ch.Attempts, "error", verr.Error())
		return nil, verr
	}

	// The challenge is keyed on the destination address, so re-resolve the
	// identity rather than trusting the (nullable, possibly legacy) account_id
	// carried on the row.
	ident, err := s.accounts.FindIdentity(ctx, models.ProviderPassword, email)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("No account found for this email")
	}
	if err != nil {
		return nil, err
	}

	token, digest, err := newPasswordResetToken()
	if err != nil {
		return nil, err
	}
	expiresAt := time.Now().UTC().Add(passwordResetTTL)

	tx, err := s.accounts.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// Burning the challenge and issuing the grant must land together: a commit
	// between them would leave either a reusable code or a grant nobody holds.
	if err := s.accounts.UpdateChallenge(ctx, tx, ch); err != nil {
		return nil, err
	}
	// Only the newest grant is live, so re-running the flow (a mistyped
	// password, a second device) cannot leave an older token redeemable.
	if err := s.accounts.InvalidatePasswordResetTokens(ctx, tx, ident.AccountID); err != nil {
		return nil, err
	}
	if _, err := s.accounts.CreatePasswordResetToken(
		ctx, tx, ident.AccountID, digest, expiresAt); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	slog.Info("password reset code verified", "account_id", ident.AccountID)
	return &PasswordResetGrant{ResetToken: token, ExpiresAt: expiresAt}, nil
}

// ResetPassword redeems a grant and writes the new password.
//
// Every device is signed out afterwards: a reset is what a user does when they
// think someone else has their password, and leaving the intruder's session
// alive would defeat the point. The user signs in again with the new password.
func (s *AuthService) ResetPassword(
	ctx context.Context, email, resetToken, newPassword string,
) error {
	email = normalizeEmail(email)
	if err := validatePassword(newPassword); err != nil {
		return err
	}

	resetToken = strings.TrimSpace(resetToken)
	if resetToken == "" {
		return apperr.NewUnauthorized(resetInvalidGrant)
	}

	grant, err := s.accounts.FindLivePasswordResetToken(ctx, hashToken(resetToken))
	if errors.Is(err, repository.ErrNotFound) {
		slog.Warn("password reset rejected: unknown, spent or expired grant")
		return apperr.NewUnauthorized(resetInvalidGrant)
	}
	if err != nil {
		return err
	}

	ident, err := s.accounts.FindIdentity(ctx, models.ProviderPassword, email)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		slog.Warn("password reset rejected: no password identity for the supplied email",
			"account_id", grant.AccountID)
		return apperr.NewUnauthorized(resetInvalidGrant)
	case err != nil:
		return err
	case ident.AccountID != grant.AccountID:
		// The grant is real but belongs to a different account than the email
		// names. Same message as an unknown grant, so pairing a stolen token
		// with guessed addresses tells the caller nothing.
		slog.Warn("password reset rejected: grant does not match the supplied email",
			"account_id", grant.AccountID)
		return apperr.NewUnauthorized(resetInvalidGrant)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	hashStr := string(hash)
	ident.PasswordHash = &hashStr

	tx, err := s.accounts.BeginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Burn the grant first: the compare-and-set inside is what makes a
	// concurrent double-redemption impossible, and it must fail the whole
	// transaction rather than run after the password is already written.
	if err := s.accounts.ConsumePasswordResetToken(ctx, tx, grant.ID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return apperr.NewUnauthorized(resetInvalidGrant)
		}
		return err
	}
	if err := s.accounts.InvalidatePasswordResetTokens(ctx, tx, grant.AccountID); err != nil {
		return err
	}
	if err := s.accounts.UpdateIdentity(ctx, tx, ident); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// Best-effort: the password is already changed, so a failure here must not
	// report the reset as failed and send the user round again. Loud in the
	// logs because it leaves a stale session alive.
	if n, err := s.sessions.RevokeAllForAccount(
		ctx, grant.AccountID, models.RevokePasswordReset); err != nil {
		slog.Error("password reset: session revocation failed",
			"account_id", grant.AccountID, "error", err)
	} else {
		slog.Info("password reset complete",
			"account_id", grant.AccountID, "sessions_revoked", n)
	}
	return nil
}

// newPasswordResetToken mints a grant and its storage digest. The plaintext is
// returned exactly once, to the caller that will hand it to the client.
func newPasswordResetToken() (token, digest string, err error) {
	buf := make([]byte, passwordResetTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	token = passwordResetTokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	return token, hashToken(token), nil
}
