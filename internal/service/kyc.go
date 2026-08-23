package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/config"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// KycService implements PAN submission and verification.
//
// Verification asks Digitap's Mobile to Prefill API whether the submitted PAN
// and name are the ones registered against the account's mobile number — the
// number the user has already proved control of over SMS OTP. That is a link
// check between three facts, not proof the PAN exists at the income-tax
// department, and the wording shown to users is chosen to not overclaim.
//
// It replaces the previous flow, where a submission sat PENDING until an admin
// approved it by hand. The admin verify/reject endpoints remain as a manual
// override for the cases automation cannot settle.
//
// In demo mode no provider is called and a submitted PAN is auto-verified.
// This flag must stay false in production.
type KycService struct {
	accounts *repository.AccountRepo
	verifier *PrefillVerifier
	cfg      config.PANConfig
	demoMode bool
}

func NewKycService(
	accounts *repository.AccountRepo,
	verifier *PrefillVerifier,
	cfg config.PANConfig,
	demoMode bool,
) *KycService {
	return &KycService{accounts: accounts, verifier: verifier, cfg: cfg, demoMode: demoMode}
}

// SubmitPAN validates the submitted PAN and name, then verifies them against
// the account's mobile number through the prefill provider.
//
// Outcomes, all of which persist the PAN so a support agent can see what was
// tried:
//
//   - match          -> VERIFIED, provider recorded, the caller proceeds
//   - mismatch       -> 422 with what to fix, attempt counted, retry allowed
//   - attempts spent -> 422 telling them to contact support; further tries are
//     refused until the PAN changes, because PAN-plus-name is guessable for a
//     known person and an uncapped loop is a brute-force oracle we pay for
//   - provider gap   -> PENDING, no error: the provider had no record to
//     compare against (102/103/503), which says nothing about whether the user
//     is honest. Blocking here would strand every legitimate user the provider
//     cannot see, so the record stays for the admin queue to settle.
func (s *KycService) SubmitPAN(ctx context.Context, accountID int64, pan, fullName string) (*models.KYCRecord, error) {
	pan = strings.ToUpper(strings.TrimSpace(pan))
	fullName = strings.Join(strings.Fields(fullName), " ")

	fields := map[string]string{}
	if !panFormat.MatchString(pan) {
		fields["pan"] = "PAN must be 5 letters, 4 digits, 1 letter (e.g. ABCDE1234F)"
	}
	if len([]rune(fullName)) < 2 {
		fields["fullName"] = "Enter your full name exactly as printed on your PAN card"
	}
	if len(fields) > 0 {
		return nil, apperr.NewValidationWith("Validation failed", fields)
	}

	acc, err := s.accounts.FindByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	// The check is "does this PAN belong to the number you verified", so with
	// no number on the account there is nothing to check it against. Only an
	// email-only signup reaches this.
	if acc.PrimaryPhone == nil || strings.TrimSpace(*acc.PrimaryPhone) == "" {
		return nil, apperr.NewPanFailure(
			"Add and verify a mobile number before submitting your PAN")
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
	slog.Info("pan submitted", "account_id", accountID)

	// Demo mode: no provider call at all. Never enabled in production.
	if s.demoMode {
		verified, verr := s.accounts.MarkPANVerifiedByProvider(ctx, accountID, fullName, "demo", "")
		if verr != nil {
			return nil, verr
		}
		slog.Info("pan auto-verified (demo mode)", "account_id", accountID)
		return verified, nil
	}

	// UpsertPAN resets the counter whenever the PAN changes, so a user who
	// mistyped once is not punished for correcting it.
	if max := s.maxAttempts(); rec.VerificationAttempts >= max {
		slog.Warn("pan verification refused: attempt cap reached",
			"account_id", accountID, "attempts", rec.VerificationAttempts)
		return nil, apperr.NewPanFailure(
			"Too many failed verification attempts. Please contact support.")
	}

	verdict, err := s.verifier.Verify(ctx, accountID, *acc.PrimaryPhone, pan, fullName)
	// Record the call before acting on it, and on the error path too: a lookup
	// that failed is exactly the one support will be asked about later.
	s.recordLookup(ctx, accountID, verdict)
	if err != nil {
		// Provider misconfiguration (bad credentials, service not provisioned).
		// The PAN is stored; report it as a service failure rather than as the
		// user's mistake, which is what a PanFailure would imply.
		return nil, fmt.Errorf("verify pan: %w", err)
	}

	switch {
	case verdict.Verified:
		provider := "digitap-prefill"
		if s.verifier.IsStub() {
			// Mark stub-verified rows so a database full of VERIFIED accounts
			// from a credential-less environment can never be mistaken for
			// people a provider actually confirmed.
			provider = "stub"
		}
		// Store the provider's spelling of the name, not the user's: it is the
		// one that matches the bureau record downstream.
		name := verdict.ProviderName
		if name == "" {
			name = fullName
		}
		verified, verr := s.accounts.MarkPANVerifiedByProvider(ctx, accountID, name, provider, verdict.ProviderRef)
		if verr != nil {
			return nil, verr
		}
		slog.Info("pan verified by provider",
			"account_id", accountID, "provider", provider, "provider_ref", verdict.ProviderRef)

		// The provider just told us this person's name, which is exactly what
		// the onboarding profile form would ask for next. Filling it here is
		// what lets a phone signup land on the dashboard instead of stopping to
		// retype a name we already hold. Best-effort: a failure here must not
		// undo a verification that succeeded.
		first, last := splitName(name)
		if nerr := s.accounts.FillNamesIfEmpty(ctx, accountID, first, last); nerr != nil {
			slog.Warn("could not fill profile name after pan verification",
				"account_id", accountID, "error", nerr)
		}
		return verified, nil

	case verdict.ProviderGap:
		slog.Info("pan stored unverified: provider had nothing to compare",
			"account_id", accountID, "reason", verdict.Reason)
		return rec, nil

	default:
		attempts, aerr := s.accounts.RecordPANVerificationAttempt(ctx, accountID, verdict.ProviderRef)
		if aerr != nil {
			return nil, aerr
		}
		slog.Info("pan verification failed", "account_id", accountID, "attempts", attempts)
		remaining := s.maxAttempts() - attempts
		if remaining <= 0 {
			return nil, apperr.NewPanFailure(
				"Too many failed verification attempts. Please contact support.")
		}
		return nil, apperr.NewPanFailure(verdict.Reason)
	}
}

// recordLookup stores one provider call for audit. Best-effort: losing the row
// must not fail a verification the provider already answered, so a failure is
// logged and swallowed.
func (s *KycService) recordLookup(ctx context.Context, accountID int64, v PrefillVerdict) {
	row := &models.PrefillLookup{
		AccountID:   accountID,
		PANMatched:  v.PANMatched,
		NameMatched: v.NameMatched,
		Verified:    v.Verified,
		ProviderGap: v.ProviderGap,
		ResponseRaw: v.ResultJSON,
	}
	if v.ProviderRef != "" {
		row.RequestID = &v.ProviderRef
	}
	if v.ClientRef != "" {
		row.ClientRef = &v.ClientRef
	}
	if v.ResultCode != 0 {
		code := v.ResultCode
		row.ResultCode = &code
	}
	if v.Message != "" {
		msg := v.Message
		if len(msg) > 255 {
			msg = msg[:255]
		}
		row.Message = &msg
	}
	if err := s.accounts.RecordPrefillLookup(ctx, row); err != nil {
		slog.Warn("could not record prefill lookup", "account_id", accountID, "error", err)
	}
}

// splitName divides a full name into the first/last pair the accounts table
// holds: first word, then everything after it. Indian names frequently carry a
// middle name or a patronymic, and keeping the remainder together preserves it
// rather than silently dropping part of someone's name.
func splitName(full string) (first, last string) {
	fields := strings.Fields(full)
	switch len(fields) {
	case 0:
		return "", ""
	case 1:
		return fields[0], ""
	default:
		return fields[0], strings.Join(fields[1:], " ")
	}
}

// maxAttempts is the failed-verification cap, defaulting to 3 when unset so a
// zero-valued config cannot silently disable the brake.
func (s *KycService) maxAttempts() int {
	if s.cfg.MaxVerificationAttempts > 0 {
		return s.cfg.MaxVerificationAttempts
	}
	return 3
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
