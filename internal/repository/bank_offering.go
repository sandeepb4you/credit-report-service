package repository

import (
	"context"
	"errors"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"credit-report-service/internal/models"
)

// BankOfferingRepo owns bank_offerings CRUD for the score-builder toolkit.
type BankOfferingRepo struct{ pool *pgxpool.Pool }

func NewBankOfferingRepo(pool *pgxpool.Pool) *BankOfferingRepo {
	return &BankOfferingRepo{pool: pool}
}

const bankOfferingCols = `id, name, product_type, min_fd_amount, interest_rate_percent,
    min_credit_score, max_credit_score, estimated_points_min, estimated_points_max,
    apply_url, revenue_note, active, created_at, updated_at`

// Create inserts an offering row and fills the server-assigned fields.
func (r *BankOfferingRepo) Create(ctx context.Context, o *models.BankOffering) error {
	err := pgxscan.Get(ctx, r.pool, o,
		`INSERT INTO bank_offerings
		     (name, product_type, min_fd_amount, interest_rate_percent,
		      min_credit_score, max_credit_score, estimated_points_min, estimated_points_max,
		      apply_url, revenue_note, active)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 RETURNING `+bankOfferingCols,
		o.Name, o.ProductType, o.MinFDAmount, o.InterestRatePercent,
		o.MinCreditScore, o.MaxCreditScore, o.EstimatedPointsMin, o.EstimatedPointsMax,
		o.ApplyURL, o.RevenueNote, o.Active,
	)
	return classifyPgErr(err)
}

// Update saves the mutable columns of an existing offering and returns the
// updated row, or ErrNotFound if the id does not exist.
func (r *BankOfferingRepo) Update(ctx context.Context, o *models.BankOffering) error {
	err := pgxscan.Get(ctx, r.pool, o,
		`UPDATE bank_offerings SET
		     name                  = $2,
		     product_type          = $3,
		     min_fd_amount         = $4,
		     interest_rate_percent = $5,
		     min_credit_score      = $6,
		     max_credit_score      = $7,
		     estimated_points_min  = $8,
		     estimated_points_max  = $9,
		     apply_url             = $10,
		     revenue_note          = $11,
		     active                = $12,
		     updated_at            = now()
		 WHERE id = $1
		 RETURNING `+bankOfferingCols,
		o.ID, o.Name, o.ProductType, o.MinFDAmount, o.InterestRatePercent,
		o.MinCreditScore, o.MaxCreditScore, o.EstimatedPointsMin, o.EstimatedPointsMax,
		o.ApplyURL, o.RevenueNote, o.Active,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return classifyPgErr(err)
}

// Delete removes an offering by id, returning ErrNotFound when nothing matched.
func (r *BankOfferingRepo) Delete(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM bank_offerings WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// FindByID returns a single offering, or ErrNotFound.
func (r *BankOfferingRepo) FindByID(ctx context.Context, id int64) (*models.BankOffering, error) {
	var o models.BankOffering
	err := pgxscan.Get(ctx, r.pool, &o,
		`SELECT `+bankOfferingCols+` FROM bank_offerings WHERE id = $1`, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// List returns offerings filtered by an optional product type and an optional
// active flag. A nil filter value means "any". Ordered by product type then
// estimated impact (highest first), mirroring how the toolkit surfaces them.
func (r *BankOfferingRepo) List(ctx context.Context, productType *string, active *bool) ([]models.BankOffering, error) {
	var os []models.BankOffering
	err := pgxscan.Select(ctx, r.pool, &os,
		`SELECT `+bankOfferingCols+` FROM bank_offerings
		 WHERE ($1::text IS NULL OR product_type = $1)
		   AND ($2::bool IS NULL OR active = $2)
		 ORDER BY product_type, estimated_points_max DESC, id`,
		productType, active)
	return os, err
}

// ListActiveForScore returns the active offerings of a given product type whose
// score band contains the given score, highest estimated impact first. This is
// the score-builder's candidate set.
func (r *BankOfferingRepo) ListActiveForScore(ctx context.Context, productType string, score int) ([]models.BankOffering, error) {
	var os []models.BankOffering
	err := pgxscan.Select(ctx, r.pool, &os,
		`SELECT `+bankOfferingCols+` FROM bank_offerings
		 WHERE product_type = $1 AND active = TRUE
		   AND min_credit_score <= $2
		   AND max_credit_score >= $2
		 ORDER BY estimated_points_max DESC, id`, productType, score)
	return os, err
}
