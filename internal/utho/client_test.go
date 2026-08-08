package utho

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestClient_StubByDefault confirms empty credentials yield a stub client that
// never performs I/O — the convention every external-capability package follows.
func TestClient_StubByDefault(t *testing.T) {
	c := New(Config{DCSlug: "dc", Bucket: "bkt"})
	if !c.IsStub() {
		t.Fatalf("empty api-token should produce a stub client")
	}
	c2 := New(Config{APIToken: "x", DCSlug: "dc", Bucket: "bkt"})
	if c2.IsStub() {
		t.Fatalf("non-empty api-token should produce a real client")
	}
}

// TestStub_UploadReturnsURL confirms the stub returns a deterministic URL that
// carries the bucket and path, so dev/CI can exercise the relay end-to-end.
func TestStub_UploadReturnsURL(t *testing.T) {
	c := New(Config{DCSlug: "lon", Bucket: "credit-reports"})
	url, err := c.Upload(context.Background(), "", "credit-reports/1/2.pdf", "2.pdf", []byte("%PDF-1.4"))
	if err != nil {
		t.Fatalf("stub Upload error: %v", err)
	}
	if !strings.Contains(url, "credit-reports") || !strings.Contains(url, "lon") {
		t.Errorf("stub URL %q missing bucket or dc-slug", url)
	}
	if !strings.HasSuffix(url, "credit-reports/1/2.pdf") {
		t.Errorf("stub URL %q should end with the object path", url)
	}
}

// TestStub_UploadFallsBackForEmptyBucket confirms the stub uses safe placeholders
// when neither the call nor the config supplies a bucket/dc-slug.
func TestStub_UploadFallsBackForEmptyBucket(t *testing.T) {
	c := New(Config{})
	url, err := c.Upload(context.Background(), "", "p/f.pdf", "f.pdf", []byte("x"))
	if err != nil {
		t.Fatalf("stub Upload error: %v", err)
	}
	if !strings.Contains(url, "stub-bucket") || !strings.Contains(url, "stub-dc") {
		t.Errorf("stub URL %q missing fallback bucket/dc", url)
	}
}

// TestReal_Upload_HappyPath stands up an httptest server mimicking Utho's
// upload endpoint and asserts the client sends Bearer auth, multipart "file"
// and "path", and returns the echoed link.
func TestReal_Upload_HappyPath(t *testing.T) {
	var (
		gotAuth     string
		gotCT       string
		gotPath     string
		gotFileName string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		// Path should contain /objectstorage/<dc>/bucket/<bucket>/upload/
		if !strings.Contains(r.URL.Path, "/objectstorage/lon/bucket/credit-reports/upload/") {
			t.Errorf("unexpected URL path: %q", r.URL.Path)
		}
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
			http.Error(w, err.Error(), 400)
			return
		}
		if v := r.MultipartForm.Value["path"]; len(v) > 0 {
			gotPath = v[0]
		}
		if fhs := r.MultipartForm.File["file"]; len(fhs) > 0 {
			gotFileName = fhs[0].Filename
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"link":   "https://credit-reports.lon.utho.io/credit-reports/9/9.pdf",
		})
	}))
	defer srv.Close()

	c := New(Config{APIToken: "tok", DCSlug: "lon", Bucket: "credit-reports", BaseURL: srv.URL})
	url, err := c.Upload(context.Background(), "", "credit-reports/9/9.pdf", "9.pdf", []byte("%PDF-1.4 stub"))
	if err != nil {
		t.Fatalf("Upload error: %v", err)
	}
	if url != "https://credit-reports.lon.utho.io/credit-reports/9/9.pdf" {
		t.Errorf("url = %q, want the echoed link", url)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth header = %q, want Bearer tok", gotAuth)
	}
	if !strings.HasPrefix(gotCT, "multipart/form-data") {
		t.Errorf("content-type = %q, want multipart/form-data", gotCT)
	}
	if gotPath != "credit-reports/9/9.pdf" {
		t.Errorf("path field = %q", gotPath)
	}
	if gotFileName != "9.pdf" {
		t.Errorf("file filename = %q", gotFileName)
	}
}

// TestReal_Upload_ConstructsURLWhenNoLink confirms the client falls back to a
// constructed object URL when Utho's response omits a link/url.
func TestReal_Upload_ConstructsURLWhenNoLink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success"})
	}))
	defer srv.Close()

	c := New(Config{APIToken: "tok", DCSlug: "lon", Bucket: "credit-reports", BaseURL: srv.URL})
	url, err := c.Upload(context.Background(), "", "credit-reports/9/9.pdf", "9.pdf", []byte("x"))
	if err != nil {
		t.Fatalf("Upload error: %v", err)
	}
	if !strings.Contains(url, "credit-reports") || !strings.Contains(url, "lon") {
		t.Errorf("constructed url %q missing bucket/dc", url)
	}
}

// TestReal_Upload_HTTPError surfaces a non-2xx as an error.
func TestReal_Upload_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New(Config{APIToken: "tok", DCSlug: "lon", Bucket: "credit-reports", BaseURL: srv.URL})
	if _, err := c.Upload(context.Background(), "", "p/f.pdf", "f.pdf", []byte("x")); err == nil {
		t.Fatalf("expected error on 401, got nil")
	}
}

// TestReal_Upload_StatusErrorField treats an "error" status in a 2xx body as a
// failure (Utho sometimes returns 200 with status:error).
func TestReal_Upload_StatusErrorField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "msg": "bucket missing"})
	}))
	defer srv.Close()

	c := New(Config{APIToken: "tok", DCSlug: "lon", Bucket: "credit-reports", BaseURL: srv.URL})
	if _, err := c.Upload(context.Background(), "", "p/f.pdf", "f.pdf", []byte("x")); err == nil {
		t.Fatalf("expected error on status:error body, got nil")
	}
}
