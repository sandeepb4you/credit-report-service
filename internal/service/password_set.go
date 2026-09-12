// Giving an account its first password, from inside a live session.
//
// The companion of email_link.go. Linking an email writes an auth_identities
// row with a NULL password hash — the address is proven, but nobody has ever
// chosen a password for it — so until now the only way to fill that hash was to
// sign out and walk the forgot-password flow, which mails a code to the address
// the user just proved a minute earlier. This endpoint lets the link flow finish
// the job in one sitting.
//
// Deliberately NOT a password *change*. It refuses an identity that already has
// a hash, so a stolen access token cannot be turned into a permanent credential:
// changing a password that exists still costs a code emailed to the mailbox.
package service

import (
	"context"
	"errors"
	"log/slog"

	"golang.org/x/crypto/bcrypt"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// passwordAlreadySet is the one message every "there is already a password"
// case returns, and it names the flow that can change one.
const passwordAlreadySet = "A password is already set; use \"Forgot password\" to change it"

// hasPassword reports whether acc's linked address could be used with
// POST /auth/login — that is, whether the password identity holding it carries
// a hash. It is what GET /profile answers with, and therefore what decides
// whether a client offers SetInitialPassword at all.
//
// Three ways to be false, all normal rather than exceptional: no email linked, a
// linked address whose identity was never given a password, and an address held
// by another account's identity (which the link flow refuses, but which a
// database restored from elsewhere could still present). None of them is an
// error — the answer is simply "no".
func (s *AuthService) hasPassword(ctx context.Context, acc *models.Account) (bool, error) {
	if acc.PrimaryEmail == nil {
		return false, nil
	}
	ident, err := s.accounts.FindIdentity(ctx, models.ProviderPassword, *acc.PrimaryEmail)
	if errors.Is(err, repository.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return ident.AccountID == acc.ID && ident.PasswordHash != nil, nil
}

// SetInitialPassword writes the first password for the calling account's linked
// email address.
//
// Preconditions, all 409s because none of them is something the caller can fix
// by retrying with a different body:
//
//   - an email must be linked and verified (the password is for signing in with
//     that address; without one there is nothing for the password to belong to)
//   - the identity must have no password hash yet — see the file comment
//
// No session is revoked, unlike ResetPassword. A reset is a recovery flow, run
// by someone who may have been locked out by an intruder, so every other device
// is cut off on principle. This runs inside a session the account holder is
// currently using, and adds a credential rather than replacing one: signing
// their own phone out mid-flow would be a punishment for finishing onboarding.
func (s *AuthService) SetInitialPassword(
	ctx context.Context, accountID int64, newPassword string,
) (*models.Profile, error) {
	if err := validatePassword(newPassword); err != nil {
		return nil, err
	}

	acc, err := s.accounts.FindByID(ctx, accountID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("Account not found")
	}
	if err != nil {
		return nil, err
	}
	if acc.PrimaryEmail == nil {
		return nil, apperr.NewConflict("Link an email address before setting a password")
	}

	ident, err := s.accounts.FindIdentity(ctx, models.ProviderPassword, *acc.PrimaryEmail)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		// primary_email is set but no password identity holds it. Only reachable
		// for a Google-only account, whose sign-in credential lives with Google.
		return nil, apperr.NewConflict("Link an email address before setting a password")
	case err != nil:
		return nil, err
	case ident.AccountID != accountID:
		// The address on this account is held by someone else's identity row.
		// Not something the caller can act on, and not something to explain in
		// detail either — it says whose address it is.
		slog.Error("set password: primary email is held by another account's identity",
			"account_id", accountID, "identity_account_id", ident.AccountID)
		return nil, apperr.NewConflict(passwordAlreadySet)
	case !ident.Verified:
		return nil, apperr.NewConflict("Verify your email address before setting a password")
	case ident.PasswordHash != nil:
		return nil, apperr.NewConflict(passwordAlreadySet)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	hashStr := string(hash)
	ident.PasswordHash = &hashStr

	tx, err := s.accounts.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := s.accounts.UpdateIdentity(ctx, tx, ident); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	slog.Info("initial password set", "account_id", accountID)
	return s.profileFor(ctx, acc)
}
