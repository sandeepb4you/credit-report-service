// Mobile-number registration for an account that already exists: the mandatory
// step after an email signup, where the account has a verified email and no
// number at all.
//
// Deliberately separate from the phone sign-in flow next door. That one is
// find-or-create and keyed on the number alone, so redeeming its code signs the
// caller into whichever account owns the number — reusing it here would let a
// signed-in user "add" someone else's number and be switched into their account
// instead. These challenges carry the requesting account_id and are refused for
// anyone else, so verifying one can only ever add the number to the account that
// asked for it.
//
// Why the number is mandatory rather than optional: PAN verification checks the
// PAN and the name against the account's mobile number through Digitap's prefill
// API (see pan_prefill.go). An account with no number cannot pass that check at
// all, so an email signup without this step could never complete KYC.
package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
	"credit-report-service/internal/sms"
)

// SendPhoneRegistrationOTP mints a challenge bound to accountID and texts the
// code to the number being claimed.
//
// A number that already belongs to another account is refused here rather than
// at verify time, because the alternative is texting a code to a stranger's
// phone on any signed-in user's say-so. The cost of answering is that a signed-in
// caller can probe which numbers are registered — the enumeration the sign-in
// flow is careful to prevent. It is a narrower exposure (an account is needed,
// and the answer is only "in use") but it is real: this endpoint wants a
// per-account send limit before launch, which the per-destination cooldown in
// OTPService does not provide for a caller who walks through fresh numbers.
func (s *AuthService) SendPhoneRegistrationOTP(ctx context.Context, accountID int64, phone string) error {
	normalized, err := normalizePhone(phone)
	if err != nil {
		return err
	}

	acc, err := s.accounts.FindByID(ctx, accountID)
	if errors.Is(err, repository.ErrNotFound) {
		return apperr.NewNotFound("Account not found")
	}
	if err != nil {
		return err
	}
	if acc.PrimaryPhone != nil && *acc.PrimaryPhone == normalized {
		return apperr.NewConflict("This mobile number is already registered to your account")
	}
	if owner, err := s.accounts.FindByPhone(ctx, normalized); err == nil && owner.ID != accountID {
		slog.Warn("phone registration rejected: number belongs to another account",
			"account_id", accountID, "destination", sms.MaskPhone(normalized))
		return apperr.NewConflict("This mobile number is already registered to another account")
	} else if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return err
	}

	ch, err := s.accounts.FindActiveChallenge(ctx, normalized, models.OtpPurposeAddIdentity)
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
	// Someone else's abandoned attempt on this number must not spend this
	// caller's sends — or, worse, be resumable by them. Start a fresh challenge;
	// the stale one stays unconsumed and expires on its own.
	if ch != nil && (ch.AccountID == nil || *ch.AccountID != accountID) {
		ch = nil
	}
	if ch == nil {
		ch = &models.OtpChallenge{
			AccountID:   &accountID,
			Channel:     models.ChannelSMS,
			Destination: normalized,
			Purpose:     models.OtpPurposeAddIdentity,
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

	// Deliver only after the challenge is committed, matching issueAndSend and
	// SendPhoneOTP: a code the user can receive but we have not stored is worse
	// than the reverse.
	return s.sms.SendOTP(ctx, normalized, plain)
}

// VerifyPhoneRegistration checks the code and attaches the number to the calling
// account: a verified phone identity plus primary_phone, which is what the PAN
// check reads. Returns the same profile shape as GET /profile so the client can
// swap its cached state without a follow-up read.
//
// No new session is issued. The access token carries the account id, role and
// token epoch, none of which this changes — the client keeps the token it has.
func (s *AuthService) VerifyPhoneRegistration(
	ctx context.Context, accountID int64, phone, otp string,
) (*models.Profile, error) {
	normalized, err := normalizePhone(phone)
	if err != nil {
		return nil, err
	}

	ch, err := s.accounts.FindActiveChallenge(ctx, normalized, models.OtpPurposeAddIdentity)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewOtpFailure("No pending verification; request a new code")
	}
	if err != nil {
		return nil, err
	}
	// A challenge raised by another account is reported as no challenge at all.
	// Saying "that code isn't yours" would confirm someone else is registering
	// this number, and the code is not ours to spend attempts against.
	if ch.AccountID == nil || *ch.AccountID != accountID {
		slog.Warn("phone registration: challenge belongs to another account",
			"account_id", accountID, "destination", sms.MaskPhone(normalized))
		return nil, apperr.NewOtpFailure("No pending verification; request a new code")
	}

	if verr := s.otp.Verify(ch, otp); verr != nil {
		// Persist the incremented attempt counter, then surface the failure.
		if tx, err := s.accounts.BeginTx(ctx); err == nil {
			_ = s.accounts.UpdateChallenge(ctx, tx, ch)
			_ = tx.Commit(ctx)
		}
		slog.Warn("phone registration otp failed",
			"account_id", accountID, "destination", sms.MaskPhone(normalized),
			"attempts", ch.Attempts, "error", verr.Error())
		return nil, verr
	}

	acc, err := s.accounts.FindByID(ctx, accountID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("Account not found")
	}
	if err != nil {
		return nil, err
	}

	// Re-checked after the code passes as well as before it was sent: the number
	// could have been claimed in between, and the unique index on primary_phone
	// would fail the write with a 500 rather than something the user can act on.
	if owner, err := s.accounts.FindByPhone(ctx, normalized); err == nil && owner.ID != accountID {
		return nil, apperr.NewConflict("This mobile number is already registered to another account")
	} else if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return nil, err
	}

	now := time.Now().UTC()
	ident, err := s.accounts.FindIdentity(ctx, models.ProviderPhone, normalized)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		ident = &models.AuthIdentity{
			AccountID:       accountID,
			Provider:        models.ProviderPhone,
			ProviderSubject: normalized,
			Phone:           &normalized,
			Verified:        true,
			VerifiedAt:      &now,
		}
	case err != nil:
		return nil, err
	case ident.AccountID != accountID:
		// An identity row without a matching accounts.primary_phone — the checks
		// above would not have caught it. Still someone else's number.
		return nil, apperr.NewConflict("This mobile number is already registered to another account")
	default:
		ident.Phone = &normalized
		ident.Verified = true
		ident.VerifiedAt = &now
	}

	acc.PrimaryPhone = &normalized
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
				return nil, apperr.NewConflict("This mobile number is already registered to another account")
			}
			return nil, err
		}
	} else if err := s.accounts.UpdateIdentity(ctx, tx, ident); err != nil {
		return nil, err
	}
	if err := s.accounts.UpdateAccount(ctx, tx, acc); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			return nil, apperr.NewConflict("This mobile number is already registered to another account")
		}
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	slog.Info("phone registered", "account_id", acc.ID)
	return s.profileFor(ctx, acc)
}
