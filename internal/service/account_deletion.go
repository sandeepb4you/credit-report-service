// Package service — user-initiated account deletion, in three calls plus a
// cancel.
//
//	1. RequestAccountDeletionCode  — texts or mails a code to the identifier
//	2. VerifyAccountDeletionCode   — checks it, hands back a single-use grant
//	3. ConfirmAccountDeletion      — redeems the grant, schedules the purge
//	   CancelAccountDeletion       — signing in stops a scheduled purge
//
// The shape is password reset's, deliberately, and for the same reasons: step
// 2 exists so the client can move the user to the confirmation screen without
// holding a live OTP while they read it, and the grant is a separate
// credential so "this code was already checked" is an explicit server fact.
//
// WHAT IS DIFFERENT, and why this file is not just a copy:
//
//   - The caller is ANONYMOUS and may never have been signed in on this
//     device. Google Play requires the deletion route to work for somebody who
//     has already uninstalled the app, so there is no bearer token to lean on
//     and the OTP is the entire authentication.
//   - It accepts a phone number OR an email address, because the app is
//     phone-first and most accounts have no address at all. The channel
//     follows the identifier.
//   - Nothing is destroyed here. Confirming schedules a purge for
//     accountDeletionGracePeriod in the future and revokes every session; the
//     sweep in account_deletion_sweep.go is what finally runs it.
package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
	"time"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
	"credit-report-service/internal/sms"
)

const (
	// accountDeletionGracePeriod is how long a confirmed request waits before
	// the sweep carries it out, and how long the account holder has to stop it
	// by simply signing in.
	//
	// Fourteen days rather than a shorter window because the OTP is a single
	// factor delivered to a device that can be lost, resold or SIM-swapped,
	// and what it authorises is irreversible destruction of reports somebody
	// paid for. The window is what turns a stolen code from a catastrophe into
	// a notification the real owner can still act on. It is also long enough
	// to survive a fortnight's holiday, which a 48-hour window is not.
	//
	// A constant rather than config: the number is quoted to the user on the
	// web page, in the confirmation mail and in the privacy policy, and a
	// deployment that could move it would make all three wrong somewhere.
	accountDeletionGracePeriod = 14 * 24 * time.Hour

	// The grant handed out by step 2. Same entropy and storage as a password
	// reset token — 32 random bytes, SHA-256 at rest, nothing to brute-force.
	accountDeletionTokenBytes  = 32
	accountDeletionTokenPrefix = "adt_"
	// accountDeletionGrantTTL bounds the gap between proving the code and
	// pressing the final button. Shorter than the reset's fifteen minutes
	// would punish someone reading the consequences page properly; longer
	// would leave a live erasure grant sitting in a browser tab.
	accountDeletionGrantTTL = 15 * time.Minute
)

// deletionInvalidGrant is the single message every "your grant is no good"
// case returns — wrong, expired, already redeemed. Distinguishing them tells a
// guesser which half of the pair to keep working on, and the user's next step
// is identical in all three.
const deletionInvalidGrant = "This request has expired; start again and request a new code"

// AccountDeletionGrant is what VerifyAccountDeletionCode hands back: proof the
// caller holds the code, redeemable exactly once by ConfirmAccountDeletion.
type AccountDeletionGrant struct {
	DeletionToken string    `json:"deletionToken"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

// AccountDeletionSchedule is what a confirmed request tells the user: when it
// happens, and how to stop it.
type AccountDeletionSchedule struct {
	ScheduledFor time.Time `json:"scheduledFor"`
	// AlreadyPending distinguishes "we scheduled this now" from "this account
	// was already scheduled". The second is not an error — a user who runs the
	// flow twice should see the same date, not a failure — but the copy
	// differs, and only the server can tell which happened.
	AlreadyPending bool `json:"alreadyPending"`
}

// RequestAccountDeletionCode sends a deletion code to a phone number or email
// address that has an account.
//
// ALWAYS RETURNS NIL for an identifier with no account, exactly like
// password/forgot: this endpoint is public and unauthenticated, so an honest
// "no such account" would let anyone sort a list of phone numbers into
// registered and not. That is the same reasoning that makes
// /auth/otp/phone/status the one deliberate exception in the service, and this
// route is emphatically not a second one.
//
// Cooldown and send-limit errors ARE surfaced, as they are on password reset:
// the page needs to render "wait 43s" on its resend button, and reaching that
// state already required knowing the identifier.
func (s *AuthService) RequestAccountDeletionCode(ctx context.Context, identifier string) error {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return apperr.NewValidationWith("Validation failed", map[string]string{
			"identifier": "Enter the phone number or email address on your account.",
		})
	}

	acc, err := findAccountByIdentifier(ctx, s.accounts, identifier)
	if err != nil {
		// A malformed phone number is a validation error the user can fix, and
		// telling them so reveals nothing — it is a statement about the string
		// they typed, not about who has an account. Everything else resolves
		// to silence.
		var v *apperr.Validation
		if errors.As(err, &v) {
			return err
		}
		slog.Warn("account deletion requested for an unknown identifier")
		return nil
	}
	// A tombstone has no primary_email or primary_phone, so it cannot be found
	// by identifier at all and this is belt-and-braces. Kept because the cost
	// is one comparison and the failure it guards against — mailing a deletion
	// code for an account that is already gone — would be impossible to
	// explain to whoever received it.
	if acc.Status == models.AccountDeleted {
		slog.Warn("account deletion requested for an already-deleted account",
			"account_id", acc.ID)
		return nil
	}

	channel, destination := models.ChannelSMS, ""
	if strings.Contains(identifier, "@") {
		channel = models.ChannelEmail
		if acc.PrimaryEmail == nil {
			return nil
		}
		destination = *acc.PrimaryEmail
	} else {
		if acc.PrimaryPhone == nil {
			return nil
		}
		destination = *acc.PrimaryPhone
	}

	plain, err := s.issueDeletionChallenge(ctx, acc.ID, channel, destination)
	if err != nil {
		return err
	}

	if channel == models.ChannelEmail {
		if err := s.mailer.SendAccountDeletionOTP(destination, plain); err != nil {
			return err
		}
	} else if err := s.sms.SendOTP(ctx, destination, plain); err != nil {
		return err
	}

	slog.Info("account deletion code sent", "account_id", acc.ID, "channel", channel)
	return nil
}

// VerifyAccountDeletionCode checks the code and mints the grant.
func (s *AuthService) VerifyAccountDeletionCode(
	ctx context.Context, identifier, otp string,
) (*AccountDeletionGrant, error) {
	acc, destination, err := s.resolveDeletionTarget(ctx, identifier)
	if err != nil {
		return nil, err
	}

	ch, err := s.accounts.FindActiveChallenge(ctx, destination, models.OtpPurposeDeleteAccount)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewOtpFailure("No deletion request in progress; request a new code")
	}
	if err != nil {
		return nil, err
	}
	// Someone else's abandoned challenge on this destination must not be
	// redeemable here. Cannot normally happen — the destination IS the
	// account's — but the challenge table is keyed on the destination string,
	// and a number that moved between accounts would otherwise carry its old
	// challenge with it.
	if ch.AccountID == nil || *ch.AccountID != acc.ID {
		return nil, apperr.NewOtpFailure("No deletion request in progress; request a new code")
	}

	if verr := s.otp.Verify(ch, otp); verr != nil {
		// Persist the incremented attempt counter before surfacing the
		// failure, or the lockout never advances and a 4-digit code can be
		// walked through at leisure.
		if tx, err := s.accounts.BeginTx(ctx); err == nil {
			_ = s.accounts.UpdateChallenge(ctx, tx, ch)
			_ = tx.Commit(ctx)
		}
		slog.Warn("account deletion otp verification failed",
			"account_id", acc.ID, "attempts", ch.Attempts, "error", verr.Error())
		return nil, verr
	}

	token, digest, err := newAccountDeletionToken()
	if err != nil {
		return nil, err
	}
	expiresAt := time.Now().UTC().Add(accountDeletionGrantTTL)

	tx, err := s.accounts.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// Burning the challenge and issuing the grant land together: a commit
	// between them would leave either a reusable code or a grant nobody holds.
	if err := s.accounts.UpdateChallenge(ctx, tx, ch); err != nil {
		return nil, err
	}
	if err := s.accounts.InvalidateDeletionTokens(ctx, tx, acc.ID); err != nil {
		return nil, err
	}
	if err := s.accounts.CreateDeletionToken(ctx, tx, acc.ID, digest, expiresAt); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	slog.Info("account deletion code verified", "account_id", acc.ID)
	return &AccountDeletionGrant{DeletionToken: token, ExpiresAt: expiresAt}, nil
}

// ConfirmAccountDeletion redeems the grant and schedules the purge.
//
// Every session is revoked here rather than at sweep time, so a signed-in app
// stops being able to renew itself the moment the request is confirmed —
// leaving one live would let whoever holds it keep pulling reports out of an
// account in its last fortnight. Signing back in is what cancels the request,
// which is the behaviour the user is told about.
//
// Same caveat as a completed password reset, and worth knowing before anyone
// treats this as an immediate lock-out: revocation acts on the REFRESH half of
// the pair. An access token already issued keeps working on ordinary routes
// until it expires (auth.access-ttl, currently 168h), so an app that is open
// and never re-authenticates can keep working for up to a week into the grace
// period. It cannot be renewed past that, and the purge bumps token_epoch,
// which the permission gates re-check.
func (s *AuthService) ConfirmAccountDeletion(
	ctx context.Context, deletionToken string,
) (*AccountDeletionSchedule, error) {
	deletionToken = strings.TrimSpace(deletionToken)
	if deletionToken == "" {
		return nil, apperr.NewValidationWith("Validation failed", map[string]string{
			"deletionToken": "Missing deletion token.",
		})
	}

	tx, err := s.accounts.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	accountID, err := s.accounts.ConsumeDeletionToken(ctx, tx, hashToken(deletionToken))
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewOtpFailure(deletionInvalidGrant)
	}
	if err != nil {
		return nil, err
	}

	acc, err := s.accounts.FindByID(ctx, accountID)
	if err != nil {
		return nil, err
	}

	channel, masked := models.ChannelSMS, ""
	switch {
	case acc.PrimaryPhone != nil:
		masked = sms.MaskPhone(*acc.PrimaryPhone)
	case acc.PrimaryEmail != nil:
		channel, masked = models.ChannelEmail, maskEmail(*acc.PrimaryEmail)
	default:
		// No contact detail at all. Cannot happen behind a verified OTP, but
		// the column is nullable and the receipt must not be written with an
		// empty destination — the row is NOT NULL on both.
		masked = "unknown"
	}

	scheduledFor := time.Now().UTC().Add(accountDeletionGracePeriod)
	req, created, err := s.accounts.CreateDeletionRequest(
		ctx, tx, accountID, channel, masked, scheduledFor)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	// Outside the transaction: revoking sessions is its own write, and a
	// failure here must not roll back a request the user has been shown.
	if _, err := s.sessions.RevokeAllForAccount(
		ctx, accountID, "account deletion requested"); err != nil {
		slog.Error("account deletion: failed to revoke sessions",
			"account_id", accountID, "error", err)
	}

	// The same rule as the reset's audit line: loud, at WARN, and carrying no
	// contact details — the account id is enough to follow it up.
	slog.Warn("account deletion scheduled",
		"account_id", accountID,
		"scheduled_for", req.ScheduledFor,
		"already_pending", !created)

	return &AccountDeletionSchedule{
		ScheduledFor:   req.ScheduledFor,
		AlreadyPending: !created,
	}, nil
}

// CancelAccountDeletion stops a scheduled purge. Called on every successful
// sign-in, where the answer is almost always "there was nothing pending" — so
// it logs only when it actually cancelled something, and never fails the
// sign-in it is called from.
func (s *AuthService) CancelAccountDeletion(ctx context.Context, accountID int64, reason string) {
	cancelled, err := s.accounts.CancelPendingDeletion(ctx, accountID, reason)
	if err != nil {
		slog.Error("account deletion: cancel failed", "account_id", accountID, "error", err)
		return
	}
	if cancelled {
		slog.Warn("account deletion cancelled", "account_id", accountID, "reason", reason)
	}
}

// PendingAccountDeletion returns the account's live request, or nil. Used by
// GET /profile so a signed-in app can tell the user their account is scheduled
// for deletion and that signing in has just stopped it.
func (s *AuthService) PendingAccountDeletion(
	ctx context.Context, accountID int64,
) (*models.AccountDeletionRequest, error) {
	req, err := s.accounts.FindPendingDeletion(ctx, accountID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, nil
	}
	return req, err
}

// resolveDeletionTarget re-resolves the identifier to an account and the
// destination its challenge was raised against.
//
// Unlike the send step this one DOES report a missing account, because by here
// the caller has already been told a code was sent and is typing one in. There
// is nothing left to hide: an attacker probing identifiers learns the same
// thing from "no request in progress" whether or not the account exists, and a
// real user who mistyped their number needs to be told something.
func (s *AuthService) resolveDeletionTarget(
	ctx context.Context, identifier string,
) (*models.Account, string, error) {
	acc, err := findAccountByIdentifier(ctx, s.accounts, identifier)
	if err != nil {
		return nil, "", apperr.NewOtpFailure("No deletion request in progress; request a new code")
	}
	if strings.Contains(identifier, "@") {
		if acc.PrimaryEmail == nil {
			return nil, "", apperr.NewOtpFailure("No deletion request in progress; request a new code")
		}
		return acc, *acc.PrimaryEmail, nil
	}
	if acc.PrimaryPhone == nil {
		return nil, "", apperr.NewOtpFailure("No deletion request in progress; request a new code")
	}
	return acc, *acc.PrimaryPhone, nil
}

// issueDeletionChallenge mints or resends the delete_account OTP. The sibling
// of issueAddIdentityChallenge, with the same two guards: an expired challenge
// is abandoned rather than resumed (its exhausted send_count would otherwise
// lock the destination out permanently, and this flow has no other route in),
// and a challenge belonging to another account is never reused.
func (s *AuthService) issueDeletionChallenge(
	ctx context.Context, accountID int64, channel, destination string,
) (string, error) {
	ch, err := s.accounts.FindActiveChallenge(ctx, destination, models.OtpPurposeDeleteAccount)
	if errors.Is(err, repository.ErrNotFound) {
		ch = nil
	} else if err != nil {
		return "", err
	}
	if ch != nil && ch.ExpiresAt != nil && ch.ExpiresAt.Before(time.Now().UTC()) {
		ch = nil
	}
	if ch != nil && (ch.AccountID == nil || *ch.AccountID != accountID) {
		ch = nil
	}
	if ch == nil {
		ch = &models.OtpChallenge{
			AccountID:   &accountID,
			Channel:     channel,
			Destination: destination,
			Purpose:     models.OtpPurposeDeleteAccount,
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
	} else if err := s.accounts.UpdateChallenge(ctx, tx, ch); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return plain, nil
}

func newAccountDeletionToken() (token, digest string, err error) {
	buf := make([]byte, accountDeletionTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	token = accountDeletionTokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	return token, hashToken(token), nil
}
