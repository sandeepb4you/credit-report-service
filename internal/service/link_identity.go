// Shared machinery for adding a verified contact point to an account that
// already exists and is already signed in: the mandatory mobile number on an
// email signup (phone_register.go), and the optional email on a phone signup
// (email_link.go).
//
// These challenges differ from the signup and sign-in ones in a way that is the
// whole point of keeping them apart: they carry the account_id that requested
// them, and are refused for anyone else. A login challenge is find-or-create and
// keyed on the destination alone, so redeeming one signs the caller into
// whichever account owns that destination. Reusing that here would let a
// signed-in user "add" a contact that already belongs to someone else and be
// switched into their account instead.
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

// issueAddIdentityChallenge mints (or refreshes) an add_identity challenge bound
// to accountID and returns the plaintext code for the caller to deliver.
//
// Delivery is the caller's job, and deliberately happens after this commits:
// matching issueAndSend, a code the user can receive but we have not stored is
// worse than the reverse. A provider failure therefore burns the send slot.
func (s *AuthService) issueAddIdentityChallenge(
	ctx context.Context, accountID int64, channel, destination string,
) (string, error) {
	ch, err := s.accounts.FindActiveChallenge(ctx, destination, models.OtpPurposeAddIdentity)
	if errors.Is(err, repository.ErrNotFound) {
		ch = nil
	} else if err != nil {
		return "", err
	}
	// Same expiry rule as issueAndSend: a dead challenge must not pin its
	// exhausted send_count on the destination forever.
	if ch != nil && ch.ExpiresAt != nil && ch.ExpiresAt.Before(time.Now().UTC()) {
		ch = nil
	}
	// Someone else's abandoned attempt on this destination must not spend this
	// caller's sends — or, worse, be resumable by them. Start a fresh challenge;
	// the stale one stays unconsumed and expires on its own.
	if ch != nil && (ch.AccountID == nil || *ch.AccountID != accountID) {
		ch = nil
	}
	if ch == nil {
		ch = &models.OtpChallenge{
			AccountID:   &accountID,
			Channel:     channel,
			Destination: destination,
			Purpose:     models.OtpPurposeAddIdentity,
		}
	}

	plain, err := s.otp.Issue(ch)
	if err != nil {
		return "", err
	}

	tx, err := s.accounts.BeginTx(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if ch.ID == 0 {
		if err := s.accounts.CreateChallenge(ctx, tx, ch); err != nil {
			return "", err
		}
	} else {
		if err := s.accounts.UpdateChallenge(ctx, tx, ch); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return plain, nil
}

// verifyAddIdentityChallenge checks otp against the challenge accountID raised
// for destination, and returns the consumed challenge for the caller to persist
// inside the same transaction as whatever it is authorising.
//
// maskedDestination is what goes in the logs — the caller supplies it because
// a phone and an email are redacted differently.
func (s *AuthService) verifyAddIdentityChallenge(
	ctx context.Context, accountID int64, destination, otp, maskedDestination string,
) (*models.OtpChallenge, error) {
	ch, err := s.accounts.FindActiveChallenge(ctx, destination, models.OtpPurposeAddIdentity)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewOtpFailure("No pending verification; request a new code")
	}
	if err != nil {
		return nil, err
	}
	// A challenge raised by another account is reported as no challenge at all.
	// Saying "that code isn't yours" would confirm someone else is claiming this
	// destination, and the code is not ours to spend attempts against.
	if ch.AccountID == nil || *ch.AccountID != accountID {
		slog.Warn("add-identity challenge belongs to another account",
			"account_id", accountID, "destination", maskedDestination)
		return nil, apperr.NewOtpFailure("No pending verification; request a new code")
	}

	if verr := s.otp.Verify(ch, otp); verr != nil {
		// Persist the incremented attempt counter, then surface the failure.
		if tx, err := s.accounts.BeginTx(ctx); err == nil {
			_ = s.accounts.UpdateChallenge(ctx, tx, ch)
			_ = tx.Commit(ctx)
		}
		slog.Warn("add-identity otp failed",
			"account_id", accountID, "destination", maskedDestination,
			"attempts", ch.Attempts, "error", verr.Error())
		return nil, verr
	}
	return ch, nil
}
