// Linking an email address to an account that signed up by phone.
//
// The mirror of phone_register.go, and optional where that one is mandatory: a
// phone signup is already complete without an email — the number is what PAN
// verification and every paid product are looked up against. An email buys the
// user a second way in, somewhere to receive the report, and (via the
// forgot-password flow) a password if they ever want one.
//
// Nothing is written until the OTP passes. Until then the address is only a
// destination on a challenge row, so an unverified address can never end up on
// an account or block anyone else from registering it.
package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// SendEmailLinkOTP mails a verification code to the address the signed-in
// account wants to link.
//
// An address already registered elsewhere is refused here rather than at verify
// time, because the alternative is mailing a code to a stranger on any signed-in
// user's say-so. As with the phone equivalent, answering that does let an
// authenticated caller probe which addresses are registered — the enumeration
// the public forgot-password flow is careful to avoid. Both endpoints want a
// per-account send cap before launch; the per-destination cooldown in OTPService
// does not cover a caller walking through fresh addresses.
func (s *AuthService) SendEmailLinkOTP(ctx context.Context, accountID int64, email string) error {
	// Shape is the handler's check, as everywhere else in this package.
	email = normalizeEmail(email)

	acc, err := s.accounts.FindByID(ctx, accountID)
	if errors.Is(err, repository.ErrNotFound) {
		return apperr.NewNotFound("Account not found")
	}
	if err != nil {
		return err
	}
	// Changing an address that is already linked is deliberately not this
	// endpoint. It would orphan the old auth_identities row, which still holds
	// the unique (provider, provider_subject) key and would go on blocking
	// signups on an address the account no longer claims. Removing it is a
	// separate decision — it may be the only password credential on the account.
	if acc.PrimaryEmail != nil {
		if *acc.PrimaryEmail == email {
			return apperr.NewConflict("This email is already linked to your account")
		}
		return apperr.NewConflict("An email is already linked to this account")
	}
	if err := s.emailIsFree(ctx, email, accountID); err != nil {
		return err
	}

	plain, err := s.issueAddIdentityChallenge(ctx, accountID, models.ChannelEmail, email)
	if err != nil {
		return err
	}
	return s.mailer.SendOTP(email, plain)
}

// VerifyEmailLink checks the code and attaches the address to the calling
// account. Returns the same profile shape as GET /profile so the client can swap
// its cached state without a follow-up read.
//
// The identity row is created with a NULL password hash: the address is proven,
// but no password has ever been chosen for it. Login therefore keeps rejecting
// it ("Invalid email or password" — PasswordHash nil never compares equal),
// while forgot-password accepts it, because the row is verified. That is the
// route by which a phone-first user can give themselves a password later, and
// it is why the row is written rather than only accounts.primary_email: without
// it, someone else could sign up on this address and take it.
//
// No new session is issued. The access token carries the account id, role and
// token epoch, none of which this changes.
func (s *AuthService) VerifyEmailLink(
	ctx context.Context, accountID int64, email, otp string,
) (*models.Profile, error) {
	email = normalizeEmail(email)

	ch, err := s.verifyAddIdentityChallenge(ctx, accountID, email, otp, hashEmail(email))
	if err != nil {
		return nil, err
	}

	acc, err := s.accounts.FindByID(ctx, accountID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("Account not found")
	}
	if err != nil {
		return nil, err
	}
	if acc.PrimaryEmail != nil && *acc.PrimaryEmail != email {
		return nil, apperr.NewConflict("An email is already linked to this account")
	}
	// Re-checked after the code passes as well as before it was sent: the
	// address could have been claimed in between, and the unique index on
	// primary_email would fail the write with a 500 rather than something the
	// user can act on.
	if err := s.emailIsFree(ctx, email, accountID); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	ident, err := s.accounts.FindIdentity(ctx, models.ProviderPassword, email)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		ident = &models.AuthIdentity{
			AccountID:       accountID,
			Provider:        models.ProviderPassword,
			ProviderSubject: email,
			Email:           &email,
			// PasswordHash stays nil — see the doc comment.
			Verified:   true,
			VerifiedAt: &now,
		}
	case err != nil:
		return nil, err
	case ident.AccountID != accountID:
		return nil, apperr.NewConflict("This email is already registered to another account")
	default:
		// An abandoned unverified signup on this address, by this same account.
		// Verifying it here is legitimate: the code just proved the mailbox.
		ident.Email = &email
		ident.Verified = true
		ident.VerifiedAt = &now
	}

	acc.PrimaryEmail = &email
	if acc.Status == models.AccountPending {
		acc.Status = models.AccountActive
	}

	tx, err := s.accounts.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := s.accounts.UpdateChallenge(ctx, tx, ch); err != nil {
		return nil, err
	}
	if ident.ID == 0 {
		if err := s.accounts.CreateIdentity(ctx, tx, ident); err != nil {
			if errors.Is(err, repository.ErrConflict) {
				return nil, apperr.NewConflict("This email is already registered to another account")
			}
			return nil, err
		}
	} else if err := s.accounts.UpdateIdentity(ctx, tx, ident); err != nil {
		return nil, err
	}
	if err := s.accounts.UpdateAccount(ctx, tx, acc); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			return nil, apperr.NewConflict("This email is already registered to another account")
		}
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	// The linked address may be on the admin allowlist, exactly as it would be
	// had the account signed up with it. Outside the tx on purpose: SetRole is
	// idempotent and a failure must not unwind a completed link.
	s.applyAdminRole(ctx, acc)

	slog.Info("email linked", "account_id", acc.ID)
	return s.profileFor(ctx, acc)
}

// emailIsFree reports a 409 if any account other than accountID already holds
// this address, whether as its primary_email or as any identity (a Google login
// carries one too, and would collide on nothing until the unique index fired).
func (s *AuthService) emailIsFree(ctx context.Context, email string, accountID int64) error {
	owner, err := s.accounts.FindByEmail(ctx, email)
	switch {
	case err == nil && owner.ID != accountID:
		slog.Warn("email link rejected: address belongs to another account",
			"account_id", accountID, "destination", hashEmail(email))
		return apperr.NewConflict("This email is already registered to another account")
	case err != nil && !errors.Is(err, repository.ErrNotFound):
		return err
	}

	ident, err := s.accounts.FindIdentityByEmail(ctx, email)
	switch {
	case err == nil && ident.AccountID != accountID:
		slog.Warn("email link rejected: identity belongs to another account",
			"account_id", accountID, "destination", hashEmail(email))
		return apperr.NewConflict("This email is already registered to another account")
	case err != nil && !errors.Is(err, repository.ErrNotFound):
		return err
	}
	return nil
}
