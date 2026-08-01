package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/api/idtoken"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// GoogleLogin authenticates a user with a Google ID token issued by the Google
// Sign-In SDK (Android or iOS). The token is verified against Google's public
// keys with its `aud` pinned to the configured Web client ID; on success the
// token's `sub` becomes a verified "google" identity on the account.
//
// Account linking rules:
//   - An existing "google" identity for this `sub` -> reuse its account.
//   - Else, an existing account whose primary email matches (or an identity by
//     email) -> add the google identity onto it.
//   - Else, create a fresh ACTIVE account.
//
// Because Google is the identity provider, the google identity is created as
// verified=true; no email OTP is sent. Email verification only governs the
// password provider.
func (s *AuthService) GoogleLogin(ctx context.Context, idToken string) (*AuthResult, error) {
	if s.googleClientID == "" {
		// Not configured: surface as 503 so the client can distinguish a disabled
		// provider from a bad token.
		return nil, apperr.NewServiceUnavailable("Google login is not configured")
	}
	idToken = strings.TrimSpace(idToken)
	if idToken == "" {
		return nil, apperr.NewValidationWith("Validation failed",
			map[string]string{"idToken": "idToken is required"})
	}

	payload, err := idtoken.Validate(ctx, idToken, s.googleClientID)
	if err != nil {
		slog.Warn("google login failed: token validation", "error", err)
		return nil, apperr.NewUnauthorized("Invalid Google token")
	}

	sub := payload.Subject
	if sub == "" {
		return nil, apperr.NewUnauthorized("Invalid Google token")
	}
	email, _ := payload.Claims["email"].(string)
	email = normalizeEmail(email)
	emailVerified, _ := payload.Claims["email_verified"].(bool)
	first, _ := payload.Claims["given_name"].(string)
	last, _ := payload.Claims["family_name"].(string)

	acc, err := s.resolveGoogleAccount(ctx, sub, email, emailVerified, first, last)
	if err != nil {
		return nil, err
	}
	s.applyAdminRole(ctx, acc)
	slog.Info("login", "account_id", acc.ID, "method", "google")
	return s.issueToken(acc)
}

// resolveGoogleAccount loads-or-creates the account + google identity for a
// verified Google payload, following the linking rules in GoogleLogin.
func (s *AuthService) resolveGoogleAccount(
	ctx context.Context, sub, email string, emailVerified bool, first, last string,
) (*models.Account, error) {
	// 1. Existing google identity for this subject -> reuse its account.
	if googleIdent, err := s.accounts.FindIdentity(ctx, models.ProviderGoogle, sub); err == nil {
		acc, err := s.accounts.FindByID(ctx, googleIdent.AccountID)
		if err != nil {
			return nil, err
		}
		return s.refreshGoogleIdentity(ctx, googleIdent, acc, email, first, last)
	} else if !errors.Is(err, repository.ErrNotFound) {
		return nil, err
	}

	// 2. Link onto an existing account whose email matches.
	if email != "" {
		if existingIdent, err := s.accounts.FindIdentityByEmail(ctx, email); err == nil {
			acc, err := s.accounts.FindByID(ctx, existingIdent.AccountID)
			if err != nil {
				return nil, err
			}
			return s.linkGoogleIdentity(ctx, acc, sub, email, emailVerified, first, last)
		} else if !errors.Is(err, repository.ErrNotFound) {
			return nil, err
		}
	}

	// 3. No match -> create a fresh ACTIVE account with a verified google identity.
	tx, err := s.accounts.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	acc := &models.Account{
		Status:           models.AccountActive,
		FirstName:        nullable(first),
		LastName:         nullable(last),
		ProfileCompleted: strings.TrimSpace(first) != "" && strings.TrimSpace(last) != "",
	}
	if emailVerified && email != "" {
		acc.PrimaryEmail = &email
	}
	if err := s.accounts.CreateAccount(ctx, tx, acc); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	ident := &models.AuthIdentity{
		AccountID:       acc.ID,
		Provider:        models.ProviderGoogle,
		ProviderSubject: sub,
		Verified:        true,
		VerifiedAt:      &now,
	}
	if email != "" {
		ident.Email = &email
	}
	if err := s.accounts.CreateIdentity(ctx, tx, ident); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			return nil, apperr.NewConflict("Google identity already linked to another account")
		}
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	slog.Info("google account created", "account_id", acc.ID)
	return acc, nil
}

// refreshGoogleIdentity updates the google identity's email/verified state if
// the latest token carries newer info, and returns the account.
func (s *AuthService) refreshGoogleIdentity(
	ctx context.Context, ident *models.AuthIdentity, acc *models.Account, email string, first, last string,
) (*models.Account, error) {
	changed := false
	if email != "" && (ident.Email == nil || *ident.Email != email) {
		ident.Email = &email
		changed = true
	}
	// Google-verified email should win on the account too.
	if email != "" && (acc.PrimaryEmail == nil || *acc.PrimaryEmail != email) {
		acc.PrimaryEmail = &email
		changed = true
	}
	if changed {
		tx, err := s.accounts.BeginTx(ctx)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback(ctx)
		if err := s.accounts.UpdateIdentity(ctx, tx, ident); err != nil {
			return nil, err
		}
		if err := s.accounts.UpdateAccount(ctx, tx, acc); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
	}
	acc = applyNames(acc, first, last)
	return acc, nil
}

// linkGoogleIdentity adds a new google identity onto an existing account.
func (s *AuthService) linkGoogleIdentity(
	ctx context.Context, acc *models.Account, sub, email string, emailVerified bool, first, last string,
) (*models.Account, error) {
	tx, err := s.accounts.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	now := time.Now().UTC()
	ident := &models.AuthIdentity{
		AccountID:       acc.ID,
		Provider:        models.ProviderGoogle,
		ProviderSubject: sub,
		Verified:        true,
		VerifiedAt:      &now,
	}
	if email != "" {
		ident.Email = &email
	}
	if err := s.accounts.CreateIdentity(ctx, tx, ident); err != nil {
		if errors.Is(err, repository.ErrConflict) {
			return nil, apperr.NewConflict("Google identity already linked to another account")
		}
		return nil, err
	}
	// Promote the account's primary email if Google vouches for one and the
	// account doesn't yet have one.
	if email != "" && emailVerified && acc.PrimaryEmail == nil {
		acc.PrimaryEmail = &email
	}
	applyNames(acc, first, last)
	if err := s.accounts.UpdateAccount(ctx, tx, acc); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	slog.Info("google identity linked", "account_id", acc.ID)
	return acc, nil
}

// applyNames fills in first/last name from the Google profile when the account
// doesn't already have them (Google info is trusted but not authoritative over
// a name the user later edited in their profile).
func applyNames(acc *models.Account, first, last string) *models.Account {
	first = strings.TrimSpace(first)
	last = strings.TrimSpace(last)
	if first != "" && acc.FirstName == nil {
		acc.FirstName = &first
	}
	if last != "" && acc.LastName == nil {
		acc.LastName = &last
	}
	if !acc.ProfileCompleted && acc.FirstName != nil && acc.LastName != nil {
		acc.ProfileCompleted = true
	}
	return acc
}

func nullable(v string) *string {
	if v = strings.TrimSpace(v); v == "" {
		return nil
	}
	return &v
}
