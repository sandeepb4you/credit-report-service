// Package service — async credit-report PDF relay (Digitap → Utho).
//
// When a credit-analytics request uses report_type 3, Digitap returns
// result_pdf: a URL for the generated PDF that is valid for ~1 hour. This file
// implements the off-request relay that downloads that PDF and re-uploads it to
// Utho object storage, storing the permanent Utho URL on the analytics row.
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

	"credit-report-service/internal/repository"
	"credit-report-service/internal/utho"
)

// pdfUploadDownloadTimeout bounds both the Digitap PDF download and (via the
// utho client's own timeout) the upload, so a stalled host can't pin the
// worker. The Digitap URL is valid for an hour; we run immediately, so this
// only guards against a hung connection.
const pdfUploadDownloadTimeout = 2 * time.Minute

// pdfJob is one queued relay: download sourceURL, upload to Utho at path, write
// the Utho URL back onto reportID.
type pdfJob struct {
	accountID int64
	reportID  int64
	sourceURL string
}

// ReportUploader runs the async Digitap→Utho PDF relay on a single goroutine.
// It is best-effort: Submit never blocks (drops + logs if the buffer is full),
// and process never propagates errors to a caller — failures just leave the
// row's result_pdf_url null.
// pdfWriter is the repository seam the uploader needs. Narrowed to the one
// write method so the relay can be tested against a fake without a database;
// *repository.CreditAnalyticsRepo satisfies it at runtime.
type pdfWriter interface {
	SetResultPDFURL(ctx context.Context, id int64, url string) error
}

type ReportUploader struct {
	client *utho.Client
	repo   pdfWriter
	bucket string
	jobs   chan pdfJob
	wg     sync.WaitGroup
	once   sync.Once
}

// NewReportUploader constructs the uploader. buffer is the queue depth beyond
// the in-flight job; submitted jobs beyond it are dropped (best-effort).
func NewReportUploader(client *utho.Client, repo *repository.CreditAnalyticsRepo, bucket string, buffer int) *ReportUploader {
	if buffer < 1 {
		buffer = 16
	}
	return &ReportUploader{
		client: client,
		repo:   repo,
		bucket: bucket,
		jobs:   make(chan pdfJob, buffer),
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

// process runs the three-step relay for one job: download from Digitap, upload
// to Utho, write the Utho URL back to the row. Every failure is logged and
// swallowed (best-effort) — the row's result_pdf_url stays null and the
// credit-analytics request is unaffected.
func (u *ReportUploader) process(ctx context.Context, job pdfJob) {
	pdfBytes, err := u.download(ctx, job.sourceURL)
	if err != nil {
		slog.Warn("credit-report pdf download failed",
			"report_id", job.reportID, "error", err)
		return
	}

	// Object path follows the agreed layout: credit-reports/<account>/<report>.pdf
	path := fmt.Sprintf("credit-reports/%d/%d.pdf", job.accountID, job.reportID)
	filename := fmt.Sprintf("%d.pdf", job.reportID)

	uthoURL, err := u.client.Upload(ctx, u.bucket, path, filename, pdfBytes)
	if err != nil {
		slog.Warn("credit-report pdf upload to utho failed",
			"report_id", job.reportID, "path", path, "error", err)
		return
	}
	if err := u.repo.SetResultPDFURL(ctx, job.reportID, uthoURL); err != nil {
		// The upload succeeded but we couldn't record the URL — the object
		// exists in Utho but the row won't point to it. Log loudly; there's no
		// automatic recovery (the URL would need to be reconstructed by path).
		slog.Error("credit-report pdf write-back failed",
			"report_id", job.reportID, "utho_url", uthoURL, "error", err)
		return
	}
	slog.Info("credit-report pdf relayed to utho",
		"report_id", job.reportID, "path", path, "bytes", len(pdfBytes))
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
