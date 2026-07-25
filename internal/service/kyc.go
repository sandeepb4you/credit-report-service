package service

import (
	"context"
	"errors"
	"strings"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// KycService implements the PAN submission endpoint. PAN authenticity against
// the income-tax database is out of scope here; this only accepts, formats,
// and stores the number. A VERIFIED row (set by a separate KYC-provider flow)
// is what gates the analysis products.
type KycService struct {
	accounts *repository.AccountRepo
}

func NewKycService(accounts *repository.AccountRepo) *KycService {
	return &KycService{accounts: accounts}
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
			return nil, apperr.NewConflict("PAN is already linked to another account")
		}
		return nil, err
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
	return rec, nil
}
