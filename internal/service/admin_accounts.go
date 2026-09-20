package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

const (
	// AccountsDefaultWindowDays matches the referral report's default: the
	// console opens on the last 30 days of signups rather than the whole
	// table, because "who joined recently" is the question it is opened with.
	AccountsDefaultWindowDays = 30
	accountsMaxWindowDays     = 3660 // ten years: effectively "all time"
	accountsDefaultPage       = 50
	accountsMaxPage           = 200
	// AccountsRecentChecks is how many past score checks the detail view
	// carries. Five, per the console's spec — enough to see a trend and a
	// recent failure, short enough to read without paging.
	AccountsRecentChecks = 5
)

// AccountQuery is one page of the admin user list: the window, the per-column
// filters and the sort.
//
// The nil-able fields are absent filters rather than zero ones — "referred at
// least 0 people" is every account, and is a different request from not
// filtering at all, which is why they are pointers and not ints.
type AccountQuery struct {
	From   time.Time
	To     time.Time
	Status string // "" = every status
	// Search matches name, phone or email, case-insensitively and anywhere in
	// the value. One box for three columns because an operator with a phone in
	// their hand should not have to say which column it is.
	Search       string
	Paid         *bool
	MinScore     *int
	MaxScore     *int
	MinReferred  *int
	MinConverted *int
	// Sort names a column from models.SortableAccountColumns; empty is the
	// default (newest signup first).
	Sort string
	Desc bool
	Limit  int
	Offset int
}

// AdminAccountsService reads the operator's customer list. Read-only: nothing
// here writes, which is why it is separate from AccountResetService even
// though both are admin account tools.
type AdminAccountsService struct {
	accounts *repository.AccountRepo
}

func NewAdminAccountsService(accounts *repository.AccountRepo) *AdminAccountsService {
	return &AdminAccountsService{accounts: accounts}
}

// List returns a page of accounts that signed up inside the window.
func (s *AdminAccountsService) List(ctx context.Context, q AccountQuery) (*models.AdminAccountPage, error) {
	from, to, err := resolveAccountWindow(q.From, q.To)
	if err != nil {
		return nil, err
	}

	status, err := normalizeAccountStatus(q.Status)
	if err != nil {
		return nil, err
	}

	limit := q.Limit
	if limit <= 0 {
		limit = accountsDefaultPage
	}
	if limit > accountsMaxPage {
		limit = accountsMaxPage
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}

	sort, ok := models.ParseAccountSort(q.Sort)
	if !ok {
		return nil, apperr.NewValidationWith("Validation failed", map[string]string{
			"sort": "expected one of " + strings.Join(models.SortableAccountColumns(), ", "),
		})
	}

	if q.MinScore != nil && q.MaxScore != nil && *q.MinScore > *q.MaxScore {
		return nil, apperr.NewValidationWith("Validation failed", map[string]string{
			"maxScore": "the highest score cannot be below the lowest",
		})
	}

	// The window is half-open internally: [from, to+1d). Callers speak in
	// inclusive days, so a list "to 30 Aug" must include the whole of the 30th.
	end := to.AddDate(0, 0, 1)

	rows, total, err := s.accounts.ListAccounts(ctx, repository.AccountListFilter{
		From:         from,
		To:           end,
		Status:       status,
		Search:       trimmedPtr(q.Search),
		Paid:         q.Paid,
		MinScore:     q.MinScore,
		MaxScore:     q.MaxScore,
		MinReferred:  q.MinReferred,
		MinConverted: q.MinConverted,
		OrderBy:      sort.SQL(q.Desc),
		Limit:        limit,
		Offset:       offset,
	})
	if err != nil {
		return nil, err
	}
	return &models.AdminAccountPage{Items: rows, Total: total, Limit: limit, Offset: offset}, nil
}

// Detail returns one account with its KYC record and recent score checks.
func (s *AdminAccountsService) Detail(ctx context.Context, accountID int64) (*models.AdminAccountDetail, error) {
	account, err := s.accounts.FindByID(ctx, accountID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NewNotFound("No account with that id")
		}
		return nil, err
	}

	// A missing KYC record is a state, not a failure: most accounts reach the
	// dashboard without one. It renders as "not submitted".
	kyc, err := s.accounts.FindKYCByAccount(ctx, accountID)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return nil, err
	}

	checks, err := s.accounts.ListRecentChecks(ctx, accountID, AccountsRecentChecks)
	if err != nil {
		return nil, err
	}

	return &models.AdminAccountDetail{Account: account, KYC: kyc, Checks: checks}, nil
}

// trimmedPtr turns a blank search box into no filter at all, rather than a
// LIKE '%%' the database has to evaluate per row.
func trimmedPtr(raw string) *string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return nil
	}
	return &v
}

// normalizeAccountStatus upper-cases the filter and refuses one the column can
// never hold, rather than quietly returning an empty list — a typo'd status
// that renders as "no users" reads as a data problem.
//
// INACTIVE is not a stored status: it means "everything that is not ACTIVE",
// which is the split the console actually offers. Expressing it here rather
// than making the client send PENDING and SUSPENDED separately keeps the two
// halves exhaustive — a status added to the column later lands on the inactive
// side by default, which is the safe way round for a list of half-registered
// accounts.
func normalizeAccountStatus(raw string) (*string, error) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if s == "" {
		return nil, nil
	}
	switch s {
	case models.AccountActive, models.AccountPending, models.AccountSuspended, models.AccountInactive:
		return &s, nil
	default:
		return nil, apperr.NewValidationWith("Validation failed", map[string]string{
			"status": "expected ACTIVE, INACTIVE, PENDING or SUSPENDED",
		})
	}
}

// resolveAccountWindow fills in whichever bound was left off, defaulting to
// the last 30 whole UTC days. Same shape and same UTC bucketing as the
// referral report, so two admin screens filtered to "last 30 days" cover the
// same days.
func resolveAccountWindow(from, to time.Time) (time.Time, time.Time, error) {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	switch {
	case from.IsZero() && to.IsZero():
		to = today
		from = to.AddDate(0, 0, -(AccountsDefaultWindowDays - 1))
	case from.IsZero():
		from = to.AddDate(0, 0, -(AccountsDefaultWindowDays - 1))
	case to.IsZero():
		to = today
	}
	if to.Before(from) {
		return time.Time{}, time.Time{}, apperr.NewValidationWith("Validation failed", map[string]string{
			"to": "the end date cannot be before the start date",
		})
	}
	if to.Sub(from) > accountsMaxWindowDays*24*time.Hour {
		return time.Time{}, time.Time{}, apperr.NewValidationWith("Validation failed", map[string]string{
			"from": "the range cannot be longer than ten years",
		})
	}
	return from, to, nil
}
