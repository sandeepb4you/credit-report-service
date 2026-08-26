// Package service — the admin action that walks an account back to signup.
//
// This exists to test onboarding repeatedly against a real account rather than
// inventing a new phone number every time. It is enabled in every environment,
// production included, because a test account on the deployed app is exactly
// the case it is for — which is also why it asks for the account's own phone or
// email as confirmation and logs every use.
package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
)

// accountResetStore is the repository surface the reset needs. Narrow on
// purpose: the interesting behaviour here is the confirmation and the object
// cleanup, and both are testable without a database behind them.
type accountResetStore interface {
	FindByID(ctx context.Context, id int64) (*models.Account, error)
	FindByEmail(ctx context.Context, email string) (*models.Account, error)
	FindByPhone(ctx context.Context, phone string) (*models.Account, error)
	SignupResetPreview(ctx context.Context, accountID int64) (*models.AccountResetCounts, error)
	ResetToSignup(ctx context.Context, accountID int64) (*models.AccountResetResult, error)
}

// reportPDFRemover deletes stored report PDFs. Optional: with no object store
// configured the rows still go, and the orphaned files are logged rather than
// silently forgotten.
type reportPDFRemover interface {
	Delete(ctx context.Context, keyOrURI string) error
	IsStub() bool
}

// AccountResetService resolves an account by contact detail and resets it.
type AccountResetService struct {
	accounts accountResetStore
	pdfStore reportPDFRemover
}

func NewAccountResetService(accounts accountResetStore) *AccountResetService {
	return &AccountResetService{accounts: accounts}
}

// SetPDFStore wires report-PDF deletion. Without it a reset leaves the
// encrypted files in the bucket, which is why the absence is logged loudly:
// the report row is gone, so nothing else will ever go looking for them.
func (s *AccountResetService) SetPDFStore(store reportPDFRemover) {
	s.pdfStore = store
}

// AccountOverview is an account plus what a reset would remove from it.
type AccountOverview struct {
	Account *models.Account
	Counts  *models.AccountResetCounts
}

// Lookup finds an account by phone number or email address.
//
// By contact detail rather than by id because that is what an admin has in
// front of them, and because the id alone is a single fat-fingered digit away
// from somebody else's account. The reset then asks for the same detail back,
// so both halves have to agree before anything is deleted.
func (s *AccountResetService) Lookup(ctx context.Context, identifier string) (*AccountOverview, error) {
	acc, err := s.findByIdentifier(ctx, identifier)
	if err != nil {
		return nil, err
	}
	counts, err := s.accounts.SignupResetPreview(ctx, acc.ID)
	if err != nil {
		return nil, apperr.NewNotFound("Account not found")
	}
	return &AccountOverview{Account: acc, Counts: counts}, nil
}

// Reset puts the account back to its post-signup state.
//
// confirm must be the account's own registered phone or email. It is not
// security — the caller is already an admin — it is the check that catches the
// wrong account, which on this endpoint costs somebody their paid reports.
func (s *AccountResetService) Reset(
	ctx context.Context, actorID, targetID int64, confirm string,
) (*models.AccountResetResult, error) {
	acc, err := s.accounts.FindByID(ctx, targetID)
	if err != nil {
		return nil, apperr.NewNotFound("Account not found")
	}
	if !accountMatchesIdentifier(acc, confirm) {
		return nil, apperr.NewValidationWith("Validation failed", map[string]string{
			"confirm": "Type the phone number or email address registered on this account.",
		})
	}

	result, err := s.accounts.ResetToSignup(ctx, targetID)
	if err != nil {
		slog.Error("account reset failed",
			"actor_id", actorID, "target_id", targetID, "error", err)
		return nil, fmt.Errorf("reset account %d: %w", targetID, err)
	}

	// Deliberately loud, and at WARN: this is the one action in the service that
	// destroys paid-for data, so it should be findable in a log without knowing
	// to look for it. Contact details are not repeated here — the account id is
	// enough to follow it up, and the log is not the place for a phone number.
	slog.Warn("account reset to signup",
		"actor_id", actorID,
		"target_id", targetID,
		"reports", result.Removed.Reports,
		"orders", result.Removed.Orders,
		"paid_orders", result.Removed.PaidOrders,
		"statements", result.Removed.BankStatements,
		"sessions_revoked", result.Removed.ActiveSessions,
		"token_epoch", result.TokenEpoch,
	)

	s.deleteReportPDFs(ctx, targetID, result.PDFObjectURIs)
	return result, nil
}

// deleteReportPDFs removes the stored files behind the reports just deleted.
//
// Best-effort and after the commit: a bucket that is down must not undo a reset
// that has already happened, and a failure here leaves an unreachable encrypted
// file rather than a broken account. Both outcomes are logged, because "the
// report is gone but its PDF is still in the bucket" is exactly the kind of
// leftover nobody discovers on their own.
func (s *AccountResetService) deleteReportPDFs(ctx context.Context, accountID int64, uris []string) {
	if len(uris) == 0 {
		return
	}
	if s.pdfStore == nil || s.pdfStore.IsStub() {
		slog.Warn("account reset left report PDFs in place (no object store configured)",
			"account_id", accountID, "objects", len(uris))
		return
	}
	for _, uri := range uris {
		if err := s.pdfStore.Delete(ctx, uri); err != nil {
			slog.Error("account reset could not delete a report PDF",
				"account_id", accountID, "uri", uri, "error", err)
		}
	}
}

// findByIdentifier resolves an email address or a mobile number, normalising
// each the way its own sign-in path does so a lookup matches what signup stored.
func (s *AccountResetService) findByIdentifier(ctx context.Context, identifier string) (*models.Account, error) {
	id := strings.TrimSpace(identifier)
	if id == "" {
		return nil, apperr.NewValidationWith("Validation failed", map[string]string{
			"identifier": "Enter the account's phone number or email address.",
		})
	}
	if strings.Contains(id, "@") {
		acc, err := s.accounts.FindByEmail(ctx, normalizeEmail(id))
		if err != nil {
			return nil, apperr.NewNotFound("No account with that email address")
		}
		return acc, nil
	}
	phone, err := normalizePhone(id)
	if err != nil {
		return nil, err
	}
	acc, err := s.accounts.FindByPhone(ctx, phone)
	if err != nil {
		return nil, apperr.NewNotFound("No account with that phone number")
	}
	return acc, nil
}

// accountMatchesIdentifier reports whether confirm names this account's own
// phone or email. An unparseable phone simply does not match — the caller gets
// the same "type the registered detail" message either way.
func accountMatchesIdentifier(acc *models.Account, confirm string) bool {
	c := strings.TrimSpace(confirm)
	if c == "" {
		return false
	}
	if acc.PrimaryEmail != nil && *acc.PrimaryEmail != "" &&
		normalizeEmail(c) == normalizeEmail(*acc.PrimaryEmail) {
		return true
	}
	if acc.PrimaryPhone != nil && *acc.PrimaryPhone != "" {
		if phone, err := normalizePhone(c); err == nil && phone == *acc.PrimaryPhone {
			return true
		}
	}
	return false
}
