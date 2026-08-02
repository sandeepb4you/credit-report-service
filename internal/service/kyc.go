package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// KycService implements the PAN submission endpoint. PAN authenticity against
// the income-tax database is out of scope here; this only accepts, formats,
// and stores the number. A VERIFIED row (set by a separate KYC-provider flow)
// is what gates the analysis products.
//
// In demo mode the real verification provider is unavailable, so a submitted
// PAN is auto-verified on submission (see SubmitPAN). This flag must stay false
// in production.
type KycService struct {
	accounts *repository.AccountRepo
	demoMode bool
}

func NewKycService(accounts *repository.AccountRepo, demoMode bool) *KycService {
	return &KycService{accounts: accounts, demoMode: demoMode}
}

// SubmitPAN validates the PAN format, then upserts it against the account's
// kyc_records row. A re-submission resets verification since the new PAN isn't
// trusted until re-verified.
func (s *KycService) SubmitPAN(ctx context.Context, accountID int64, pan string) (*models.KYCRecord, error) {
	pan = strings.ToUpper(strings.TrimSpace(pan))
	if !panFormat.MatchString(pan) {
		return nil, apperr.NewValidationWith("Validation failed",
			map[string]string{"pan": "PAN must be 5 letters, 4 digits, 1 letter (e.g. ABCDE1234F)"})
	}

	rec, err := s.accounts.UpsertPAN(ctx, accountID, pan)
	if err != nil {
		if errors.Is(err, repository.ErrConflict) {
			// Conflict, not the PAN value itself.
			slog.Warn("pan submission rejected: linked to another account", "account_id", accountID)
			return nil, apperr.NewConflict("PAN is already linked to another account")
		}
		return nil, err
	}
	// account_id only — the PAN number is PII and must never appear in logs.
	slog.Info("pan submitted", "account_id", accountID, "verified", rec.PANVerified)

	// Demo mode: skip the (unavailable) external verification provider and mark
	// the PAN verified immediately, so the credit-analytics flow is usable
	// without the admin verify step. Never enabled in production.
	if s.demoMode {
		verified, verr := s.accounts.VerifyPAN(ctx, accountID)
		if verr != nil {
			// The PAN is stored; auto-verify is a best-effort demo convenience.
			// Surface the failure rather than silently returning a PENDING row.
			return nil, verr
		}
		slog.Info("pan auto-verified (demo mode)", "account_id", accountID)
		return verified, nil
	}
	return rec, nil
}

// VerifyPAN marks the given account's PAN as verified (admin action). The
// account must already have a KYC row (created via SubmitPAN).
func (s *KycService) VerifyPAN(ctx context.Context, accountID int64) (*models.KYCRecord, error) {
	rec, err := s.accounts.VerifyPAN(ctx, accountID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("No PAN on file for this account")
	}
	if err != nil {
		return nil, err
	}
	// Admin-gated action — worth recording who was verified, never the PAN.
	slog.Info("pan verified", "account_id", accountID)
	return rec, nil
}
