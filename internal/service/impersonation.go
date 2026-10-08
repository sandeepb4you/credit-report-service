package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// ImpersonationTTL is how long a "View as user" token works. Short on purpose:
// it is a support tool for looking at a screen, not a session, and it cannot be
// refreshed — a longer look is a second, separately audited view.
const ImpersonationTTL = 30 * time.Minute

// ImpersonationService issues an admin's read-only view of a customer's app.
//
// The view authenticates as the customer, so every route answers exactly what
// the customer would see — which is the point: a support question is about
// what is on their screen. Read-only is enforced on the server, not trusted to
// the app: middleware.ReadOnlyImpersonation refuses every request a view token
// makes other than a read, so nothing can be paid, pulled, submitted, edited or
// deleted on the customer's behalf.
//
// It never goes through AuthService.issueSession. That path is a sign-in — it
// writes a session row, and cancels a scheduled account deletion — and a view
// must do neither.
type ImpersonationService struct {
	accounts *repository.AccountRepo
	tokens   *TokenService
}

func NewImpersonationService(accounts *repository.AccountRepo, tokens *TokenService) *ImpersonationService {
	return &ImpersonationService{accounts: accounts, tokens: tokens}
}

// ImpersonationTarget is who a view is looking at, for the app's banner.
type ImpersonationTarget struct {
	ID    int64   `json:"id"`
	Name  string  `json:"name"`
	Phone *string `json:"phone,omitempty"`
	Email *string `json:"email,omitempty"`
}

// ImpersonationStarted is the response to opening a view.
type ImpersonationStarted struct {
	ImpersonationID int64               `json:"impersonationId"`
	Token           string              `json:"token"`
	ExpiresAt       time.Time           `json:"expiresAt"`
	Account         ImpersonationTarget `json:"account"`
}

// Start opens a read-only view of targetID for adminID.
//
// Only customer accounts can be viewed. An admin's or agent's view would carry
// their role, and a read through that role is a read of the console — so
// "View as" could become a way to borrow another operator's permissions.
func (s *ImpersonationService) Start(ctx context.Context, adminID, targetID int64) (*ImpersonationStarted, error) {
	if adminID == targetID {
		return nil, apperr.NewValidation("You cannot view your own account")
	}
	acc, err := s.accounts.FindByID(ctx, targetID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NewNotFound("No account with that id")
		}
		return nil, err
	}
	if models.NormalizeRole(acc.Role) != models.RoleUser {
		return nil, apperr.NewForbidden("Only customer accounts can be viewed")
	}
	if acc.Status == models.AccountDeleted {
		return nil, apperr.NewConflict("This account has been deleted")
	}
	expires := time.Now().UTC().Add(ImpersonationTTL)
	id, err := s.accounts.StartImpersonation(ctx, adminID, targetID, expires)
	if err != nil {
		return nil, err
	}
	tok, err := s.tokens.IssueImpersonation(targetID, acc.Role, acc.TokenEpoch, adminID, id, expires)
	if err != nil {
		return nil, err
	}
	slog.Info("admin view-as started",
		"impersonation_id", id, "admin_id", adminID, "account_id", targetID)
	return &ImpersonationStarted{
		ImpersonationID: id,
		Token:           tok.Token,
		ExpiresAt:       tok.ExpiresAt,
		Account: ImpersonationTarget{
			ID:    acc.ID,
			Name:  joinName(acc.FirstName, acc.LastName),
			Phone: acc.PrimaryPhone,
			Email: acc.PrimaryEmail,
		},
	}, nil
}

// End closes a view early, revoking its token.
func (s *ImpersonationService) End(ctx context.Context, adminID, impersonationID int64) error {
	if err := s.accounts.EndImpersonation(ctx, impersonationID, adminID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return apperr.NewNotFound("No such view")
		}
		return err
	}
	slog.Info("admin view-as ended", "impersonation_id", impersonationID, "admin_id", adminID)
	return nil
}

func joinName(first, last *string) string {
	var parts []string
	for _, p := range []*string{first, last} {
		if p != nil && strings.TrimSpace(*p) != "" {
			parts = append(parts, strings.TrimSpace(*p))
		}
	}
	return strings.Join(parts, " ")
}
