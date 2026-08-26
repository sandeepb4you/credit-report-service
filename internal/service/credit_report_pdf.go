// Package service — async credit-report PDF relay (Digitap → S3).
//
// When a credit-analytics request uses report_type 3, Digitap returns
// result_pdf: a URL for the generated PDF that is valid for ~1 hour. This file
// implements the off-request relay that downloads that PDF, encrypts it with the
// account holder's PAN and date of birth, uploads it to S3, and stores the
// object's s3:// URI on the analytics row.
//
// The one-hour expiry is why this is a relay and not a redirect: a link the user
// could follow tomorrow has to be our copy, not Digitap's.
//
// Encryption happens here rather than on the way out to email, so the object is
// unreadable in the bucket as well as in a mailbox. A report that CANNOT be
// encrypted is not uploaded at all — see process. Storing it unprotected to save
// the download would defeat the requirement it exists to satisfy.
//
// The relay is best-effort and asynchronous: the credit-analytics Request
// endpoint returns immediately, and this worker runs afterward. A failure at
// any step (download, upload, write-back) is logged and leaves result_pdf_url
// null — the report and score are unaffected, and the raw Digitap response
// (with the source URL) is already persisted.
//
// Volume here is low (one PDF per credit pull), so a single-worker loop is
// plenty. The bank-statement worker pool (bank_statement.go) is the precedent
// if this ever needs more throughput.
package service

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// pdfUploadDownloadTimeout bounds both the Digitap PDF download and (via the
// utho client's own timeout) the upload, so a stalled host can't pin the
// worker. The Digitap URL is valid for an hour; we run immediately, so this
// only guards against a hung connection.
const pdfUploadDownloadTimeout = 2 * time.Minute

// pdfJob is one queued relay: download sourceURL, encrypt, upload, and write the
// object URI back onto reportID.
type pdfJob struct {
	accountID int64
	reportID  int64
	sourceURL string
}

// pdfWriter is the repository seam the uploader needs. Narrowed to the one
// write method so the relay can be tested against a fake without a database;
// *repository.CreditAnalyticsRepo satisfies it at runtime.
type pdfWriter interface {
	SetResultPDFURL(ctx context.Context, id int64, url string) error
}

// pdfIdentityReader supplies the two facts the password is built from. Narrow on
// purpose: the relay has no business with the rest of an account.
type pdfIdentityReader interface {
	FindByID(ctx context.Context, id int64) (*models.Account, error)
	FindKYCByAccount(ctx context.Context, accountID int64) (*models.KYCRecord, error)
}

// pdfObjectStore is the storage seam. Utho is gone — its own API is
// S3-compatible, so moving back would be an endpoint override on the S3 client
// rather than a second implementation.
type pdfObjectStore interface {
	Upload(ctx context.Context, bucket, key, filename string, body []byte) (string, error)
	IsStub() bool
}

// ReportUploader runs the async Digitap→S3 PDF relay on a single goroutine.
// It is best-effort: Submit never blocks (drops + logs if the buffer is full),
// and process never propagates errors to a caller — failures just leave the
// row's result_pdf_url null.
type ReportUploader struct {
	store    pdfObjectStore
	repo     pdfWriter
	accounts pdfIdentityReader
	jobs     chan pdfJob
	wg       sync.WaitGroup
	once     sync.Once
}

// NewReportUploader constructs the uploader. buffer is the queue depth beyond
// the in-flight job; submitted jobs beyond it are dropped (best-effort).
func NewReportUploader(
	store pdfObjectStore,
	repo *repository.CreditAnalyticsRepo,
	accounts pdfIdentityReader,
	buffer int,
) *ReportUploader {
	if buffer < 1 {
		buffer = 16
	}
	return &ReportUploader{
		store:    store,
		repo:     repo,
		accounts: accounts,
		jobs:     make(chan pdfJob, buffer),
	}
}

// Submit queues a relay job without blocking. Returns false (and logs) if the
// buffer is full — best-effort means we drop rather than stall the request.
func (u *ReportUploader) Submit(accountID, reportID int64, sourceURL string) bool {
	select {
	case u.jobs <- pdfJob{accountID: accountID, reportID: reportID, sourceURL: sourceURL}:
		return true
	default:
		slog.Warn("credit-report pdf relay queue full; dropping upload",
			"report_id", reportID, "account_id", accountID)
		return false
	}
}

// Start spawns the worker. Idempotent (guarded by once). The ctx is the parent
// for every job; cancelling it (server shutdown) halts the worker once the
// queue drains.
func (u *ReportUploader) Start(ctx context.Context) {
	u.once.Do(func() {
		u.wg.Add(1)
		go u.worker(ctx)
	})
}

// worker drains the queue until Stop closes it, running one relay per job.
func (u *ReportUploader) worker(ctx context.Context) {
	defer u.wg.Done()
	for job := range u.jobs {
		u.process(ctx, job)
	}
}

// Stop closes the queue and waits for the in-flight job to finish. Idempotent.
// Called on shutdown so a graceful restart doesn't abandon a half-uploaded PDF.
func (u *ReportUploader) Stop() {
	close(u.jobs)
	u.wg.Wait()
}

// process runs the relay for one job: download from Digitap, encrypt with the
// holder's PAN + date of birth, upload to S3, write the object URI back to the
// row. Every failure is logged and swallowed (best-effort) — the row's
// result_pdf_url stays null and the credit-analytics request is unaffected.
func (u *ReportUploader) process(ctx context.Context, job pdfJob) {
	// Resolved before the download so a report that can never be protected costs
	// nothing to discover: no fetch, no encrypt, no object written.
	password, err := u.passwordFor(ctx, job.accountID)
	if err != nil {
		slog.Error("credit-report pdf skipped: cannot build its password",
			"report_id", job.reportID, "account_id", job.accountID, "error", err)
		return
	}

	pdfBytes, err := u.download(ctx, job.sourceURL)
	if err != nil {
		slog.Warn("credit-report pdf download failed",
			"report_id", job.reportID, "error", err)
		return
	}

	encrypted, err := EncryptReportPDF(pdfBytes, password)
	if err != nil {
		// Deliberately no unencrypted fallback. The report itself is already
		// persisted on the analytics row, so losing the PDF costs a convenience;
		// storing a readable one costs the protection this whole path exists for.
		slog.Error("credit-report pdf skipped: encryption failed",
			"report_id", job.reportID, "error", err)
		return
	}

	// Object layout: credit-reports/<account>/<report>.pdf
	key := fmt.Sprintf("credit-reports/%d/%d.pdf", job.accountID, job.reportID)
	filename := fmt.Sprintf("myscorr-credit-report-%d.pdf", job.reportID)

	uri, err := u.store.Upload(ctx, "", key, filename, encrypted)
	if err != nil {
		slog.Warn("credit-report pdf upload failed",
			"report_id", job.reportID, "key", key, "error", err)
		return
	}
	if err := u.repo.SetResultPDFURL(ctx, job.reportID, uri); err != nil {
		// The upload succeeded but we couldn't record the URI — the object exists
		// but the row won't point to it. Log loudly; there is no automatic
		// recovery, though the key is derivable from account and report id.
		slog.Error("credit-report pdf write-back failed",
			"report_id", job.reportID, "key", key, "error", err)
		return
	}
	slog.Info("credit-report pdf stored",
		"report_id", job.reportID, "key", key, "bytes", len(encrypted))
}

// passwordFor builds the PDF password from the account's verified PAN and date
// of birth.
//
// Both come from the account rather than the request: the file has to be
// openable by its holder later, from an email they kept, with facts they know —
// not with something we made up at generation time and would then have to store.
func (u *ReportUploader) passwordFor(ctx context.Context, accountID int64) (string, error) {
	acc, err := u.accounts.FindByID(ctx, accountID)
	if err != nil {
		return "", fmt.Errorf("load account: %w", err)
	}
	kyc, err := u.accounts.FindKYCByAccount(ctx, accountID)
	if err != nil {
		return "", fmt.Errorf("load kyc: %w", err)
	}
	return ReportPDFPassword(kyc.PANNumber, acc.DateOfBirth)
}

// download fetches the PDF bytes from the short-lived Digitap URL. The URL
// expires after ~1 hour; jobs run immediately on submit, so this is only an
// issue if the queue backs up (crash + recovery) — in which case the 4xx from
// Digitap fails the job best-effort.
func (u *ReportUploader) download(ctx context.Context, url string) ([]byte, error) {
	dlCtx, cancel := context.WithTimeout(ctx, pdfUploadDownloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build download request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download pdf: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download pdf: status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
