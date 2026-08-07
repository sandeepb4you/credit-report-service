// Package utho is a thin HTTP client for the Utho Cloud object-storage upload
// API (POST /v2/objectstorage/:dcslug/bucket/:name/upload/).
//
// It covers the single operation this service needs: uploading a downloaded
// credit-report PDF to a configured bucket and getting back a public URL.
//
// When no API token is configured (APIToken == ""), New returns a stub client
// that never does I/O and replies with a synthesized success URL, so the
// feature runs offline / in CI — the same convention used by the bankdata,
// digitap, and payments clients.
package utho

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL is the Utho API host. Rarely overridden; Config.BaseURL exists
// for testing against a mock server.
const DefaultBaseURL = "https://api.utho.com/v2"

// Client uploads files to Utho object storage. The zero value is not usable;
// construct one with New. When APIToken is empty the client runs in stub mode
// (IsStub) and no HTTP is performed.
type Client struct {
	cfg  Config
	http *http.Client
	stub bool
}

// New returns a Utho client. If cfg.APIToken is empty, a stub client is
// returned: Upload never performs I/O and returns a canned URL.
func New(cfg Config) *Client {
	c := &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.Timeout},
	}
	if cfg.Timeout == 0 {
		c.http.Timeout = 60 * time.Second // uploads can be larger than API calls
	}
	if cfg.BaseURL == "" {
		c.cfg.BaseURL = DefaultBaseURL
	}
	if cfg.APIToken == "" {
		c.stub = true
	}
	return c
}

// IsStub reports whether this client is the offline stub.
func (c *Client) IsStub() bool { return c.stub }

// Upload stores fileBytes under path in the configured bucket and returns the
// object's public URL. filename is the multipart "filename" hint (the stored
// object's name comes from path, but Utho echoes filename in headers). bucket
// overrides Config.Bucket when non-empty.
func (c *Client) Upload(ctx context.Context, bucket, path, filename string, fileBytes []byte) (string, error) {
	if c.stub {
		return c.stubUpload(bucket, path), nil
	}
	if bucket == "" {
		bucket = c.cfg.Bucket
	}
	if bucket == "" {
		return "", fmt.Errorf("utho upload: bucket is not configured")
	}

	// Build the multipart form: two fields, "file" (the PDF) and "path".
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("build multipart form: %w", err)
	}
	if _, err := fw.Write(fileBytes); err != nil {
		return "", fmt.Errorf("write file to form: %w", err)
	}
	if err := w.WriteField("path", path); err != nil {
		return "", fmt.Errorf("write path field: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("close multipart form: %w", err)
	}

	url := fmt.Sprintf("%s/objectstorage/%s/bucket/%s/upload/",
		strings.TrimRight(c.cfg.BaseURL, "/"), c.cfg.DCSlug, bucket)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return "", fmt.Errorf("build utho request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIToken)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("call utho: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("utho upload failed: status %d, body: %s", resp.StatusCode, truncate(string(raw), 256))
	}

	var env UploadResponse
	if err := json.Unmarshal(raw, &env); err != nil {
		// Non-JSON success body: fall back to a constructed URL.
		return c.objectURL(bucket, path), nil
	}
	if env.Status == "error" {
		return "", fmt.Errorf("utho upload error: %s", env.Msg)
	}
	// Prefer an echoed URL; otherwise construct one from the bucket + path.
	if u := env.Link; u != "" {
		return u, nil
	}
	if u := env.URL; u != "" {
		return u, nil
	}
	return c.objectURL(bucket, path), nil
}

// objectURL constructs a public URL for an object when Utho doesn't echo one.
// Utho serves buckets at https://<bucket>.<dcslug>.celitech.in/<path>; this is a
// best-effort fallback (the upload response usually carries the real link).
func (c *Client) objectURL(bucket, path string) string {
	host := fmt.Sprintf("%s.%s.utho.io", bucket, c.cfg.DCSlug)
	if c.cfg.DCSlug == "" {
		host = bucket + ".utho.io"
	}
	return fmt.Sprintf("https://%s/%s", host, strings.TrimLeft(path, "/"))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
