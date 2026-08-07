// Package service — bank-statement analysis.
//
// This file implements the bank-statement feature: PDF text extraction plus
// heuristic analysis (salary, EMIs, deposits, withdrawals, spending categories)
// running on an in-process worker pool. The pool is the service's first
// asynchronous primitive — the upload handler inserts a 'processing' row and
// returns its id immediately, and a worker writes the analysis back when done.
// Clients poll the row until status = 'completed' (or 'failed').
//
// The analysis engine (parseTransactions + analyze) is deliberately pure: it
// takes the extracted text and returns the derived metrics, so it can be tested
// table-driven without a database, mirroring parseReportInsights in
// credit_analytics.go.
package service

import (
	"context"
	cryptoRand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/bankdata"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
	"credit-report-service/internal/statement"
)

// Pagination bounds for ListStatements (mirrors ListReports).
const (
	statementDefaultSize = 20
	statementMaxSize     = 100
)

// StaleProcessingAge is how old a 'processing' row must be before startup
// recovery reclaims it. Exported so main wires it into the boot-time reclaim
// call. Picked generously: real analysis finishes in seconds, so anything older
// than this at boot was interrupted by a crash/restart.
const StaleProcessingAge = 5 * time.Minute

// BankStatementService orchestrates bank-statement upload, async analysis, and
// reads. It holds a Parser (text extraction) and the repo; the WorkerPool is
// wired in after construction via SetPool because the two are mutually
// dependent (the service submits jobs; the pool calls process back). The
// bankdata client is the Digitap Bank-Data API used for the redirect/upload
// flow (the alternative to local parsing); empty credentials make it a stub.
type BankStatementService struct {
	parser   statement.Parser
	repo     *repository.BankStatementRepo
	pool     *WorkerPool
	bankdata *bankdata.Client
	// callbackURL is the public URL we tell Digitap to POST the transaction-
	// complete callback to (txn_completed_cburl). Empty in dev (no public
	// host); the poll fallback in Get still completes the flow.
	callbackURL string
	// defaultReturnURL is where Digitap sends the user's browser after the
	// upload UI; passed through to Generate URL if the client omits it.
	defaultReturnURL string
}

// NewBankStatementService builds the service without its pool. Call SetPool
// before Submit so queued jobs have somewhere to land.
func NewBankStatementService(
	parser statement.Parser,
	repo *repository.BankStatementRepo,
	bankdataClient *bankdata.Client,
	callbackURL, defaultReturnURL string,
) *BankStatementService {
	return &BankStatementService{
		parser:           parser,
		repo:             repo,
		bankdata:         bankdataClient,
		callbackURL:      callbackURL,
		defaultReturnURL: defaultReturnURL,
	}
}

// SetPool wires the worker pool. Broken out of the constructor to resolve the
// service↔pool cycle the same way analytics/loan-switch resolve cross-service
// links.
func (s *BankStatementService) SetPool(pool *WorkerPool) { s.pool = pool }

// Submit accepts a freshly uploaded statement, persists a 'processing' row, and
// hands the work to the pool. The returned row is the caller's handle for
// polling. A size failure returns 413; a saturated queue returns 503 (the row
// is marked failed so the client sees why on the next poll).
func (s *BankStatementService) Submit(
	ctx context.Context,
	accountID int64,
	filename, mimeType string,
	pdfBytes []byte,
	maxBytes int,
) (*models.BankStatement, error) {
	if maxBytes > 0 && len(pdfBytes) > maxBytes {
		return nil, apperr.NewPayloadTooLarge(
			fmt.Sprintf("statement exceeds the %d-byte limit", maxBytes))
	}
	stmt := &models.BankStatement{
		AccountID: accountID,
		Filename:  filename,
		MimeType:  mimeType,
		PDFBytes:  pdfBytes,
		Status:    models.BankStatementStatusProcessing,
	}
	if err := s.repo.Create(ctx, stmt); err != nil {
		return nil, err
	}
	// Submit after the row exists so the worker can always update by id. If the
	// queue is full, mark the row failed with a clear reason rather than
	// leaving it hung in 'processing'.
	if err := s.pool.Submit(Job{StatementID: stmt.ID, PDFBytes: pdfBytes}); err != nil {
		_ = s.repo.MarkFailed(ctx, stmt.ID, "server busy, please retry shortly")
		stmt.Status = models.BankStatementStatusFailed
		stmt.ErrorMessage = "server busy, please retry shortly"
		return nil, apperr.NewServiceUnavailable("analysis queue is full, please retry shortly")
	}
	// Strip the PDF bytes from the returned payload — the client never needs
	// them and they'd bloat the JSON.
	stmt.PDFBytes = nil
	return stmt, nil
}

// process is the worker callback: extract text, analyze, write the result back.
// It runs on a worker goroutine with its own context (see WorkerPool.Submit),
// decoupled from the HTTP request that queued it. Every terminal branch writes
// either UpdateResult or MarkFailed so no row is left in 'processing'.
func (s *BankStatementService) process(ctx context.Context, job Job) {
	text, _, err := s.parser.Extract(job.PDFBytes)
	if err != nil {
		// ErrUnparseable (scanned PDF) and any other extraction failure land
		// here; both are user-facing "couldn't read this file" outcomes.
		reason := "could not extract text from this PDF"
		if errors.Is(err, statement.ErrUnparseable) {
			reason = "this PDF appears to be scanned or image-only; please export a text-based statement"
		}
		s.fail(ctx, job.StatementID, reason)
		return
	}

	analysis := analyze(text)
	payload, err := json.Marshal(analysis)
	if err != nil {
		s.fail(ctx, job.StatementID, "failed to serialize analysis")
		return
	}

	// Derive period bounds from the parsed transactions for the row's
	// period_start/period_end columns (cheap filtering without unpacking JSONB).
	var start, end *time.Time
	if len(analysis.Transactions) > 0 {
		s, e := analysis.Transactions[0].Date, analysis.Transactions[len(analysis.Transactions)-1].Date
		start, end = &s, &e
	}
	if err := s.repo.UpdateResult(ctx, job.StatementID, text, payload, len(analysis.Transactions), start, end); err != nil {
		// UpdateResult returning ErrNotFound means the row was deleted between
		// submit and process — nothing to update, so just log.
		slog.Warn("bank statement disappeared before result write",
			"id", job.StatementID, "error", err)
	}
}

// fail marks a row failed and logs the underlying cause for ops. The user-facing
// reason is the one stored on the row; the log line carries the detail.
func (s *BankStatementService) fail(ctx context.Context, id int64, reason string) {
	if err := s.repo.MarkFailed(ctx, id, reason); err != nil {
		slog.Error("failed to mark bank statement failed", "id", id, "error", err)
	}
}

// Get returns one statement (lightweight columns) for the owning account, or
// apperr.NotFound. Ownership is enforced in the repo query. For a Digitap row
// still in 'processing', it opportunistically syncs from Digitap first (the
// poll fallback): this lets the flow complete even when the public callback
// URL isn't reachable (dev without ngrok, or a missed webhook). Sync failures
// are swallowed — the row is returned as-is so a transient upstream outage
// doesn't break the read.
func (s *BankStatementService) Get(ctx context.Context, accountID, id int64) (*models.BankStatement, error) {
	stmt, err := s.repo.FindByID(ctx, accountID, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NewNotFound("Bank statement not found")
		}
		return nil, err
	}
	if stmt.Provider == models.BankStatementProviderDigitap &&
		stmt.Status == models.BankStatementStatusProcessing &&
		stmt.RequestID != "" {
		// Best-effort: a failure here (Digitap down, timeout) just means the
		// client keeps polling. Re-read after the sync attempt so we return the
		// freshest row.
		_ = s.SyncDigitap(ctx, stmt)
		if fresh, err := s.repo.FindByID(ctx, accountID, id); err == nil {
			return fresh, nil
		}
	}
	return stmt, nil
}

// GetRaw returns the statement with its extracted text layer (but never the PDF
// bytes), for debugging / "show me what was parsed".
func (s *BankStatementService) GetRaw(ctx context.Context, accountID, id int64) (*models.BankStatement, error) {
	return s.Get(ctx, accountID, id)
}

// GetLatest returns the most recent completed analysis, or apperr.NotFound.
func (s *BankStatementService) GetLatest(ctx context.Context, accountID int64) (*models.BankStatement, error) {
	stmt, err := s.repo.FindLatestByAccount(ctx, accountID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, apperr.NewNotFound("No completed bank statement analysis found")
		}
		return nil, err
	}
	return stmt, nil
}

// StatementSummary is the trimmed list-item shape: the id, status, and timing.
// Analysis is omitted to keep list responses small; fetch by id for the full
// breakdown.
type StatementSummary struct {
	ID               int64      `json:"id"`
	Filename         string     `json:"filename"`
	Status           string     `json:"status"`
	TransactionCount *int       `json:"transactionCount,omitempty"`
	PeriodStart      *time.Time `json:"periodStart,omitempty"`
	PeriodEnd        *time.Time `json:"periodEnd,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	CompletedAt      *time.Time `json:"completedAt,omitempty"`
}

// StatementPage is the paginated list response.
type StatementPage struct {
	Items []StatementSummary `json:"items"`
	Page  int                `json:"page"`
	Size  int                `json:"size"`
	Total int64              `json:"total"`
}

// List returns one page of the caller's statements, newest first.
func (s *BankStatementService) List(ctx context.Context, accountID int64, page, size int) (*StatementPage, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = statementDefaultSize
	}
	if size > statementMaxSize {
		size = statementMaxSize
	}
	offset := (page - 1) * size

	rows, err := s.repo.FindByAccountPaged(ctx, accountID, size, offset)
	if err != nil {
		return nil, err
	}
	total, err := s.repo.CountByAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	items := make([]StatementSummary, 0, len(rows))
	for _, r := range rows {
		items = append(items, StatementSummary{
			ID: r.ID, Filename: r.Filename, Status: r.Status,
			TransactionCount: r.TransactionCount,
			PeriodStart:      r.PeriodStart, PeriodEnd: r.PeriodEnd,
			CreatedAt: r.CreatedAt, CompletedAt: r.CompletedAt,
		})
	}
	return &StatementPage{Items: items, Page: page, Size: size, Total: total}, nil
}

// ===========================================================================
// Digitap Bank-Data flow (redirect/upload).
//
// The user uploads their statement PDF to Digitap's UI (the PDF never touches
// us). We mint the UI url via Generate URL, Digitap calls us back on completion
// (or the client polls), and we fetch the categorised report. Digitap computes
// salary/categories itself, so for these rows we store their report JSON
// verbatim in the analysis column rather than running our heuristics.
// ===========================================================================

// Re-exports so handlers can reference the bankdata callback shape/type without
// importing the bankdata package directly (keeps the handler's dependency on
// the service package, not the upstream client).
type (
	// BankDataCallbackEvent is the body Digitap POSTs to our webhook.
	BankDataCallbackEvent = bankdata.CallbackEvent
)

// BankDataCallbackTransactionComplete is the header value Digitap sends on a
// transaction-complete callback. See bankdata.CallbackTypeTransactionComplete.
const BankDataCallbackTransactionComplete = bankdata.CallbackTypeTransactionComplete

// us). We mint the UI url via Generate URL, Digitap calls us back on completion
// (or the client polls), and we fetch the categorised report. Digitap computes
// salary/categories itself, so for these rows we store their report JSON
// verbatim in the analysis column rather than running our heuristics.
// ===========================================================================

// DigitapInitiateResponse is the result of starting a Digitap flow: the row id
// (for polling) and the UI url + expiry the client should redirect the user to.
type DigitapInitiateResponse struct {
	ID          int64  `json:"id"`
	RedirectURL string `json:"redirectUrl"`
	ExpiresAt   string `json:"expiresAt,omitempty"`
	RequestID   string `json:"requestId,omitempty"`
}

// InitiateDigitap calls Generate URL and persists a 'processing' digitap row.
// The client redirects the user to RedirectURL; when done, Digitap POSTs our
// callback (txn_completed_cburl), and the client polls GET /:id in the meantime.
// returnUrl is optional and overrides the configured default.
func (s *BankStatementService) InitiateDigitap(
	ctx context.Context,
	accountID int64,
	returnURL string,
) (*DigitapInitiateResponse, error) {
	if s.bankdata == nil {
		// The digitap flow is optional; if it isn't wired, say so explicitly.
		return nil, apperr.NewServiceUnavailable("Digitap bank-statement flow is not configured")
	}
	if returnURL == "" {
		returnURL = s.defaultReturnURL
	}

	// client_ref_num is our correlation id, surfaced back in the callback body.
	// Prefixed so it's recognizable in Digitap's logs.
	refNum := "BS-" + strconv.FormatInt(time.Now().UnixMilli(), 10) + "-" + randHex(6)

	resp, status, err := s.bankdata.GenerateURL(ctx, bankdata.GenerateURLRequest{
		ClientRefNum:      refNum,
		TxnCompletedCBURL: s.callbackURL,
		Destination:       "statementupload",
		ReturnURL:         returnURL,
	})
	if err != nil {
		return nil, apperr.NewBadGateway("Digitap bank-data request failed")
	}
	if resp.Status != "success" || resp.URL == "" {
		// Map the documented upstream error codes to HTTP-appropriate responses.
		return nil, mapDigitapError(resp.Code, resp.Msg, status)
	}

	var expiresAt *time.Time
	if t, err := time.Parse(time.RFC3339, resp.Expires); err == nil {
		expiresAt = &t
	}
	row, err := s.repo.CreateDigitap(ctx, accountID, resp.RequestID, resp.URL, expiresAt)
	if err != nil {
		// The Digitap session was minted but we couldn't persist the row. The
		// user can't poll, so surface a 503 — the client can retry initiation.
		slog.Error("failed to persist digitap statement row",
			"account_id", accountID, "request_id", resp.RequestID, "error", err)
		return nil, apperr.NewServiceUnavailable("could not start bank-statement analysis")
	}
	return &DigitapInitiateResponse{
		ID: row.ID, RedirectURL: resp.URL,
		ExpiresAt: resp.Expires, RequestID: resp.RequestID,
	}, nil
}

// HandleCallback processes a transaction-complete callback from Digitap. It
// locates the row by request_id, records the txn_id, and triggers the sync
// (status-check + retrieve-report). Idempotent: a second callback for an
// already-completed row is a no-op. Unknown request_ids are logged and ignored
// (Digitap may redeliver for a flow we no longer have).
func (s *BankStatementService) HandleCallback(ctx context.Context, event bankdata.CallbackEvent) error {
	row, err := s.repo.FindByRequestID(ctx, event.RequestID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			slog.Warn("digitap callback for unknown request_id",
				"request_id", event.RequestID, "txn_id", event.TxnID)
			return nil
		}
		return err
	}
	// Already terminal — nothing to do. Avoids re-fetching the report on
	// redelivered callbacks.
	if row.Status != models.BankStatementStatusProcessing {
		return nil
	}
	if event.TxnID != "" {
		_ = s.repo.SetTxnID(ctx, row.ID, event.TxnID)
		row.TxnID = event.TxnID
	}
	// A Failure callback (e.g. user cancelled) is terminal; fail the row
	// without a status-check round-trip.
	if strings.EqualFold(event.Status, "Failure") {
		s.fail(ctx, row.ID, "statement upload was cancelled or failed at Digitap")
		return nil
	}
	return s.SyncDigitap(ctx, row)
}

// SyncDigitap is the shared retrieve step called by both the webhook and the
// poll fallback: ask Digitap for the status, and if the report is ready, fetch
// and persist it. Returns nil for non-terminal states (still processing) so the
// caller can retry; returns an error only on upstream failure.
func (s *BankStatementService) SyncDigitap(ctx context.Context, row *models.BankStatement) error {
	if row.RequestID == "" {
		return nil // not a digitap row, or missing correlation — nothing to sync
	}
	status, _, err := s.bankdata.StatusCheck(ctx, row.RequestID)
	if err != nil {
		slog.Warn("digitap status-check failed", "request_id", row.RequestID, "error", err)
		return err
	}
	// Find the first terminal txn for this request_id.
	var txn *bankdata.TxnStatus
	for i := range status.TxnStatus {
		t := status.TxnStatus[i]
		if t.Code == bankdata.CodeReportGenerated ||
			t.Status == "Failure" || t.Status == "Error" {
			txn = &t
			break
		}
	}
	if txn == nil {
		// Still in progress (or no txns yet). Leave the row 'processing'; the
		// next poll/callback will retry.
		return nil
	}
	if txn.Code != bankdata.CodeReportGenerated {
		reason := "statement analysis failed at Digitap"
		if txn.Msg != "" {
			reason = txn.Msg
		}
		s.fail(ctx, row.ID, reason)
		return nil
	}

	// Report ready — fetch it. Record the txn_id first so a retrieve failure
	// can be retried with the same id.
	if txn.TxnID != "" {
		_ = s.repo.SetTxnID(ctx, row.ID, txn.TxnID)
	}
	report, _, err := s.bankdata.RetrieveReport(ctx, txn.TxnID)
	if err != nil {
		slog.Warn("digitap retrieve-report failed",
			"request_id", row.RequestID, "txn_id", txn.TxnID, "error", err)
		return err
	}
	if len(report.Result) == 0 {
		s.fail(ctx, row.ID, "Digitap returned an empty report")
		return nil
	}
	// Store Digitap's report JSON verbatim. The transaction_count is unknown
	// (their schema nests it under result); leave the column null rather than
	// guess, and skip the period bounds (digitap rows have no parsed-text
	// layer to derive them from).
	if err := s.repo.UpdateResult(ctx, row.ID, "", report.Result, 0, nil, nil); err != nil {
		slog.Error("failed to persist digitap report",
			"id", row.ID, "request_id", row.RequestID, "error", err)
		return err
	}
	return nil
}

// mapDigitapError translates the documented Generate-URL error codes to typed
// app errors. Most are 502 (upstream/our-config issue); InvalidInstitution and
// the date-range codes are 400 (bad client input).
func mapDigitapError(code, msg string, status int) error {
	switch code {
	case "AccessDenied", "SignatureDoesNotMatch", "InvalidEncryption",
		"ClientNotConfigured", "NotSignedUp", "InternalError":
		// Our credentials / Digitap's side — surface as bad gateway.
		return apperr.NewBadGateway("Digitap rejected the request: " + orMsg(msg, code))
	case "InvalidInstitution", "InstitutionCurrentlyNotSupported",
		"InvalidStmtStartDate", "InvalidStmtEndDate", "DateRangeTooLarge",
		"InvalidClientRefNum", "InvalidDestination":
		return apperr.NewValidation("invalid bank-statement request: " + orMsg(msg, code))
	default:
		return apperr.NewBadGateway("Digitap error: " + orMsg(msg, code))
	}
}

func orMsg(msg, code string) string {
	if msg != "" {
		return msg
	}
	return code
}

// randHex returns n hex digits from the crypto/rand source. Used for
// client_ref_num suffixes; a failure falls back to a fixed suffix rather than
// failing the request.
func randHex(n int) string {
	b := make([]byte, n/2+1)
	if _, err := cryptoRand.Read(b); err != nil {
		return "000000"
	}
	return hex.EncodeToString(b)[:n]
}

// into salary / EMI / categories and computes summary totals. Both are pure so
// they test without a DB, like parseReportInsights.
// ===========================================================================

// Transaction is a single parsed statement line. Amount is always positive;
// Direction distinguishes money in (credit) from money out (debit).
type Transaction struct {
	Date        time.Time `json:"date"`
	Description string    `json:"description"`
	Amount      float64   `json:"amount"`
	Direction   string    `json:"direction"` // "credit" | "debit"
}

const (
	directionCredit = "credit"
	directionDebit  = "debit"
)

// Analysis is the derived-metrics payload persisted as the row's analysis JSONB
// and returned to the client when status = 'completed'.
type Analysis struct {
	Summary       Summary         `json:"summary"`
	Salary        *RecurringItem  `json:"salary,omitempty"`
	EMIs          []RecurringItem `json:"emis,omitempty"`
	Subscriptions []RecurringItem `json:"subscriptions,omitempty"`
	Categories    []CategoryTotal `json:"categories"`
	TopMerchants  []MerchantTotal `json:"topMerchants,omitempty"`
	MonthlyTotals []MonthlyTotal  `json:"monthlyTotals,omitempty"`
	Transactions  []Transaction   `json:"transactions"`
	ParseWarnings []string        `json:"parseWarnings,omitempty"`
}

// Summary holds the headline numbers: total in, total out, net, and averages.
type Summary struct {
	TotalCredits     float64    `json:"totalCredits"`
	TotalDebits      float64    `json:"totalDebits"`
	NetCashFlow      float64    `json:"netCashFlow"`
	MonthlyAvgCredit float64    `json:"monthlyAvgCredit"`
	MonthlyAvgDebit  float64    `json:"monthlyAvgDebit"`
	TransactionCount int        `json:"transactionCount"`
	PeriodStart      *time.Time `json:"periodStart,omitempty"`
	PeriodEnd        *time.Time `json:"periodEnd,omitempty"`
	MonthsCovered    int        `json:"monthsCovered"`
}

// RecurringItem describes a detected repeating transaction: salary, an EMI, or
// a subscription. Frequency is "monthly" for now (the only cadence we detect).
type RecurringItem struct {
	Description string  `json:"description"`
	Amount      float64 `json:"amount"`
	Direction   string  `json:"direction"`
	Frequency   string  `json:"frequency"`
	Occurrences int     `json:"occurrences"` // how many of the parsed txns matched
}

// CategoryTotal aggregates spend/deposit by payment rail or merchant type.
type CategoryTotal struct {
	Category string  `json:"category"`
	Count    int     `json:"count"`
	Total    float64 `json:"total"`
}

// MerchantTotal aggregates by normalized counterparty (e.g. "AMAZON", "SWIGGY").
type MerchantTotal struct {
	Merchant string  `json:"merchant"`
	Count    int     `json:"count"`
	Total    float64 `json:"total"`
}

// MonthlyTotal aggregates a calendar month.
type MonthlyTotal struct {
	Month  string  `json:"month"` // "2024-04"
	Credit float64 `json:"credit"`
	Debit  float64 `json:"debit"`
}

// analyze parses the text and classifies the transactions. It never returns an
// error: unparseable input simply yields an empty analysis with warnings, so a
// row still reaches 'completed' and the client can see what (if anything) was
// found. Hard extraction failures are handled earlier by the service.
func analyze(text string) *Analysis {
	txns, warnings := parseTransactions(text)
	a := &Analysis{Transactions: txns, ParseWarnings: warnings}

	// Sort by date asc so period bounds and monthly aggregation are stable.
	sort.Slice(a.Transactions, func(i, j int) bool {
		return a.Transactions[i].Date.Before(a.Transactions[j].Date)
	})

	a.Summary = buildSummary(a.Transactions)
	a.Categories = categorize(a.Transactions)
	a.TopMerchants = topMerchants(a.Transactions)
	a.MonthlyTotals = monthlyTotals(a.Transactions)
	a.Salary = detectSalary(a.Transactions)
	a.EMIs = detectEMIs(a.Transactions)
	a.Subscriptions = detectSubscriptions(a.Transactions)
	return a
}

// buildSummary computes totals and per-month averages. MonthsCovered is derived
// from the span of parsed dates so averages stay meaningful for partial months.
func buildSummary(txns []Transaction) Summary {
	var s Summary
	s.TransactionCount = len(txns)
	for _, t := range txns {
		if t.Direction == directionCredit {
			s.TotalCredits += t.Amount
		} else {
			s.TotalDebits += t.Amount
		}
	}
	s.NetCashFlow = s.TotalCredits - s.TotalDebits
	if len(txns) > 0 {
		s.PeriodStart = &txns[0].Date
		s.PeriodEnd = &txns[len(txns)-1].Date
		s.MonthsCovered = monthsBetween(*s.PeriodStart, *s.PeriodEnd)
	}
	if s.MonthsCovered > 0 {
		s.MonthlyAvgCredit = round2(s.TotalCredits / float64(s.MonthsCovered))
		s.MonthlyAvgDebit = round2(s.TotalDebits / float64(s.MonthsCovered))
	}
	return s
}

// monthsBetween returns the number of calendar months spanned (at least 1 when
// equal), used to average totals into a per-month figure.
func monthsBetween(a, b time.Time) int {
	if b.Before(a) {
		a, b = b, a
	}
	years := b.Year() - a.Year()
	months := int(b.Month()) - int(a.Month())
	n := years*12 + months + 1
	if n < 1 {
		return 1
	}
	return n
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// ===========================================================================
// Transaction parsing.
//
// Indian bank statements (HDFC/ICICI/SBI/Axis) typically render one transaction
// per line, leading with a date and trailing with one or two money columns. We
// detect the date at the start, the trailing amount(s), and treat the gap as the
// description. Lines that don't match are skipped (and counted as warnings).
// ===========================================================================

// dateRe matches the leading date in the common Indian formats:
//
//	05/04/2024 | 05-04-2024 | 05/04/24 | 05 Apr 2024 | 05-APR-24
//
// The month may be numeric (1-2 digits) or a short/long name (Jan..September).
var dateRe = regexp.MustCompile(`^(\d{1,2})[ /-]([A-Za-z]{3,9}|\d{1,2})[ /-](\d{2,4})\s+`)

// moneyRe finds trailing amounts; the last two whitespace-separated tokens that
// look like numbers (with optional commas/CR/DR suffix). Captures used below.
var moneyTailRe = regexp.MustCompile(`([0-9][0-9,]*\.?[0-9]*)\s*(CR|DR)?\s*$`)

// parseAmount parses an Indian-formatted amount ("1,234.56", "85000.00",
// "1,200") stripping thousands separators.
func parseAmount(s string) (float64, bool) {
	s = strings.ReplaceAll(s, ",", "")
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// parseDate turns the matched day/month/year into a time.Time, handling both
// numeric ("04") and short-name ("Apr", "April") months. Two-digit years are
// pinned to the 2000s (statements are recent).
func parseDate(day, mon, year string) (time.Time, bool) {
	d, err := strconv.Atoi(day)
	if err != nil || d < 1 || d > 31 {
		return time.Time{}, false
	}
	y, err := strconv.Atoi(year)
	if err != nil {
		return time.Time{}, false
	}
	if y < 100 {
		y += 2000
	}
	// Numeric month.
	if m, err := strconv.Atoi(mon); err == nil {
		if m < 1 || m > 12 {
			return time.Time{}, false
		}
		return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC), true
	}
	// Short or long month name.
	t, err := time.Parse("Jan 2 2006", fmt.Sprintf("%s %d %d", mon, d, y))
	if err == nil {
		return t, true
	}
	return time.Time{}, false
}

// parseTransactions walks the text line by line, extracting transactions that
// begin with a date. Returns the parsed set plus warnings noting how many lines
// were skipped (a hint at how much of the statement we couldn't read).
func parseTransactions(text string) ([]Transaction, []string) {
	var txns []Transaction
	skipped := 0
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		m := dateRe.FindStringSubmatch(line)
		if m == nil {
			// A non-empty line with no leading date isn't a transaction (headers,
			// footers, continuation). Only count skips when the line has digits,
			// to avoid inflating the count with boilerplate.
			if hasDigit(line) {
				skipped++
			}
			continue
		}
		date, ok := parseDate(m[1], m[2], m[3])
		if !ok {
			skipped++
			continue
		}
		rest := strings.TrimSpace(line[len(m[0]):])
		txn, ok := parseTxnBody(rest, date)
		if !ok {
			skipped++
			continue
		}
		txns = append(txns, txn)
	}

	var warnings []string
	if skipped > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"%d line(s) with amounts could not be parsed and were skipped", skipped))
	}
	return txns, warnings
}

var hasDigitRe = regexp.MustCompile(`[0-9]`)

func hasDigit(s string) bool { return hasDigitRe.MatchString(s) }

// parseTxnBody splits the post-date remainder into description + amount column
// (or two columns: debit then credit). The trailing tokens are the money; the
// leading text is the description. Direction is inferred from CR/DR suffix,
// the presence of a second amount column, or — failing both — defaulted to
// debit (the common case; explicit markers above override this).
func parseTxnBody(rest string, date time.Time) (Transaction, bool) {
	tokens := strings.Fields(rest)
	if len(tokens) < 2 { // need at least a description and an amount
		return Transaction{}, false
	}

	// Some statements emit the direction as a separate trailing token
	// ("85000.00 CR" → ["85000.00", "CR"]). Fold a bare trailing CR/DR into the
	// preceding token so the money walk below sees one combined token.
	tokens = foldTrailingDirection(tokens)

	// Walk back from the end while tokens look numeric (with optional CR/DR
	// suffix), capturing up to two amounts.
	firstMoney := len(tokens)
	moneyCount := 0
	for i := len(tokens) - 1; i >= 0 && moneyCount < 2; i-- {
		if _, ok := parseAmount(cleanMoney(tokens[i])); ok {
			firstMoney = i
			moneyCount++
			continue
		}
		break
	}
	if moneyCount == 0 {
		return Transaction{}, false
	}

	desc := strings.TrimSpace(strings.Join(tokens[:firstMoney], " "))
	if desc == "" {
		return Transaction{}, false
	}

	moneyTokens := tokens[firstMoney:]
	// Two-column layout: [debit, credit]. A single non-zero value occupies one
	// slot; the other is implicitly zero.
	if len(moneyTokens) >= 2 {
		debitStr := cleanMoney(moneyTokens[0])
		creditStr := cleanMoney(moneyTokens[1])
		debit, _ := parseAmount(debitStr)
		credit, _ := parseAmount(creditStr)
		// Prefer the non-zero column; if both present (rare), credit wins as the
		// direction-specific column.
		switch {
		case credit > 0:
			return Transaction{Date: date, Description: desc, Amount: credit, Direction: directionCredit}, true
		case debit > 0:
			return Transaction{Date: date, Description: desc, Amount: debit, Direction: directionDebit}, true
		default:
			return Transaction{}, false
		}
	}

	// Single-column layout: rely on a CR/DR suffix; default to debit otherwise
	// (the common case — most lines on a statement are money out).
	one := moneyTokens[0]
	direction := directionDebit
	cleaned := one
	if strings.HasSuffix(strings.ToUpper(one), "CR") {
		direction = directionCredit
		cleaned = one[:len(one)-2]
	} else if strings.HasSuffix(strings.ToUpper(one), "DR") {
		direction = directionDebit
		cleaned = one[:len(one)-2]
	}
	amt, ok := parseAmount(cleaned)
	if !ok || amt <= 0 {
		return Transaction{}, false
	}
	return Transaction{Date: date, Description: desc, Amount: amt, Direction: direction}, true
}

// foldTrailingDirection merges a bare trailing "CR"/"DR" token (any case) into
// the preceding token, so "85000.00 CR" becomes "85000.00CR". A no-op when the
// suffix is already attached or absent.
func foldTrailingDirection(tokens []string) []string {
	if len(tokens) < 2 {
		return tokens
	}
	last := strings.ToUpper(tokens[len(tokens)-1])
	if last == "CR" || last == "DR" {
		tokens[len(tokens)-2] = tokens[len(tokens)-2] + last
		tokens = tokens[:len(tokens)-1]
	}
	return tokens
}

// cleanMoney strips a CR/DR suffix (case-insensitive) from a money token.
func cleanMoney(tok string) string {
	up := strings.ToUpper(tok)
	up = strings.TrimSuffix(up, "CR")
	up = strings.TrimSuffix(up, "DR")
	return strings.TrimSpace(up)
}

// ===========================================================================
// Classification heuristics.
// ===========================================================================

// Keyword sets are case-insensitive and matched anywhere in the description.

var (
	salaryRe = regexp.MustCompile(`(?i)salary|payroll|wages|remuneration`)
	emiRe    = regexp.MustCompile(`(?i)\bemi\b|loan|nach|mandate|\becs\b|instal|autopay`)
	subsRe   = regexp.MustCompile(`(?i)netflix|primevideo|prime|spotify|hotstar|youtube|swiggy|zomato|amazon prime`)
)

// Category keyword rules, checked in order; the first match wins. UPI/Card/ATM
// are payment rails; Fuel/Grocery are spend-type overlays matched on the
// counterparty.
var categoryRules = []struct {
	category string
	re       *regexp.Regexp
}{
	{"upi", regexp.MustCompile(`(?i)\bupi\b|unified payment`)},
	{"atm", regexp.MustCompile(`(?i)\batm\b|cash withdrawal`)},
	{"card", regexp.MustCompile(`(?i)\bvisa\b|\bcard\b|\bpos\b|\bdebit card\b`)},
	{"neft_imps_rtgs", regexp.MustCompile(`(?i)\bneft\b|\bimps\b|\brtgs\b`)},
	{"cheque", regexp.MustCompile(`(?i)\bcheque\b|\bchq\b`)},
	{"fuel", regexp.MustCompile(`(?i)fuel|petrol|diesel|bpcl|hpcl|iocl|indian oil`)},
	{"grocery", regexp.MustCompile(`(?i)grocery|supermarket|big bazaar|dmart|reliance fresh|more store`)},
}

// categorize bins each transaction by the first matching category rule and sums
// totals. Transactions matching no rule are excluded (they still appear in the
// transaction list and the headline totals).
func categorize(txns []Transaction) []CategoryTotal {
	buckets := map[string]*CategoryTotal{}
	order := []string{}
	for _, t := range txns {
		cat := matchCategory(t.Description)
		if cat == "" {
			continue
		}
		b, ok := buckets[cat]
		if !ok {
			b = &CategoryTotal{Category: cat}
			buckets[cat] = b
			order = append(order, cat)
		}
		b.Count++
		b.Total += t.Amount
	}
	out := make([]CategoryTotal, 0, len(order))
	for _, cat := range order {
		c := buckets[cat]
		c.Total = round2(c.Total)
		out = append(out, *c)
	}
	return out
}

func matchCategory(desc string) string {
	for _, r := range categoryRules {
		if r.re.MatchString(desc) {
			return r.category
		}
	}
	return ""
}

// topMerchants aggregates spend by normalized counterparty. Normalization keeps
// the first few words of the description uppercased, so "UPI/To AMAZON" and
// "AMAZON RETAIL" both roll up loosely. Only debits count as "spend".
func topMerchants(txns []Transaction) []MerchantTotal {
	buckets := map[string]*MerchantTotal{}
	for _, t := range txns {
		if t.Direction != directionDebit {
			continue
		}
		m := normalizeMerchant(t.Description)
		if m == "" {
			continue
		}
		b, ok := buckets[m]
		if !ok {
			b = &MerchantTotal{Merchant: m}
			buckets[m] = b
		}
		b.Count++
		b.Total += t.Amount
	}
	out := make([]MerchantTotal, 0, len(buckets))
	for _, b := range buckets {
		b.Total = round2(b.Total)
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Total > out[j].Total })
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

// normalizeMerchant strips common payment-rail prefixes ("UPI/", "VISA/",
// "NEFT/") and keeps the leading counterparty token. Falls back to the whole
// description when nothing recognizable remains.
func normalizeMerchant(desc string) string {
	s := strings.ToUpper(strings.TrimSpace(desc))
	// Drop leading rail/sequence prefixes.
	s = regexp.MustCompile(`^(UPI|VISA|NEFT|IMPS|RTGS|ATM|POS|CARD)[/:\-\s]+`).ReplaceAllString(s, "")
	// Drop transaction-reference number noise like "/412345678/" or "/To ".
	s = regexp.MustCompile(`/[0-9]+/|/TO\s+|/`).ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	if s == "" {
		return strings.ToUpper(desc)
	}
	// Keep the first three words to group loosely (e.g. "AMAZON RETAIL IN").
	fields := strings.Fields(s)
	if len(fields) > 3 {
		fields = fields[:3]
	}
	return strings.Join(fields, " ")
}

// monthlyTotals aggregates debits/credits per calendar month (YYYY-MM).
func monthlyTotals(txns []Transaction) []MonthlyTotal {
	buckets := map[string]*MonthlyTotal{}
	order := []string{}
	for _, t := range txns {
		key := t.Date.Format("2006-01")
		b, ok := buckets[key]
		if !ok {
			b = &MonthlyTotal{Month: key}
			buckets[key] = b
			order = append(order, key)
		}
		if t.Direction == directionCredit {
			b.Credit += t.Amount
		} else {
			b.Debit += t.Amount
		}
	}
	sort.Strings(order)
	out := make([]MonthlyTotal, 0, len(order))
	for _, k := range order {
		b := buckets[k]
		out = append(out, MonthlyTotal{
			Month: b.Month, Credit: round2(b.Credit), Debit: round2(b.Debit),
		})
	}
	return out
}

// detectSalary returns the single most-likely salary credit, or nil. Salary is
// either an explicit SALARY/PAYROLL keyword, or a recurring monthly credit of
// similar size; debits are never considered.
func detectSalary(txns []Transaction) *RecurringItem {
	var best *RecurringItem
	for i := range txns {
		t := txns[i]
		if t.Direction != directionCredit {
			continue
		}
		if salaryRe.MatchString(t.Description) {
			item := groupRecurring(txns, t, "monthly")
			if best == nil || item.Occurrences > best.Occurrences ||
				(item.Occurrences == best.Occurrences && item.Amount > best.Amount) {
				best = &item
			}
		}
	}
	if best != nil {
		return best
	}
	// No explicit salary label: look for the largest recurring monthly credit
	// above a modest threshold (avoids flagging a one-off transfer).
	groups := recurringGroups(txns, directionCredit)
	for i := range groups {
		g := groups[i]
		if g.Occurrences >= 2 && g.Amount >= 5000 {
			if best == nil || g.Amount > best.Amount {
				best = &g
			}
		}
	}
	return best
}

// detectEMIs returns all recurring monthly debits that look like loan payments,
// whether by keyword (EMI/LOAN/NACH/...) or by cadence.
func detectEMIs(txns []Transaction) []RecurringItem {
	seen := map[string]bool{}
	var out []RecurringItem
	for _, t := range txns {
		if t.Direction != directionDebit || !emiRe.MatchString(t.Description) {
			continue
		}
		key := normalizeMerchant(t.Description)
		if seen[key] {
			continue
		}
		seen[key] = true
		item := groupRecurring(txns, t, "monthly")
		out = append(out, item)
	}
	return out
}

// detectSubscriptions returns recurring small debits matching known services.
func detectSubscriptions(txns []Transaction) []RecurringItem {
	seen := map[string]bool{}
	var out []RecurringItem
	for _, t := range txns {
		if t.Direction != directionDebit || !subsRe.MatchString(t.Description) {
			continue
		}
		key := normalizeMerchant(t.Description)
		if seen[key] {
			continue
		}
		seen[key] = true
		item := groupRecurring(txns, t, "monthly")
		out = append(out, item)
	}
	return out
}

// recurringGroups returns all monthly-recurring transactions of a direction,
// grouped by normalized counterparty, with occurrences >= 2.
func recurringGroups(txns []Transaction, direction string) []RecurringItem {
	groups := map[string][]Transaction{}
	order := []string{}
	for _, t := range txns {
		if t.Direction != direction {
			continue
		}
		key := normalizeMerchant(t.Description)
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], t)
	}
	var out []RecurringItem
	for _, k := range order {
		g := groups[k]
		if !isMonthlyRecurring(g) {
			continue
		}
		// Pick the modal amount (the value that appears most often in the group).
		out = append(out, RecurringItem{
			Description: g[0].Description,
			Amount:      round2(medianAmount(g)),
			Direction:   direction,
			Frequency:   "monthly",
			Occurrences: len(g),
		})
	}
	return out
}

// groupRecurring counts how many transactions share the seed's normalized
// merchant and recur roughly monthly. Used to attach an Occurrences count to a
// keyword-matched seed transaction.
func groupRecurring(txns []Transaction, seed Transaction, _ string) RecurringItem {
	key := normalizeMerchant(seed.Description)
	var matched []Transaction
	for _, t := range txns {
		if t.Direction != seed.Direction {
			continue
		}
		if normalizeMerchant(t.Description) != key {
			continue
		}
		// Same counterparty and within ±15% of the seed amount counts as the
		// same recurring line.
		if seed.Amount > 0 && math.Abs(t.Amount-seed.Amount)/seed.Amount > 0.15 {
			continue
		}
		matched = append(matched, t)
	}
	return RecurringItem{
		Description: seed.Description,
		Amount:      round2(seed.Amount),
		Direction:   seed.Direction,
		Frequency:   "monthly",
		Occurrences: len(matched),
	}
}

// isMonthlyRecurring reports whether a group of same-merchant transactions
// repeats across distinct months — the hallmark of a salary/EMI/subscription.
// Two or more distinct months is enough; we don't require strict periodicity.
func isMonthlyRecurring(group []Transaction) bool {
	if len(group) < 2 {
		return false
	}
	months := map[string]bool{}
	for _, t := range group {
		months[t.Date.Format("2006-01")] = true
	}
	return len(months) >= 2
}

// medianAmount returns the middle amount of a group; a robust representative
// when amounts vary slightly month to month.
func medianAmount(group []Transaction) float64 {
	if len(group) == 0 {
		return 0
	}
	vals := make([]float64, len(group))
	for i, t := range group {
		vals[i] = t.Amount
	}
	sort.Float64s(vals)
	mid := len(vals) / 2
	if len(vals)%2 == 1 {
		return vals[mid]
	}
	return (vals[mid-1] + vals[mid]) / 2
}

// ===========================================================================
// Worker pool — the service's first asynchronous primitive.
//
// No external queue exists in the codebase, so analysis runs on a bounded
// in-process pool. Submit is non-blocking (the channel is buffered); a full
// queue surfaces as a 503 so the client retries rather than blocking the
// request. Workers derive a fresh context (Process is decoupled from the
// HTTP request, which has already returned 202).
// ===========================================================================

// Job is one queued analysis task. PDFBytes are carried in the job so the
// worker doesn't have to re-read the row (and re-fetch a potentially large
// BYTEA) to process it.
type Job struct {
	StatementID int64
	PDFBytes    []byte
}

// processor decouples the pool from the service for testing — the pool only
// needs a "run this job" callback.
type processor interface {
	process(ctx context.Context, job Job)
}

// WorkerPool runs statement analysis on N goroutines. It is safe to Submit from
// any goroutine; Submit never blocks (it returns ErrQueueFull when saturated).
type WorkerPool struct {
	jobs        chan Job
	proc        processor
	timeout     time.Duration
	concurrency int
	wg          sync.WaitGroup
	startOnce   sync.Once
	stopOnce    sync.Once
}

// NewWorkerPool constructs a pool. concurrency is the worker count; buffer is
// the queue depth (how many jobs can be queued beyond the in-flight ones).
// timeout bounds a single analysis so a pathological PDF can't pin a worker.
// Workers are spawned by Start (not here) so the pool can be built during DI
// and launched once the server ctx is available.
func NewWorkerPool(proc processor, concurrency, buffer int, timeout time.Duration) *WorkerPool {
	if concurrency < 1 {
		concurrency = 1
	}
	if buffer < 0 {
		buffer = 0
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	return &WorkerPool{
		jobs:        make(chan Job, buffer),
		proc:        proc,
		timeout:     timeout,
		concurrency: concurrency,
	}
}

// ErrQueueFull is returned by Submit when the buffered queue is full.
var ErrQueueFull = errors.New("statement analysis queue is full")

// Submit queues a job without blocking. Returns ErrQueueFull if the buffer is
// full so the caller can fail the row fast instead of stalling the request.
func (p *WorkerPool) Submit(job Job) error {
	select {
	case p.jobs <- job:
		return nil
	default:
		return ErrQueueFull
	}
}

// Start spawns the workers. It is idempotent (guarded by startOnce) so
// accidental double-start is harmless. Each worker runs until the jobs channel
// is closed by Stop. The supplied ctx is the parent for every job: per-job
// timeouts derive from it, and cancelling it (server shutdown) halts workers
// once the queue drains.
func (p *WorkerPool) Start(ctx context.Context) {
	p.startOnce.Do(func() {
		for i := 0; i < p.concurrency; i++ {
			p.wg.Add(1)
			go p.worker(ctx)
		}
	})
}

// worker drains the jobs channel until it closes, calling proc.process with a
// fresh, timeout-bounded context per job.
func (p *WorkerPool) worker(ctx context.Context) {
	defer p.wg.Done()
	for job := range p.jobs {
		// Derive from the server ctx (so shutdown cancels us) but with a
		// per-job timeout. We deliberately do NOT derive from any request ctx:
		// the request that queued this job has already returned.
		jobCtx, cancel := context.WithTimeout(ctx, p.timeout)
		p.proc.process(jobCtx, job)
		cancel()
	}
}

// Stop closes the queue and waits for in-flight jobs to finish. Idempotent
// (guarded by stopOnce). After Stop the pool cannot be restarted.
func (p *WorkerPool) Stop() {
	p.stopOnce.Do(func() {
		close(p.jobs)
		p.wg.Wait()
	})
}
