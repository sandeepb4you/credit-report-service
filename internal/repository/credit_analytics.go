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
    request_body, response_body, credit_score, result_pdf_url, created_at`

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

// SetResultPDFURL writes the stored object's s3:// URI onto a row once the
// async download+upload completes. Idempotent: re-writing the same value is a
// no-op.
// Used by the best-effort ReportUploader; a failure here just leaves the column
// null (the raw response_body still has Digitap's source URL).
func (r *CreditAnalyticsRepo) SetResultPDFURL(ctx context.Context, id int64, url string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE credit_analytics_requests SET result_pdf_url = $2 WHERE id = $1`,
		id, url)
	return err
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
	rs := []models.CreditAnalyticsRequest{}
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

// FindLatestByAccount returns the report an account's analysis should be built
// from: the newest successful (2xx upstream) pull that actually carries a
// score, falling back to the newest successful pull when none of them do.
//
// It is deliberately not simply "the newest row". The bureau sometimes answers
// 200 with a degraded INProfileResponse — no SCORE block and a truncated
// account list — and such a response, being newest, would shadow a complete
// report taken seconds earlier. That is not a hypothetical: it left an account
// whose report had a score and three tradelines showing the "you have no score
// yet" paywall, because a retry one second later came back score-less.
//
// A missing SCORE block is a data-quality event, not the user's score going
// away, so preferring the last real one is the honest reading. Staleness is
// handled separately: ReportInsights.Outdated still flags anything past the
// freshness window, so an older-but-complete report cannot quietly pass as
// current.
func (r *CreditAnalyticsRepo) FindLatestByAccount(ctx context.Context, accountID int64) (*models.CreditAnalyticsRequest, error) {
	var req models.CreditAnalyticsRequest
	err := pgxscan.Get(ctx, r.pool, &req,
		`SELECT `+creditAnalyticsCols+` FROM credit_analytics_requests
		 WHERE account_id = $1
		   AND http_status >= 200 AND http_status < 300
		   AND response_body IS NOT NULL
		 ORDER BY (credit_score IS NOT NULL) DESC, id DESC
		 LIMIT 1`, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &req, err
}
