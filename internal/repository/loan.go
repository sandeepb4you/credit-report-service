package repository

import (
	"context"
	"errors"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"credit-report-service/internal/models"
)

// LoanProviderRepo owns loan_providers CRUD and the single-row
// loan_switch_settings config.
type LoanProviderRepo struct{ pool *pgxpool.Pool }

func NewLoanProviderRepo(pool *pgxpool.Pool) *LoanProviderRepo {
	return &LoanProviderRepo{pool: pool}
}

const loanProviderCols = `id, name, loan_type, interest_rate_percent,
    processing_fee_percent, processing_fee_flat, min_credit_score,
    max_tenure_months, active, created_at, updated_at`

// Create inserts a provider row and fills the server-assigned fields.
func (r *LoanProviderRepo) Create(ctx context.Context, p *models.LoanProvider) error {
	err := pgxscan.Get(ctx, r.pool, p,
		`INSERT INTO loan_providers
		     (name, loan_type, interest_rate_percent, processing_fee_percent,
		      processing_fee_flat, min_credit_score, max_tenure_months, active)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING `+loanProviderCols,
		p.Name, p.LoanType, p.InterestRatePercent, p.ProcessingFeePercent,
		p.ProcessingFeeFlat, p.MinCreditScore, p.MaxTenureMonths, p.Active,
	)
	return classifyPgErr(err)
}

// Update saves the mutable columns of an existing provider and returns the
// updated row, or ErrNotFound if the id does not exist.
func (r *LoanProviderRepo) Update(ctx context.Context, p *models.LoanProvider) error {
	err := pgxscan.Get(ctx, r.pool, p,
		`UPDATE loan_providers SET
		     name                   = $2,
		     loan_type              = $3,
		     interest_rate_percent  = $4,
		     processing_fee_percent = $5,
		     processing_fee_flat    = $6,
		     min_credit_score       = $7,
		     max_tenure_months      = $8,
		     active                 = $9,
		     updated_at             = now()
		 WHERE id = $1
		 RETURNING `+loanProviderCols,
		p.ID, p.Name, p.LoanType, p.InterestRatePercent, p.ProcessingFeePercent,
		p.ProcessingFeeFlat, p.MinCreditScore, p.MaxTenureMonths, p.Active,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return classifyPgErr(err)
}

// Delete removes a provider by id, returning ErrNotFound when nothing matched.
func (r *LoanProviderRepo) Delete(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM loan_providers WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// FindByID returns a single provider, or ErrNotFound.
func (r *LoanProviderRepo) FindByID(ctx context.Context, id int64) (*models.LoanProvider, error) {
	var p models.LoanProvider
	err := pgxscan.Get(ctx, r.pool, &p,
		`SELECT `+loanProviderCols+` FROM loan_providers WHERE id = $1`, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// List returns providers filtered by an optional loan type and an optional
// active flag. A nil filter value means "any". Ordered by loan type then rate.
func (r *LoanProviderRepo) List(ctx context.Context, loanType *string, active *bool) ([]models.LoanProvider, error) {
	var ps []models.LoanProvider
	err := pgxscan.Select(ctx, r.pool, &ps,
		`SELECT `+loanProviderCols+` FROM loan_providers
		 WHERE ($1::text IS NULL OR loan_type = $1)
		   AND ($2::bool IS NULL OR active = $2)
		 ORDER BY loan_type, interest_rate_percent, id`,
		loanType, active)
	return ps, err
}

// ListActiveByType returns the active providers for one loan type, cheapest
// rate first. This is the optimizer's candidate set.
func (r *LoanProviderRepo) ListActiveByType(ctx context.Context, loanType string) ([]models.LoanProvider, error) {
	var ps []models.LoanProvider
	err := pgxscan.Select(ctx, r.pool, &ps,
		`SELECT `+loanProviderCols+` FROM loan_providers
		 WHERE loan_type = $1 AND active = TRUE
		 ORDER BY interest_rate_percent, id`, loanType)
	return ps, err
}

// ---- loan_switch_settings (singleton) -----------------------------------

const loanSwitchSettingsCols = `id, recovery_window_months,
    foreclosure_fee_percent_home, foreclosure_fee_percent_personal,
    foreclosure_fee_percent_car, updated_at`

// GetSettings returns the single settings row. The row is seeded by the
// migration, so a missing row is an operational fault rather than a normal case.
func (r *LoanProviderRepo) GetSettings(ctx context.Context) (*models.LoanSwitchSettings, error) {
	var s models.LoanSwitchSettings
	err := pgxscan.Get(ctx, r.pool, &s,
		`SELECT `+loanSwitchSettingsCols+` FROM loan_switch_settings WHERE id = 1`)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// UpdateSettings writes the configurable fields of the singleton row and
// returns the updated values.
func (r *LoanProviderRepo) UpdateSettings(ctx context.Context, s *models.LoanSwitchSettings) error {
	err := pgxscan.Get(ctx, r.pool, s,
		`UPDATE loan_switch_settings SET
		     recovery_window_months           = $1,
		     foreclosure_fee_percent_home     = $2,
		     foreclosure_fee_percent_personal = $3,
		     foreclosure_fee_percent_car      = $4,
		     updated_at                       = now()
		 WHERE id = 1
		 RETURNING `+loanSwitchSettingsCols,
		s.RecoveryWindowMonths, s.ForeclosureFeeHome,
		s.ForeclosureFeePersonal, s.ForeclosureFeeCar,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return classifyPgErr(err)
}
