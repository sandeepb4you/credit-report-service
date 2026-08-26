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
	// Non-zero once the branch below decides to move an identity off another
	// account. UpdateIdentity cannot do that — it does not write account_id — so
	// the write below has to take the dedicated path.
	var reassignFrom int64
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
		claimable, cErr := s.identityIsUnproven(ctx, ident)
		if cErr != nil {
			return nil, cErr
		}
		if !claimable {
			return nil, apperr.NewConflict("This email is already registered to another account")
		}
		// The code just proved this mailbox, and the row holding the address
		// never proved anything. Move it rather than deleting and re-inserting:
		// the unique (provider, provider_subject) key means a second row for the
		// same address cannot exist, so a transfer is the only shape that works.
		//
		// The password hash MUST be dropped. It was chosen by whoever ran the
		// abandoned signup, who never demonstrated they own the address — keeping
		// it would let them sign straight into this account. Nil is also what a
		// fresh link produces: login keeps rejecting the address while
		// password/forgot can set one, which is how a phone-first user gives
		// themselves a password.
		slog.Info("email link: reassigning an unverified signup identity",
			"account_id", accountID, "from_account_id", ident.AccountID,
			"destination", hashEmail(email))
		reassignFrom = ident.AccountID
		ident.AccountID = accountID
		ident.Email = &email
		ident.PasswordHash = nil
		ident.Verified = true
		ident.VerifiedAt = &now
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
	switch {
	case ident.ID == 0:
		if err := s.accounts.CreateIdentity(ctx, tx, ident); err != nil {
			if errors.Is(err, repository.ErrConflict) {
				return nil, apperr.NewConflict("This email is already registered to another account")
			}
			return nil, err
		}
	case reassignFrom != 0:
		// Moving the row, not editing it in place. UpdateIdentity would leave
		// account_id untouched and quietly finish: the address would stay on the
		// abandoned account while primary_email was set on this one, leaving two
		// accounts disagreeing about who owns it.
		if err := s.accounts.ReassignIdentity(ctx, tx, ident.ID, accountID, now); err != nil {
			return nil, err
		}
	default:
		if err := s.accounts.UpdateIdentity(ctx, tx, ident); err != nil {
			return nil, err
		}
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
		claimable, cErr := s.identityIsUnproven(ctx, ident)
		if cErr != nil {
			return cErr
		}
		if claimable {
			// An abandoned signup, not an owner. Let the flow continue: the
			// caller still has to prove the mailbox with the emailed code.
			slog.Info("email link: address held only by an unverified signup, allowing the attempt",
				"account_id", accountID, "destination", hashEmail(email))
			return nil
		}
		slog.Warn("email link rejected: identity belongs to another account",
			"account_id", accountID, "destination", hashEmail(email))
		return apperr.NewConflict("This email is already registered to another account")
	case err != nil && !errors.Is(err, repository.ErrNotFound):
		return err
	}
	return nil
}

// identityIsUnproven reports whether ident is a claim nobody ever substantiated:
// an unverified row whose account has no primary email either.
//
// Signup writes the auth_identities row BEFORE the code is checked (see
// AuthService.Signup), so anyone can type an address they do not own and, until
// this, permanently prevent its real owner from linking it to their own account.
// Registering with an email, never verifying it, then signing up by phone was
// enough to lock yourself out of your own address.
//
// Signup already takes this view — an existing unverified identity there is
// updated and re-verified rather than refused. This makes the link flow agree.
//
// Requiring primary_email to be nil as well as verified=false is what keeps it
// narrow: primary_email is only ever set by a completed verification, so its
// absence means the address has never been proven for that account by anyone.
func (s *AuthService) identityIsUnproven(ctx context.Context, ident *models.AuthIdentity) (bool, error) {
	if ident.Verified {
		return false, nil
	}
	owner, err := s.accounts.FindByID(ctx, ident.AccountID)
	if errors.Is(err, repository.ErrNotFound) {
		// The identity outlived its account. Nothing owns the address.
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return owner.PrimaryEmail == nil, nil
}
