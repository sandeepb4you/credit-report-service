package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"credit-report-service/internal/models"
)

// BankStatementRepo owns bank_statements queries. Two column lists are used so
// the heavy columns (extracted_text, pdf_bytes) are excluded from list/detail
// reads: list and detail responses are small and fast, while the worker fetches
// the full row only when it needs the PDF bytes and writes the text back.
type BankStatementRepo struct{ pool *pgxpool.Pool }

func NewBankStatementRepo(pool *pgxpool.Pool) *BankStatementRepo {
	return &BankStatementRepo{pool: pool}
}

// bankStatementCols is the lightweight column set returned by list/detail/latest
// and by the raw endpoint. It excludes pdf_bytes (large, never useful to the
// client) but includes extracted_text so /raw can show what was parsed.
const bankStatementCols = `id, account_id, provider, filename, mime_type, status,
    extracted_text, analysis, error_message,
    transaction_count, period_start, period_end,
    request_id, txn_id, redirect_url, url_expires_at,
    created_at, completed_at`

// Create inserts a freshly-uploaded statement row in 'processing' status and
// fills the server-assigned fields (id, created_at) on the supplied model.
func (r *BankStatementRepo) Create(ctx context.Context, stmt *models.BankStatement) error {
	return pgxscan.Get(ctx, r.pool, stmt,
		`INSERT INTO bank_statements (account_id, provider, filename, mime_type, status, pdf_bytes)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING `+bankStatementCols,
		stmt.AccountID, models.BankStatementProviderLocal, stmt.Filename, stmt.MimeType,
		models.BankStatementStatusProcessing, stmt.PDFBytes,
	)
}

// CreateDigitap inserts a 'processing' row for the Digitap redirect/upload flow
// — there's no PDF on our side (the user uploads to Digitap's UI), so we store
// the Digitap correlation id and the UI url we're handing the client instead.
func (r *BankStatementRepo) CreateDigitap(
	ctx context.Context,
	accountID int64,
	requestID, redirectURL string,
	urlExpiresAt *time.Time,
) (*models.BankStatement, error) {
	var s models.BankStatement
	err := pgxscan.Get(ctx, r.pool, &s,
		`INSERT INTO bank_statements
		     (account_id, provider, status, request_id, redirect_url, url_expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING `+bankStatementCols,
		accountID, models.BankStatementProviderDigitap,
		models.BankStatementStatusProcessing, requestID, redirectURL, urlExpiresAt,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// FindByRequestID locates a row by its Digitap request_id. Used by the public
// callback webhook, which carries request_id but is not authenticated as the
// owning account — so this deliberately does NOT filter by account_id.
func (r *BankStatementRepo) FindByRequestID(ctx context.Context, requestID string) (*models.BankStatement, error) {
	var s models.BankStatement
	err := pgxscan.Get(ctx, r.pool, &s,
		`SELECT `+bankStatementCols+` FROM bank_statements
		 WHERE request_id = $1`, requestID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// SetTxnID records the Digitap txn_id on a row, returned by the callback and
// status-check. Idempotent: re-recording the same txn_id is a no-op.
func (r *BankStatementRepo) SetTxnID(ctx context.Context, id int64, txnID string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE bank_statements SET txn_id = $2 WHERE id = $1 AND txn_id IS DISTINCT FROM $2`,
		id, txnID)
	return err
}

// UpdateResult is called by the worker once analysis succeeds: it flips status
// to 'completed', stores the extracted text and derived analysis JSON, and
// records the parsed transaction count and period. Returns ErrNotFound if the
// row was deleted between submit and process.
func (r *BankStatementRepo) UpdateResult(
	ctx context.Context,
	id int64,
	extractedText string,
	analysis json.RawMessage,
	transactionCount int,
	periodStart, periodEnd *time.Time,
) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE bank_statements
		    SET status            = $2,
		        extracted_text    = $3,
		        analysis          = $4,
		        transaction_count = $5,
		        period_start      = $6,
		        period_end        = $7,
		        completed_at      = now()
		  WHERE id = $1`,
		id, models.BankStatementStatusCompleted, extractedText, analysis,
		transactionCount, periodStart, periodEnd,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkFailed flips a row to 'failed' with a user-facing reason. Used when the
// PDF can't be parsed or analysis errors out.
func (r *BankStatementRepo) MarkFailed(ctx context.Context, id int64, reason string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE bank_statements
		    SET status        = $2,
		        error_message = $3,
		        completed_at  = now()
		  WHERE id = $1`,
		id, models.BankStatementStatusFailed, reason,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// FindByID returns one row (lightweight columns) owned by accountID, or
// ErrNotFound. Ownership is enforced in the query so a caller can never read
// another account's statement by guessing ids.
func (r *BankStatementRepo) FindByID(ctx context.Context, accountID, id int64) (*models.BankStatement, error) {
	var s models.BankStatement
	err := pgxscan.Get(ctx, r.pool, &s,
		`SELECT `+bankStatementCols+` FROM bank_statements
		 WHERE id = $1 AND account_id = $2`, id, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// FindByAccountPaged returns one page of an account's rows, newest first.
func (r *BankStatementRepo) FindByAccountPaged(ctx context.Context, accountID int64, limit, offset int) ([]models.BankStatement, error) {
	rs := []models.BankStatement{}
	err := pgxscan.Select(ctx, r.pool, &rs,
		`SELECT `+bankStatementCols+` FROM bank_statements
		 WHERE account_id = $1
		 ORDER BY id DESC
		 LIMIT $2 OFFSET $3`, accountID, limit, offset)
	return rs, err
}

// CountByAccount returns the total number of rows for an account (for the
// pagination total field).
func (r *BankStatementRepo) CountByAccount(ctx context.Context, accountID int64) (int64, error) {
	var n int64
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM bank_statements WHERE account_id = $1`, accountID).Scan(&n)
	return n, err
}

// FindLatestByAccount returns the most recent completed analysis for an
// account, or ErrNotFound. Only 'completed' rows carry analysis worth surfacing.
func (r *BankStatementRepo) FindLatestByAccount(ctx context.Context, accountID int64) (*models.BankStatement, error) {
	var s models.BankStatement
	err := pgxscan.Get(ctx, r.pool, &s,
		`SELECT `+bankStatementCols+` FROM bank_statements
		 WHERE account_id = $1 AND status = $2
		 ORDER BY id DESC
		 LIMIT 1`, accountID, models.BankStatementStatusCompleted)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// ReclaimStaleProcessing flips any rows still 'processing' after staleAfter to
// 'failed'. Called by the worker pool on startup so a crash mid-analysis
// doesn't leave rows hung forever. Returns the number of rows reclaimed.
func (r *BankStatementRepo) ReclaimStaleProcessing(ctx context.Context, staleAfter time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE bank_statements
		    SET status        = $2,
		        error_message = $3,
		        completed_at  = now()
		  WHERE status = $1 AND created_at < $4`,
		models.BankStatementStatusProcessing,
		models.BankStatementStatusFailed,
		"analysis interrupted by server restart",
		staleAfter,
	)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
