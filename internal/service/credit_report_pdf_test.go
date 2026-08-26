package service

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"credit-report-service/internal/models"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// fakePDFWriter records SetResultPDFURL calls so tests can assert the
// write-back without a database.
type fakePDFWriter struct {
	mu  sync.Mutex
	got map[int64]string
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

// fakeObjectStore captures what would have gone to S3. Inspecting the uploaded
// bytes is the only way to prove the PDF was encrypted before it left the
// process — asserting on the password helper alone would pass while shipping
// readable reports.
type fakeObjectStore struct {
	mu       sync.Mutex
	puts     map[string][]byte
	failWith error
}

func (f *fakeObjectStore) Upload(_ context.Context, _, key, _ string, body []byte) (string, error) {
	if f.failWith != nil {
		return "", f.failWith
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.puts == nil {
		f.puts = map[string][]byte{}
	}
	stored := make([]byte, len(body))
	copy(stored, body)
	f.puts[key] = stored
	return "s3://test-bucket/" + key, nil
}

func (f *fakeObjectStore) IsStub() bool { return false }

func (f *fakeObjectStore) uploaded(key string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.puts[key]
	return b, ok
}

// fakeIdentity supplies the PAN and date of birth the password is built from.
type fakeIdentity struct {
	pan string
	dob *time.Time
	err error
}

func (f *fakeIdentity) FindByID(_ context.Context, id int64) (*models.Account, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &models.Account{ID: id, DateOfBirth: f.dob}, nil
}

func (f *fakeIdentity) FindKYCByAccount(_ context.Context, _ int64) (*models.KYCRecord, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &models.KYCRecord{PANNumber: f.pan}, nil
}

func testDOB(t *testing.T, s string) *time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatal(err)
	}
	return &d
}

// samplePDF is a real PDF: the relay encrypts what it downloads, so a stand-in
// byte string would fail for the wrong reason.
func samplePDF(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "sample.pdf"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

func newTestUploader(t *testing.T) (*ReportUploader, *fakePDFWriter, *fakeObjectStore) {
	t.Helper()
	fw := &fakePDFWriter{}
	fs := &fakeObjectStore{}
	u := &ReportUploader{
		store:    fs,
		repo:     fw,
		accounts: &fakeIdentity{pan: "ABCDE1234F", dob: testDOB(t, "1991-09-24")},
		jobs:     make(chan pdfJob, 4),
	}
	return u, fw, fs
}

// The happy path: download, encrypt, upload, write back the s3:// URI.
func TestProcess_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(samplePDF(t))
	}))
	defer srv.Close()

	u, fw, _ := newTestUploader(t)
	u.process(context.Background(), pdfJob{accountID: 7, reportID: 9, sourceURL: srv.URL})

	uri, ok := fw.got[9]
	if !ok {
		t.Fatalf("expected SetResultPDFURL(9, …); got %+v", fw.got)
	}
	// Object layout: credit-reports/<account>/<report>.pdf
	if !strings.Contains(uri, "credit-reports/7/9.pdf") {
		t.Errorf("write-back uri %q missing the agreed object path", uri)
	}
	// An s3:// URI, not an https URL: the bucket is private, so a URL would only
	// look usable. Reads presign this at request time.
	if !strings.HasPrefix(uri, "s3://") {
		t.Errorf("write-back uri %q should be an s3:// URI", uri)
	}
}

// The point of the whole path: what lands in the bucket must already be
// encrypted, and openable only with PAN + DDMMYYYY.
func TestProcess_UploadsAnEncryptedPDF(t *testing.T) {
	plain := samplePDF(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(plain)
	}))
	defer srv.Close()

	u, _, fs := newTestUploader(t)
	u.process(context.Background(), pdfJob{accountID: 7, reportID: 9, sourceURL: srv.URL})

	stored, ok := fs.uploaded("credit-reports/7/9.pdf")
	if !ok {
		t.Fatal("nothing was uploaded")
	}
	if bytes.Equal(stored, plain) {
		t.Fatal("the stored object is the plaintext PDF; it was never encrypted")
	}
	if err := api.Validate(bytes.NewReader(stored), model.NewDefaultConfiguration()); err == nil {
		t.Error("stored PDF opens with no password")
	}
	pw := "ABCDE1234F24091991"
	if err := api.Validate(bytes.NewReader(stored), model.NewAESConfiguration(pw, pw, 256)); err != nil {
		t.Errorf("stored PDF does not open with PAN+DDMMYYYY: %v", err)
	}
}

// No date of birth means no password, and a report that cannot be protected is
// not stored at all. Uploading it readable would defeat the requirement this
// path exists for.
func TestProcess_SkipsWhenThePasswordCannotBeBuilt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		identity *fakeIdentity
	}{
		{"no date of birth", &fakeIdentity{pan: "ABCDE1234F", dob: nil}},
		{"no pan", &fakeIdentity{pan: "", dob: testDOB(t, "1991-09-24")}},
		{"account lookup fails", &fakeIdentity{err: fmt.Errorf("db down")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hits := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				_, _ = w.Write(samplePDF(t))
			}))
			defer srv.Close()

			u, fw, fs := newTestUploader(t)
			u.accounts = tc.identity
			u.process(context.Background(), pdfJob{accountID: 1, reportID: 2, sourceURL: srv.URL})

			if len(fw.got) != 0 {
				t.Errorf("wrote back %+v; nothing should be recorded", fw.got)
			}
			if _, ok := fs.uploaded("credit-reports/1/2.pdf"); ok {
				t.Error("an unprotected PDF was uploaded")
			}
			// Resolved before the download, so an impossible job costs no fetch.
			if hits != 0 {
				t.Errorf("downloaded %d time(s); the password check should come first", hits)
			}
		})
	}
}

// Best-effort: a 404 from Digitap (the 1-hour URL expired) must not panic or
// write a URL.
func TestProcess_DownloadFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer srv.Close()

	u, fw, _ := newTestUploader(t)
	u.process(context.Background(), pdfJob{accountID: 1, reportID: 2, sourceURL: srv.URL})

	if len(fw.got) != 0 {
		t.Errorf("expected no write-back on download failure, got %+v", fw.got)
	}
}

func TestProcess_UnreachableSource(t *testing.T) {
	u, fw, _ := newTestUploader(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // connect now fails
	u.process(context.Background(), pdfJob{accountID: 1, reportID: 2, sourceURL: srv.URL})
	if len(fw.got) != 0 {
		t.Errorf("expected no write-back on unreachable source, got %+v", fw.got)
	}
}

// An upload failure leaves the row untouched rather than recording a URI for an
// object that is not there.
func TestProcess_UploadFailureLeavesTheRowAlone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(samplePDF(t))
	}))
	defer srv.Close()

	u, fw, fs := newTestUploader(t)
	fs.failWith = fmt.Errorf("s3 unavailable")
	u.process(context.Background(), pdfJob{accountID: 1, reportID: 2, sourceURL: srv.URL})

	if len(fw.got) != 0 {
		t.Errorf("recorded %+v for an object that was never stored", fw.got)
	}
}

// Drives the full Submit→worker→process pipeline once.
func TestSubmitAndDrain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(samplePDF(t))
	}))
	defer srv.Close()

	u, fw, _ := newTestUploader(t)
	u.Start(context.Background())
	defer u.Stop()

	if !u.Submit(3, 5, srv.URL) {
		t.Fatalf("Submit returned false on a non-full queue")
	}
	deadline := time.Now().Add(3 * time.Second)
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

// Submit returns false rather than blocking once the buffer saturates.
func TestSubmit_DroppedWhenFull(t *testing.T) {
	u, _, _ := newTestUploader(t)
	// Don't Start() the worker, so the buffer fills and stays full.
	for i := int64(1); i <= 4; i++ {
		u.jobs <- pdfJob{accountID: 1, reportID: i, sourceURL: "x"}
	}
	if u.Submit(1, 5, "x") {
		t.Errorf("Submit on a full queue should return false (best-effort drop)")
	}
}
