package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"credit-report-service/internal/utho"
)

// fakePDFWriter records SetResultPDFURL calls so tests can assert the
// write-back without a database.
type fakePDFWriter struct {
	mu   sync.Mutex
	got  map[int64]string
	errs int
}

func (f *fakePDFWriter) SetResultPDFURL(_ context.Context, id int64, url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.got == nil {
		f.got = map[int64]string{}
	}
	f.got[id] = url
	return nil
}

// newTestUploader builds an uploader wired to a stub utho client and a fake
// repo, ready to drive process() directly.
func newTestUploader(t *testing.T) (*ReportUploader, *fakePDFWriter) {
	t.Helper()
	uthoClient := utho.New(utho.Config{DCSlug: "dc", Bucket: "credit-reports"})
	fw := &fakePDFWriter{}
	u := &ReportUploader{
		client: uthoClient,
		repo:   fw,
		bucket: "credit-reports",
		jobs:   make(chan pdfJob, 4),
	}
	return u, fw
}

// TestProcess_HappyPath stands up an httptest server as Digitap's result_pdf
// URL, runs process(), and asserts the downloaded bytes are written back to the
// row via the repo with a Utho URL carrying the agreed object path.
func TestProcess_HappyPath(t *testing.T) {
	pdfBody := []byte("%PDF-1.4 hello report")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(pdfBody)
	}))
	defer srv.Close()

	u, fw := newTestUploader(t)
	u.process(context.Background(), pdfJob{accountID: 7, reportID: 9, sourceURL: srv.URL})

	url, ok := fw.got[9]
	if !ok {
		t.Fatalf("expected SetResultPDFURL(9, …) to be called; got %+v", fw.got)
	}
	// Path layout agreed with the user: credit-reports/<account>/<report>.pdf
	if !strings.Contains(url, "credit-reports/7/9.pdf") {
		t.Errorf("write-back url %q missing the agreed object path", url)
	}
}

// TestProcess_DownloadFailure leaves the row untouched. Best-effort: a 404 from
// Digitap (e.g. the 1-hour URL expired) must not panic or write a URL.
func TestProcess_DownloadFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()

	u, fw := newTestUploader(t)
	u.process(context.Background(), pdfJob{accountID: 1, reportID: 2, sourceURL: srv.URL})

	if len(fw.got) != 0 {
		t.Errorf("expected no write-back on download failure, got %+v", fw.got)
	}
}

// TestProcess_UnreachableSource also leaves the row untouched when the source
// host is down (best-effort, no panic).
func TestProcess_UnreachableSource(t *testing.T) {
	u, fw := newTestUploader(t)
	// A closed server returns connection errors on connect.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	u.process(context.Background(), pdfJob{accountID: 1, reportID: 2, sourceURL: srv.URL})
	if len(fw.got) != 0 {
		t.Errorf("expected no write-back on unreachable source, got %+v", fw.got)
	}
}

// TestSubmitAndDrain drives the full Submit→worker→process pipeline once and
// confirms the job is processed. Uses a stub utho client + httptest source so
// no external calls are made.
func TestSubmitAndDrain(t *testing.T) {
	pdfBody := []byte("%PDF-1.4")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(pdfBody)
	}))
	defer srv.Close()

	u, fw := newTestUploader(t)
	u.Start(context.Background())
	defer u.Stop()

	if !u.Submit(3, 5, srv.URL) {
		t.Fatalf("Submit returned false on a non-full queue")
	}
	// Poll until the write-back lands (worker runs async). Best-effort timeout.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fw.mu.Lock()
		done := len(fw.got) > 0
		fw.mu.Unlock()
		if done {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, ok := fw.got[5]; !ok {
		t.Fatalf("expected report 5 to be processed after Submit; got %+v", fw.got)
	}
}

// TestSubmit_DroppedWhenFull confirms Submit returns false (and does not block)
// once the buffer is saturated — the best-effort drop behavior.
func TestSubmit_DroppedWhenFull(t *testing.T) {
	u, _ := newTestUploader(t)
	// Don't Start() the worker, so the buffer fills and stays full.
	u.jobs <- pdfJob{accountID: 1, reportID: 1, sourceURL: "x"} // fill slot 1
	u.jobs <- pdfJob{accountID: 1, reportID: 2, sourceURL: "x"} // fill slot 2
	u.jobs <- pdfJob{accountID: 1, reportID: 3, sourceURL: "x"} // fill slot 3
	u.jobs <- pdfJob{accountID: 1, reportID: 4, sourceURL: "x"} // fill slot 4 (cap)
	if u.Submit(1, 5, "x") {
		t.Errorf("Submit on a full queue should return false (best-effort drop)")
	}
}
