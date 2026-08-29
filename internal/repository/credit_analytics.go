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
    idempotency_key, request_id, result_code, http_status, message,
    request_body, response_body, credit_score, result_pdf_url,
    reuse_count, last_reused_at, reused_from_report_id, data_fetched_at, created_at`

// succeededPredicate narrows credit_analytics_requests to the rows that represent
// a report the user actually received: a 2xx from Digitap carrying a body.
//
// Every request is persisted, successes and failures alike, so a support question
// months later still has the provider's own answer beside our decision. But a
// failed pull is not a report, and conflating the two on a consumer-facing path
// does real damage:
//
//   - It is not a past check. Listed, it is a history entry that opens nothing.
//   - Worse, it reads as a CONSUMED ENTITLEMENT. The app decides whether a paid
//     score check still owes the user a pull by comparing the newest PAID order
//     against the newest row here (HomeViewModel.checkUnconsumedScoreCheck). A
//     failed pull is by definition newer than the payment that triggered it, so
//     an unfiltered list marks the check spent and puts the paywall back in front
//     of someone who has already paid — inviting them to buy a second time for a
//     report our vendor failed to deliver.
//
// The client cannot draw this distinction itself: ReportSummary carries
// {id, createdAt, creditScore}, and a thin-file success — which DOES legitimately
// consume the check, since the bureau answered — is null-scored exactly like a
// failure.
//
// Deliberately the same predicate FindLatestByAccount already applied on its own:
// "a report" now means one thing on every read path instead of two.
const succeededPredicate = `http_status >= 200 AND http_status < 300
		   AND response_body IS NOT NULL`

// Create inserts a credit-analytics request row and fills the server-assigned
// fields (id, created_at) on the supplied model.
func (r *CreditAnalyticsRepo) Create(ctx context.Context, req *models.CreditAnalyticsRequest) error {
	err := pgxscan.Get(ctx, r.pool, req,
		`INSERT INTO credit_analytics_requests
		     (account_id, client_ref_num, mobile_no, idempotency_key, request_id,
		      result_code, http_status, message, request_body, response_body, credit_score)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 RETURNING `+creditAnalyticsCols,
		req.AccountID, req.ClientRefNum, req.MobileNo, req.IdempotencyKey, req.RequestID,
		req.ResultCode, req.HTTPStatus, req.Message, req.RequestBody, req.ResponseBody,
		req.CreditScore,
	)
	// A duplicate (account_id, idempotency_key) means a concurrent call already
	// claimed this key. Surfaced as ErrConflict so the service can return that
	// call's row instead of a second report.
	return classifyPgErr(err)
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

// CreateReusedCopy writes a new report row carrying an existing pull's data,
// for a refresh answered inside the reuse window.
//
// A copy rather than handing back the source row, because the caller bought a
// check and their history has to show one: a purchase that leaves no trace where
// the user looks for it reads as a lost payment.
//
// Three fields are deliberate:
//
//   - data_fetched_at is INHERITED, never now(). It is what the reuse window is
//     measured against, so a copy cannot restart the clock. Copying now() here
//     would mean a refresh every six days kept the data alive forever and the
//     bureau was never called again.
//   - reused_from_report_id points at the ORIGINAL pull, resolved through the
//     source when the source is itself a copy, so lineage stays one level deep.
//   - result_pdf_url is carried over: the stored PDF is the same document, and
//     the copy should be downloadable rather than silently lacking one.
//
// client_ref_num and request_body come from the caller's own request — this row
// records a request that was genuinely made, just answered from storage.
func (r *CreditAnalyticsRepo) CreateReusedCopy(
	ctx context.Context,
	src *models.CreditAnalyticsRequest,
	accountID int64,
	clientRefNum string,
	requestBody []byte,
	idempotencyKey *string,
) (*models.CreditAnalyticsRequest, error) {
	origin := src.ID
	if src.ReusedFromReportID != nil {
		origin = *src.ReusedFromReportID
	}
	var out models.CreditAnalyticsRequest
	err := pgxscan.Get(ctx, r.pool, &out,
		`INSERT INTO credit_analytics_requests
		     (account_id, client_ref_num, mobile_no, idempotency_key, request_id,
		      result_code, http_status, message, request_body, response_body,
		      credit_score, result_pdf_url, reused_from_report_id, data_fetched_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		 RETURNING `+creditAnalyticsCols,
		accountID, clientRefNum, src.MobileNo, idempotencyKey, src.RequestID,
		src.ResultCode, src.HTTPStatus, src.Message, requestBody, src.ResponseBody,
		src.CreditScore, src.ResultPDFURL, origin, src.DataFetchedAt,
	)
	return &out, classifyPgErr(err)
}

// RecordReuse counts one serving of this stored report in place of a fresh
// bureau pull.
//
// The reuse path otherwise writes nothing, which is what made reuse invisible
// once its log line rotated away. One primary-key UPDATE against the Digitap
// call it replaces is a rounding error, and it is what makes "how often does
// reuse fire, and what does it save" answerable at all.
//
// Best-effort at the call site: losing the count must never fail a response the
// caller is entitled to.
func (r *CreditAnalyticsRepo) RecordReuse(ctx context.Context, id int64) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE credit_analytics_requests
		 SET reuse_count = reuse_count + 1, last_reused_at = now()
		 WHERE id = $1`, id)
	return err
}

// FindByAccountAndKey returns the row a previous call stored under this
// account's idempotency key, or ErrNotFound.
//
// Scoped to the account, not global: the key is chosen by the client, so a
// globally-keyed lookup would let one account guess another's key and read a
// stranger's credit report.
//
// Deliberately returns rows whatever their outcome, unlike the reports list. A
// replay must answer with what the first call actually produced — if that call
// failed, the replay says so rather than quietly running a second billed pull
// that the caller believes is the same request.
func (r *CreditAnalyticsRepo) FindByAccountAndKey(ctx context.Context, accountID int64, key string) (*models.CreditAnalyticsRequest, error) {
	var req models.CreditAnalyticsRequest
	err := pgxscan.Get(ctx, r.pool, &req,
		`SELECT `+creditAnalyticsCols+` FROM credit_analytics_requests
		 WHERE account_id = $1 AND idempotency_key = $2`, accountID, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &req, nil
}

// FindByAccountPaged returns one page of an account's reports, newest first.
// limit is the page size; offset is the zero-based row offset.
//
// Failed pulls are excluded — see succeededPredicate. This list is what the app
// calls "my past score checks" AND what it measures a paid-but-unrun check
// against, and a failure is neither.
func (r *CreditAnalyticsRepo) FindByAccountPaged(ctx context.Context, accountID int64, limit, offset int) ([]models.CreditAnalyticsRequest, error) {
	rs := []models.CreditAnalyticsRequest{}
	err := pgxscan.Select(ctx, r.pool, &rs,
		`SELECT `+creditAnalyticsCols+` FROM credit_analytics_requests
		 WHERE account_id = $1
		   AND `+succeededPredicate+`
		 ORDER BY id DESC
		 LIMIT $2 OFFSET $3`, accountID, limit, offset)
	return rs, err
}

// CountByAccount returns the total number of reports for an account (for the
// pagination total field).
//
// Must apply the same filter as FindByAccountPaged or the total overcounts the
// pages it describes: an account with one report and two failed attempts would
// report 3 items across a single page of 1.
func (r *CreditAnalyticsRepo) CountByAccount(ctx context.Context, accountID int64) (int64, error) {
	var n int64
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM credit_analytics_requests
		 WHERE account_id = $1
		   AND `+succeededPredicate, accountID).Scan(&n)
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
		   AND `+succeededPredicate+`
		 ORDER BY (credit_score IS NOT NULL) DESC, id DESC
		 LIMIT 1`, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &req, err
}
