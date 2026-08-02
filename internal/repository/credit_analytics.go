package repository

import (
	"context"
	"errors"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"credit-report-service/internal/models"
)

// CreditAnalyticsRepo owns credit_analytics_requests queries.
type CreditAnalyticsRepo struct{ pool *pgxpool.Pool }

func NewCreditAnalyticsRepo(pool *pgxpool.Pool) *CreditAnalyticsRepo {
	return &CreditAnalyticsRepo{pool: pool}
}

const creditAnalyticsCols = `id, account_id, client_ref_num, mobile_no,
    request_id, result_code, http_status, message,
    request_body, response_body, credit_score, created_at`

// Create inserts a credit-analytics request row and fills the server-assigned
// fields (id, created_at) on the supplied model.
func (r *CreditAnalyticsRepo) Create(ctx context.Context, req *models.CreditAnalyticsRequest) error {
	return pgxscan.Get(ctx, r.pool, req,
		`INSERT INTO credit_analytics_requests
		     (account_id, client_ref_num, mobile_no, request_id, result_code,
		      http_status, message, request_body, response_body, credit_score)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING `+creditAnalyticsCols,
		req.AccountID, req.ClientRefNum, req.MobileNo, req.RequestID, req.ResultCode,
		req.HTTPStatus, req.Message, req.RequestBody, req.ResponseBody, req.CreditScore,
	)
}

// FindByID returns a single row by id, or ErrNotFound.
func (r *CreditAnalyticsRepo) FindByID(ctx context.Context, id int64) (*models.CreditAnalyticsRequest, error) {
	var req models.CreditAnalyticsRequest
	err := pgxscan.Get(ctx, r.pool, &req,
		`SELECT `+creditAnalyticsCols+` FROM credit_analytics_requests WHERE id = $1`, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &req, nil
}

// FindByAccountPaged returns one page of an account's rows, newest first.
// limit is the page size; offset is the zero-based row offset.
func (r *CreditAnalyticsRepo) FindByAccountPaged(ctx context.Context, accountID int64, limit, offset int) ([]models.CreditAnalyticsRequest, error) {
	var rs []models.CreditAnalyticsRequest
	err := pgxscan.Select(ctx, r.pool, &rs,
		`SELECT `+creditAnalyticsCols+` FROM credit_analytics_requests
		 WHERE account_id = $1
		 ORDER BY id DESC
		 LIMIT $2 OFFSET $3`, accountID, limit, offset)
	return rs, err
}

// CountByAccount returns the total number of rows for an account (for the
// pagination total field).
func (r *CreditAnalyticsRepo) CountByAccount(ctx context.Context, accountID int64) (int64, error) {
	var n int64
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM credit_analytics_requests WHERE account_id = $1`, accountID).Scan(&n)
	return n, err
}

// FindLatestByAccount returns the most recent successful report (2xx upstream)
// for an account, or ErrNotFound.
func (r *CreditAnalyticsRepo) FindLatestByAccount(ctx context.Context, accountID int64) (*models.CreditAnalyticsRequest, error) {
	var req models.CreditAnalyticsRequest
	err := pgxscan.Get(ctx, r.pool, &req,
		`SELECT `+creditAnalyticsCols+` FROM credit_analytics_requests
		 WHERE account_id = $1
		   AND http_status >= 200 AND http_status < 300
		   AND response_body IS NOT NULL
		 ORDER BY id DESC
		 LIMIT 1`, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &req, err
}
