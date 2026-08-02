package service

import (
	"context"
	"errors"
	"log/slog"
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
	agentSvc       *AgentService
	admins         []string // auth.admin-emails allowlist; lowercase
	googleClientID string   // "Web application" OAuth client ID; empty -> disabled
}

func NewAuthService(
	accounts *repository.AccountRepo,
	otp *OTPService,
	mailer Mailer,
	tokens *TokenService,
	authCfg config.AuthConfig,
	agentSvc *AgentService,
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
		agentSvc:       agentSvc,
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

// AuthResult carries a session token plus the account (returned on verify/login).
type AuthResult struct {
	Token     string          `json:"token"`
	ExpiresAt time.Time       `json:"expiresAt"`
	Account   *models.Account `json:"account"`
}

// Signup creates a PENDING account with an unverified password identity and
// mails a verification OTP. Re-signing up for an unverified email updates the
// password and re-issues the OTP. An optional agentCode links the account to
// the referring agent.
func (s *AuthService) Signup(ctx context.Context, email, password string, agentCode *string) (*SignupResult, error) {
	email = normalizeEmail(email)
	if err := validatePassword(password); err != nil {
		return nil, err
	}

	// Validate agent code if provided.
	var agentID *int64
	if agentCode != nil {
		cleaned := strings.TrimSpace(*agentCode)
		if cleaned != "" {
			aid, err := s.agentSvc.ValidateAgentCode(ctx, cleaned)
			if err != nil {
				return nil, err
			}
			agentID = &aid
		}
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
		acc := &models.Account{Status: models.AccountPending, AgentID: agentID}
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
func (s *AuthService) VerifyEmail(ctx context.Context, email, otp string) (*AuthResult, error) {
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
	return s.issueToken(acc)
}

// Login verifies email+password and returns a session token.
func (s *AuthService) Login(ctx context.Context, email, password string) (*AuthResult, error) {
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
	return s.issueToken(acc)
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

// UpdateAgentCode sets or updates the agent on the authenticated account.
// Non-admin users can only update the agent code once (tracked by agent_code_updated).
// Admins can update it anytime.
func (s *AuthService) UpdateAgentCode(ctx context.Context, accountID int64, agentCode string, isAdmin bool) (*models.Account, error) {
	acc, err := s.accounts.FindByID(ctx, accountID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("Account not found")
	}
	if err != nil {
		return nil, err
	}

	// Non-admin users can only update agent code once.
	if !isAdmin && acc.AgentCodeUpdated {
		return nil, apperr.NewConflict("Agent code can only be updated once")
	}

	agentID, err := s.agentSvc.ValidateAgentCode(ctx, agentCode)
	if err != nil {
		return nil, err
	}

	if err := s.accounts.SetAgentID(ctx, accountID, &agentID, true); err != nil {
		return nil, err
	}

	// Re-fetch to return the updated account with the new agent_id.
	acc, err = s.accounts.FindByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	slog.Info("agent code updated", "account_id", accountID, "agent_id", agentID, "is_admin", isAdmin)
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

func (s *AuthService) issueToken(acc *models.Account) (*AuthResult, error) {
	tok, err := s.tokens.Issue(acc.ID, acc.Role)
	if err != nil {
		return nil, err
	}
	return &AuthResult{Token: tok.Token, ExpiresAt: tok.ExpiresAt, Account: acc}, nil
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
