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

// The redesigned email signup, in three calls:
//
//	1. StartEmailSignup  — mails a code to an address, creating nothing
//	2. VerifySignupOTP   — checks the code, hands back a single-use grant
//	3. CompleteSignup    — redeems the grant, creates the account, opens a session
//
// The shape mirrors the password-reset trio next door, and for the same reason
// (see password_reset.go): a two-screen flow must not leave a live OTP in the
// client's hands while the user thinks about a password.
//
// What is different, and is the whole point of the redesign, is the ORDER. The
// older POST /auth/signup takes a password first and creates a PENDING account
// immediately, then mails a code to prove the address. That leaves half-made
// accounts behind for every abandoned signup, and it asks people to commit to a
// password before they have shown they can receive mail at the address. Here
// nothing is written until the address is proven, and the account is created
// ACTIVE in one step at the end — so there is nothing for /auth/verify-email to
// activate afterwards, and no PENDING row to clean up if the user walks away.
//
// The old route stays mounted and unchanged; this is additive.

const (
	// signupTokenBytes is the entropy behind a signup grant. Same size as a
	// refresh token, hashed the same way (SHA-256, see hashToken).
	signupTokenBytes = 32
	// signupTokenPrefix marks the token in logs and client storage, and is part
	// of the hashed string.
	signupTokenPrefix = "sgt_"
	// signupTokenTTL bounds the gap between proving the address and choosing a
	// password. Matches passwordResetTTL: it is the same screen-to-screen gap.
	signupTokenTTL = 15 * time.Minute
)

// signupInvalidGrant is the single message every "your grant is no good" case
// returns — wrong, expired, already redeemed. Distinguishing them tells an
// attacker which half of the pair to keep guessing, and the user's next step is
// the same in all three.
const signupInvalidGrant = "This signup has expired; request a new code"

// SignupGrant is what VerifySignupOTP hands back: proof that the caller holds
// the emailed code, redeemable exactly once by CompleteSignup.
type SignupGrant struct {
	SignupToken string    `json:"signupToken"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

// StartEmailSignup mails a verification code to an address that has no account.
//
// An address that IS already registered gets the same 200 and no mail, exactly
// as ForgotPassword does for an unknown one. The two endpoints are mirror
// images and have to lie in mirror-image directions: between them, an honest
// answer from either would let an anonymous caller sort any list of addresses
// into "has an account" and "does not". The user who genuinely owns a
// registered address is not stuck — they can sign in, or reset the password.
//
// Nothing is created here. The challenge row carries a NULL account_id, which
// otp_challenges has always allowed for exactly this case.
func (s *AuthService) StartEmailSignup(ctx context.Context, email string) error {
	email = normalizeEmail(email)

	existing, err := s.accounts.FindIdentity(ctx, models.ProviderPassword, email)
	switch {
	case err != nil && !errors.Is(err, repository.ErrNotFound):
		return err
	case existing != nil && existing.Verified:
		// Logged without the address, like every other auth-enumeration path.
		slog.Warn("email signup requested for an address that already has an account",
			"account_id", existing.AccountID)
		return nil
	}

	// An unverified identity from the older /auth/signup is not a reason to
	// refuse: that account was never usable, and proving the address here is a
	// strictly better standard than the one that created it. CompleteSignup
	// adopts the row rather than colliding with it.
	if err := s.issueAndSend(ctx, nil, email, models.OtpPurposeSignupEmail); err != nil {
		return err
	}
	slog.Info("email signup code sent")
	return nil
}

// VerifySignupOTP checks the emailed code and, on success, consumes the
// challenge and mints the single-use grant that CompleteSignup redeems.
func (s *AuthService) VerifySignupOTP(
	ctx context.Context, email, otp string,
) (*SignupGrant, error) {
	email = normalizeEmail(email)

	ch, err := s.accounts.FindActiveChallenge(ctx, email, models.OtpPurposeSignupEmail)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewOtpFailure("No signup in progress; request a new code")
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
		slog.Warn("email signup otp verification failed",
			"attempts", ch.Attempts, "error", verr.Error())
		return nil, verr
	}

	token, digest, err := newSignupToken()
	if err != nil {
		return nil, err
	}
	expiresAt := time.Now().UTC().Add(signupTokenTTL)

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
	if err := s.accounts.InvalidateSignupTokens(ctx, tx, email); err != nil {
		return nil, err
	}
	if _, err := s.accounts.CreateSignupToken(ctx, tx, email, digest, expiresAt); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	slog.Info("email signup code verified")
	return &SignupGrant{SignupToken: token, ExpiresAt: expiresAt}, nil
}

// CompleteSignup redeems a grant, creates the account and opens a session.
//
// The account is created ACTIVE with a verified identity in one transaction:
// the address was proven two calls ago, so there is nothing left for anyone to
// verify. An optional referralCode attributes it to whoever owns that code, and
// is resolved before anything is written — a bad code fails the call outright
// rather than silently creating an unattributed account, because the referrer
// would otherwise never know they lost the credit.
func (s *AuthService) CompleteSignup(
	ctx context.Context, signupToken, password, referralCode string, dev models.DeviceInfo,
) (*AuthResult, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}

	signupToken = strings.TrimSpace(signupToken)
	if signupToken == "" {
		return nil, apperr.NewUnauthorized(signupInvalidGrant)
	}

	grant, err := s.accounts.FindLiveSignupToken(ctx, hashToken(signupToken))
	if errors.Is(err, repository.ErrNotFound) {
		slog.Warn("signup rejected: unknown, spent or expired grant")
		return nil, apperr.NewUnauthorized(signupInvalidGrant)
	}
	if err != nil {
		return nil, err
	}
	email := grant.Email

	referrerID, referrerCode, err := s.coupons.ResolveReferral(ctx, referralCode)
	if err != nil {
		return nil, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	hashStr := string(hash)

	// The address was free when the code went out; between then and now someone
	// could have registered it (the older /auth/signup, or a Google login on the
	// same address). Re-read rather than assume.
	existing, err := s.accounts.FindIdentity(ctx, models.ProviderPassword, email)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return nil, err
	}
	if existing != nil && existing.Verified {
		slog.Warn("signup rejected: email registered while the signup was in progress",
			"account_id", existing.AccountID)
		return nil, apperr.NewConflict("Email already registered")
	}

	now := time.Now().UTC()

	tx, err := s.accounts.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	// Burn the grant first: the compare-and-set inside is what makes a
	// concurrent double-redemption impossible, and it must fail the whole
	// transaction rather than run after an account has already been created.
	if err := s.accounts.ConsumeSignupToken(ctx, tx, grant.ID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NewUnauthorized(signupInvalidGrant)
		}
		return nil, err
	}
	if err := s.accounts.InvalidateSignupTokens(ctx, tx, email); err != nil {
		return nil, err
	}

	var acc *models.Account
	if existing != nil {
		// An unverified leftover from the older flow. Adopt it rather than
		// colliding with its unique (provider, provider_subject) key: the
		// address has now been proven to a higher standard than that row was
		// ever held to, and the password being set is the one just chosen.
		acc, err = s.accounts.FindByID(ctx, existing.AccountID)
		if err != nil {
			return nil, err
		}
		existing.PasswordHash = &hashStr
		existing.Verified = true
		existing.VerifiedAt = &now
		if err := s.accounts.UpdateIdentity(ctx, tx, existing); err != nil {
			return nil, err
		}
		// Attribution is not rewritten: the account already exists and may
		// already be attributed, and re-attributing it here would let anyone
		// finishing an abandoned signup redirect the credit.
	} else {
		acc = &models.Account{Status: models.AccountActive}
		if referrerID != 0 {
			acc.ReferredByAccountID = &referrerID
			acc.ReferredByCode = &referrerCode
		}
		if err := s.accounts.CreateAccount(ctx, tx, acc); err != nil {
			return nil, err
		}
		ident := &models.AuthIdentity{
			AccountID:       acc.ID,
			Provider:        models.ProviderPassword,
			ProviderSubject: email,
			Email:           &email,
			PasswordHash:    &hashStr,
			Verified:        true,
			VerifiedAt:      &now,
		}
		if err := s.accounts.CreateIdentity(ctx, tx, ident); err != nil {
			if errors.Is(err, repository.ErrConflict) {
				return nil, apperr.NewConflict("Email already registered")
			}
			return nil, err
		}
	}

	acc.PrimaryEmail = &email
	if acc.Status == models.AccountPending {
		acc.Status = models.AccountActive
	}
	if err := s.accounts.UpdateAccount(ctx, tx, acc); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			return nil, apperr.NewConflict("Email already registered")
		}
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	// Auto-promote to admin if the (now verified) email is on the allowlist.
	// Outside the tx on purpose, as in VerifyEmail: SetRole is idempotent and a
	// failure here must not unwind an account that already exists.
	s.applyAdminRole(ctx, acc)

	slog.Info("email signup complete", "account_id", acc.ID)
	return s.issueSession(ctx, acc, dev)
}

// newSignupToken mints a grant and its storage digest. The plaintext is
// returned exactly once, to the caller that will hand it to the client.
func newSignupToken() (token, digest string, err error) {
	buf := make([]byte, signupTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	token = signupTokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	return token, hashToken(token), nil
}
