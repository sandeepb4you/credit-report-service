package service

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/config"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// minPasswordLen is the minimum accepted password length.
const minPasswordLen = 8

// AuthService implements email+password signup with email-OTP verification and
// JWT login. Additional identity providers (google, phone) slot in alongside
// the password provider on the same accounts.
type AuthService struct {
	accounts       *repository.AccountRepo
	otp            *OTPService
	mailer         Mailer
	tokens         *TokenService
	sessions       *SessionService
	coupons        *CouponService // resolves referral codes at signup
	admins         []string       // auth.admin-emails allowlist; lowercase
	googleClientID string         // "Web application" OAuth client ID; empty -> disabled
}

func NewAuthService(
	accounts *repository.AccountRepo,
	otp *OTPService,
	mailer Mailer,
	tokens *TokenService,
	sessions *SessionService,
	coupons *CouponService,
	authCfg config.AuthConfig,
) *AuthService {
	admins := make([]string, 0, len(authCfg.AdminEmails))
	for _, e := range authCfg.AdminEmails {
		if t := strings.ToLower(strings.TrimSpace(e)); t != "" {
			admins = append(admins, t)
		}
	}
	return &AuthService{
		accounts:       accounts,
		otp:            otp,
		mailer:         mailer,
		tokens:         tokens,
		sessions:       sessions,
		coupons:        coupons,
		admins:         admins,
		googleClientID: authCfg.Google.ClientID,
	}
}

// SignupResult is returned after signup; the client must verify the email next.
type SignupResult struct {
	AccountID int64  `json:"accountId"`
	Email     string `json:"email"`
	Message   string `json:"message"`
}

// AuthResult carries a freshly minted token pair plus the account (returned on
// verify/login/refresh).
//
// RefreshToken is populated by the service on every path, but the handler
// strips it for web clients and sets an httpOnly cookie instead — a browser
// must never see a 30-day credential in JS-readable JSON. SessionID is the
// device this pair belongs to and is not serialized; handlers use it to set
// cookies and to label the current device.
type AuthResult struct {
	Token        string          `json:"token"`
	ExpiresAt    time.Time       `json:"expiresAt"`
	RefreshToken string          `json:"refreshToken,omitempty"`
	Account      *models.Account `json:"account"`

	SessionID        int64     `json:"-"`
	RefreshExpiresAt time.Time `json:"-"`
}

// Signup creates a PENDING account with an unverified password identity and
// mails a verification OTP. Re-signing up for an unverified email updates the
// password and re-issues the OTP.
//
// An optional referralCode attributes the new account to whoever owns that
// code. It is resolved before anything is written, so a bad code fails the
// signup outright rather than silently creating an unattributed account — the
// referrer would otherwise never know they lost the credit. Attribution only
// applies to accounts created here; re-signing up on an existing unverified
// account keeps whatever attribution it already had.
func (s *AuthService) Signup(
	ctx context.Context, email, password, referralCode string,
) (*SignupResult, error) {
	email = normalizeEmail(email)
	if err := validatePassword(password); err != nil {
		return nil, err
	}

	referrerID, referrerCode, err := s.coupons.ResolveReferral(ctx, referralCode)
	if err != nil {
		return nil, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	hashStr := string(hash)

	existing, err := s.accounts.FindIdentity(ctx, models.ProviderPassword, email)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return nil, err
	}

	var accountID int64
	tx, err := s.accounts.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	switch {
	case existing != nil && existing.Verified:
		slog.Warn("signup rejected: email already registered", "account_id", existing.AccountID)
		return nil, apperr.NewConflict("Email already registered")
	case existing != nil:
		// Unverified: allow updating the password and re-verifying.
		existing.PasswordHash = &hashStr
		if err := s.accounts.UpdateIdentity(ctx, tx, existing); err != nil {
			return nil, err
		}
		accountID = existing.AccountID
	default:
		acc := &models.Account{Status: models.AccountPending}
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
			Verified:        false,
		}
		if err := s.accounts.CreateIdentity(ctx, tx, ident); err != nil {
			if errors.Is(err, repository.ErrConflict) {
				return nil, apperr.NewConflict("Email already registered")
			}
			return nil, err
		}
		accountID = acc.ID
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	if err := s.issueAndSend(ctx, &accountID, email, models.OtpPurposeSignup); err != nil {
		return nil, err
	}
	slog.Info("signup", "account_id", accountID, "status", "pending_verification")
	return &SignupResult{
		AccountID: accountID,
		Email:     email,
		Message:   "Verification code sent to " + email,
	}, nil
}

// ResendOTP re-issues the signup verification OTP for an unverified email.
func (s *AuthService) ResendOTP(ctx context.Context, email string) error {
	email = normalizeEmail(email)
	ident, err := s.accounts.FindIdentity(ctx, models.ProviderPassword, email)
	if errors.Is(err, repository.ErrNotFound) {
		return apperr.NewNotFound("No signup found for this email")
	}
	if err != nil {
		return err
	}
	if ident.Verified {
		return apperr.NewConflict("Email is already verified")
	}
	return s.issueAndSend(ctx, &ident.AccountID, email, models.OtpPurposeSignup)
}

// VerifyEmail checks the signup OTP; on success it verifies the identity,
// activates the account, and returns a session token.
func (s *AuthService) VerifyEmail(
	ctx context.Context, email, otp string, dev models.DeviceInfo,
) (*AuthResult, error) {
	email = normalizeEmail(email)

	ch, err := s.accounts.FindActiveChallenge(ctx, email, models.OtpPurposeSignup)
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
		// Log the failed attempt without the OTP value or the email. account_id
		// may be nil for legacy challenges; fall back to 0.
		aid := int64(0)
		if ch.AccountID != nil {
			aid = *ch.AccountID
		}
		slog.Warn("otp verification failed",
			"account_id", aid,
			"attempts", ch.Attempts,
			"error", verr.Error(),
		)
		return nil, verr
	}

	ident, err := s.accounts.FindIdentity(ctx, models.ProviderPassword, email)
	if err != nil {
		return nil, err
	}
	acc, err := s.accounts.FindByID(ctx, ident.AccountID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	ident.Verified = true
	ident.VerifiedAt = &now
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
	if err := s.accounts.UpdateIdentity(ctx, tx, ident); err != nil {
		return nil, err
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
	// Done outside the tx on purpose: SetRole is idempotent and a failure here
	// must not unwind a successful verification — the account is already active.
	s.applyAdminRole(ctx, acc)

	slog.Info("email verified", "account_id", acc.ID)
	return s.issueSession(ctx, acc, dev)
}

// Login verifies email+password and opens a session for the device.
func (s *AuthService) Login(
	ctx context.Context, email, password string, dev models.DeviceInfo,
) (*AuthResult, error) {
	email = normalizeEmail(email)

	ident, err := s.accounts.FindIdentity(ctx, models.ProviderPassword, email)
	if errors.Is(err, repository.ErrNotFound) {
		// Unknown email: don't echo the address or any account id.
		slog.Warn("login failed: unknown identity")
		return nil, apperr.NewUnauthorized("Invalid email or password")
	}
	if err != nil {
		return nil, err
	}
	if ident.PasswordHash == nil ||
		bcrypt.CompareHashAndPassword([]byte(*ident.PasswordHash), []byte(password)) != nil {
		// Wrong password: log the account_id so brute-force is traceable, but
		// never the email or password.
		slog.Warn("login failed: invalid credentials", "account_id", ident.AccountID)
		return nil, apperr.NewUnauthorized("Invalid email or password")
	}
	if !ident.Verified {
		slog.Warn("login failed: email not verified", "account_id", ident.AccountID)
		return nil, apperr.NewUnauthorized("Email not verified; please verify to continue")
	}

	acc, err := s.accounts.FindByID(ctx, ident.AccountID)
	if err != nil {
		return nil, err
	}
	s.applyAdminRole(ctx, acc)
	slog.Info("login", "account_id", acc.ID, "method", "password")
	return s.issueSession(ctx, acc, dev)
}

// GetAccount returns the account for an authenticated request.
func (s *AuthService) GetAccount(ctx context.Context, accountID int64) (*models.Account, error) {
	acc, err := s.accounts.FindByID(ctx, accountID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("Account not found")
	}
	return acc, err
}

// UpdateProfile sets first/last name and date of birth, marking the profile
// step complete.
func (s *AuthService) UpdateProfile(
	ctx context.Context, accountID int64, firstName, lastName string, dob *time.Time,
) (*models.Account, error) {
	acc, err := s.accounts.FindByID(ctx, accountID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("Account not found")
	}
	if err != nil {
		return nil, err
	}

	first := strings.TrimSpace(firstName)
	last := strings.TrimSpace(lastName)
	acc.FirstName = &first
	acc.LastName = &last
	acc.DateOfBirth = dob
	acc.ProfileCompleted = first != "" && last != ""

	tx, err := s.accounts.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := s.accounts.UpdateAccount(ctx, tx, acc); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return acc, nil
}

// SetRole grants or revokes a role on an account. Callers must already have
// gated on models.PermAccountSetRole — the service only validates that the
// role exists, so an unknown value can never be written to the column.
func (s *AuthService) SetRole(ctx context.Context, accountID int64, role string) (*models.Account, error) {
	if _, ok := models.RoleRank(role); !ok {
		return nil, apperr.NewValidationWith("Validation failed",
			map[string]string{"role": "unknown role " + strconv.Quote(role)})
	}
	if _, err := s.accounts.FindByID(ctx, accountID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NewNotFound("Account not found")
		}
		return nil, err
	}
	if err := s.accounts.SetRole(ctx, accountID, role); err != nil {
		return nil, err
	}
	acc, err := s.accounts.FindByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	// The account keeps whatever access its existing tokens carry until they
	// expire; role lives in the JWT and is only re-read on refresh.
	slog.Info("role changed", "account_id", accountID, "role", role)
	return acc, nil
}

// ---- helpers -----------------------------------------------------------

// issueAndSend loads or creates the active OTP challenge for a destination,
// issues a fresh code (enforcing cooldown/send limits), persists it, and mails
// the code.
func (s *AuthService) issueAndSend(ctx context.Context, accountID *int64, destination, purpose string) error {
	ch, err := s.accounts.FindActiveChallenge(ctx, destination, purpose)
	if errors.Is(err, repository.ErrNotFound) {
		ch = &models.OtpChallenge{
			AccountID:   accountID,
			Channel:     models.ChannelEmail,
			Destination: destination,
			Purpose:     purpose,
		}
	} else if err != nil {
		return err
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

	return s.mailer.SendOTP(destination, plain)
}

// issueSession opens a session for the device and mints the access token bound
// to it. Every successful authentication funnels through here, so a device
// always ends up in the account's signed-in list.
func (s *AuthService) issueSession(
	ctx context.Context, acc *models.Account, dev models.DeviceInfo,
) (*AuthResult, error) {
	sess, refresh, err := s.sessions.Start(ctx, acc.ID, dev)
	if err != nil {
		return nil, err
	}
	tok, err := s.tokens.Issue(acc.ID, acc.Role, sess.ID)
	if err != nil {
		return nil, err
	}
	return &AuthResult{
		Token:            tok.Token,
		ExpiresAt:        tok.ExpiresAt,
		RefreshToken:     refresh,
		Account:          acc,
		SessionID:        sess.ID,
		RefreshExpiresAt: sess.ExpiresAt,
	}, nil
}

// Refresh exchanges a refresh token for a new token pair. The session's
// account is re-read so a role change lands in the new access token within one
// refresh cycle rather than waiting for the user to sign in again.
func (s *AuthService) Refresh(
	ctx context.Context, refreshToken string, dev models.DeviceInfo,
) (*AuthResult, error) {
	sess, next, err := s.sessions.Refresh(ctx, refreshToken, dev)
	if err != nil {
		return nil, err
	}
	acc, err := s.accounts.FindByID(ctx, sess.AccountID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NewUnauthorized("Session expired; please sign in again")
		}
		return nil, err
	}
	tok, err := s.tokens.Issue(acc.ID, acc.Role, sess.ID)
	if err != nil {
		return nil, err
	}
	return &AuthResult{
		Token:            tok.Token,
		ExpiresAt:        tok.ExpiresAt,
		RefreshToken:     next,
		Account:          acc,
		SessionID:        sess.ID,
		RefreshExpiresAt: sess.ExpiresAt,
	}, nil
}

// applyAdminRole promotes the account to admin if its primary email is on the
// config allowlist and it isn't already. Best-effort: errors are swallowed
// (logged via the central handler only on truly unexpected DB failures) so a
// flaky promote never blocks a successful auth. The account's Role field is
// updated in place so the immediately-following issueToken picks it up.
func (s *AuthService) applyAdminRole(ctx context.Context, acc *models.Account) {
	if acc.PrimaryEmail == nil || acc.Role == models.RoleAdmin {
		return
	}
	if !s.isAdminEmail(*acc.PrimaryEmail) {
		return
	}
	if err := s.accounts.SetRole(ctx, acc.ID, models.RoleAdmin); err != nil {
		// Don't fail the auth flow; the next login will retry the promotion.
		slog.Warn("admin role promotion failed", "account_id", acc.ID, "error", err)
		return
	}
	slog.Info("admin role granted", "account_id", acc.ID)
	acc.Role = models.RoleAdmin
}

// isAdminEmail reports whether email matches the allowlist (case-insensitive).
func (s *AuthService) isAdminEmail(email string) bool {
	if len(s.admins) == 0 {
		return false
	}
	e := strings.ToLower(strings.TrimSpace(email))
	for _, a := range s.admins {
		if a == e {
			return true
		}
	}
	return false
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func validatePassword(pw string) error {
	if len(pw) < minPasswordLen {
		return apperr.NewValidationWith("Validation failed",
			map[string]string{"password": "password must be at least 8 characters"})
	}
	return nil
}
