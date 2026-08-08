package service

import (
	"context"
	"errors"
	"fmt"
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
		// Reviewer 0: no human approved this, so the row records a NULL reviewer
		// with a timestamp — distinguishable from an admin's decision.
		verified, verr := s.accounts.VerifyPAN(ctx, accountID, 0)
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

// Status reports the account's KYC state. An account that has never submitted
// a PAN is not an error — it reports KycNotSubmitted — so the client can drive
// its onboarding screen off a 200 in every case rather than treating a 404 as
// a state. The full PAN is deliberately not returned; see models.KYCStatus.
func (s *KycService) Status(ctx context.Context, accountID int64) (models.KYCStatus, error) {
	rec, err := s.accounts.FindKYCByAccount(ctx, accountID)
	if errors.Is(err, repository.ErrNotFound) {
		return models.NewKYCStatus(nil), nil
	}
	if err != nil {
		return models.KYCStatus{}, err
	}
	return models.NewKYCStatus(rec), nil
}

// Bounds on the admin review queue page size. A default keeps an unbounded
// queue from being served in one response; the max caps what a caller can ask
// for. Both are reflected back in KYCReviewPage.Limit so a client can tell
// what it actually got.
const (
	kycQueueDefaultLimit = 50
	kycQueueMaxLimit     = 200
)

// ListPending returns the KYC review queue — accounts that submitted a PAN and
// are waiting on verification — newest activity first. Callers must already
// have been gated on models.PermKycVerify: the rows carry full PANs.
//
// limit <= 0 means the default; anything above kycQueueMaxLimit is clamped to
// it rather than rejected, and the value actually used comes back on the page.
func (s *KycService) ListPending(ctx context.Context, limit, offset int) (*models.KYCReviewPage, error) {
	limit, offset = clampQueuePage(limit, offset)

	items, err := s.accounts.ListKYCByStatus(ctx, models.KycPending, limit, offset)
	if err != nil {
		return nil, err
	}
	total, err := s.accounts.CountKYCByStatus(ctx, models.KycPending)
	if err != nil {
		return nil, err
	}
	return &models.KYCReviewPage{Items: items, Total: total, Limit: limit, Offset: offset}, nil
}

// clampQueuePage normalizes a requested page into one the queue will serve:
// an unset or nonsensical limit becomes the default, an oversized one is
// capped, and a negative offset is treated as the first page.
func clampQueuePage(limit, offset int) (int, int) {
	switch {
	case limit <= 0:
		limit = kycQueueDefaultLimit
	case limit > kycQueueMaxLimit:
		limit = kycQueueMaxLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// VerifyPAN marks the given account's PAN as verified (admin action). The
// account must already have a KYC row (created via SubmitPAN).
//
// reviewerID is the admin making the call; it is recorded on the row so a
// verification can be traced back to whoever approved it.
func (s *KycService) VerifyPAN(ctx context.Context, accountID, reviewerID int64) (*models.KYCRecord, error) {
	rec, err := s.accounts.VerifyPAN(ctx, accountID, reviewerID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("No PAN on file for this account")
	}
	if err != nil {
		return nil, err
	}
	// Admin-gated action — record who verified whom, never the PAN.
	slog.Info("pan verified", "account_id", accountID, "reviewer_id", reviewerID)
	return rec, nil
}

// maxRejectionReasonLen bounds the reviewer's note. The column is TEXT so the
// database would take anything; the cap keeps a pasted document out of a field
// the applicant is going to be shown.
const maxRejectionReasonLen = 500

// validateRejectionReason trims and checks a reviewer's note, returning the
// value to store. Measured in runes, not bytes, so a reason written in an
// Indian script is not cut short at a third of the visible length.
func validateRejectionReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	switch {
	case reason == "":
		return "", apperr.NewValidationWith("Validation failed",
			map[string]string{"reason": "reason is required"})
	case len([]rune(reason)) > maxRejectionReasonLen:
		return "", apperr.NewValidationWith("Validation failed",
			map[string]string{"reason": fmt.Sprintf(
				"reason must be at most %d characters", maxRejectionReasonLen)})
	}
	return reason, nil
}

// RejectPAN marks the given account's KYC as rejected (admin action). The
// reason is required — a rejection the applicant cannot act on just produces a
// support ticket — and is surfaced to them via GET /api/kyc/status.
//
// Rejecting an already-verified account is allowed and withdraws its access:
// an admin who approved by mistake needs a way back.
func (s *KycService) RejectPAN(ctx context.Context, accountID int64, reason string, reviewerID int64) (*models.KYCRecord, error) {
	reason, err := validateRejectionReason(reason)
	if err != nil {
		return nil, err
	}

	rec, err := s.accounts.RejectPAN(ctx, accountID, reason, reviewerID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.NewNotFound("No PAN on file for this account")
	}
	if err != nil {
		return nil, err
	}
	// The reason is reviewer-authored and may quote the submission, so it is
	// not logged — only the fact of the rejection. Never the PAN.
	slog.Info("pan rejected", "account_id", accountID, "reviewer_id", reviewerID)
	return rec, nil
}
