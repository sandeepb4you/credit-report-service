// Package bankdata is a thin HTTP client for the Digitap Bank Data PDF UI API
// (Digitap Statement Upload UI API Specifications, Version 1.20).
//
// It covers the four endpoints that compose the redirect/upload flow:
//
//	POST /bank-data/generateurl     — mint a Digitap UI URL for the user
//	POST /bank-data/statuscheck     — poll whether the report is ready
//	POST /bank-data/retrievereport  — fetch the generated JSON/XLSX report
//	POST /bank-data/institutionlist — list supported banks (not wired yet)
//
// When no client credentials are configured (ClientID == ""), New returns a
// stub client that never does I/O and replies with a synthesized success
// envelope, so the feature runs offline / in CI — the same convention used by
// the credit digitap client (internal/digitap) and the Cashfree gateway.
package bankdata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Config configures the Bank-Data client.
type Config struct {
	BaseURL      string
	ClientID     string
	ClientSecret string
	Timeout      time.Duration
}

// Client calls the Digitap Bank Data API. The zero value is not usable;
// construct one with New. When ClientID is empty the client runs in stub mode
// (IsStub) and no HTTP is performed.
type Client struct {
	cfg  Config
	http *http.Client
	stub bool
}

// New returns a Bank-Data client. If cfg.ClientID is empty, a stub client is
// returned: none of the methods perform I/O and all reply with canned success.
func New(cfg Config) *Client {
	c := &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.Timeout},
	}
	if cfg.Timeout == 0 {
		c.http.Timeout = 30 * time.Second
	}
	if cfg.ClientID == "" {
		c.stub = true
	}
	return c
}

// IsStub reports whether this client is the offline stub.
func (c *Client) IsStub() bool { return c.stub }

// GenerateURL calls POST /bank-data/generateurl. Returns the Digitap UI url,
// the request_id to correlate later calls, and the URL expiry.
func (c *Client) GenerateURL(ctx context.Context, req GenerateURLRequest) (*GenerateURLResponse, int, error) {
	return call(ctx, c, PathGenerateURL, req, func(raw []byte) (*GenerateURLResponse, error) {
		var r GenerateURLResponse
		return &r, json.Unmarshal(raw, &r)
	})
}

// StatusCheck calls POST /bank-data/statuscheck with a request_id and returns
// the per-transaction statuses.
func (c *Client) StatusCheck(ctx context.Context, requestID string) (*StatusCheckResponse, int, error) {
	return call(ctx, c, PathStatusCheck, StatusCheckRequest{RequestID: requestID}, func(raw []byte) (*StatusCheckResponse, error) {
		var r StatusCheckResponse
		return &r, json.Unmarshal(raw, &r)
	})
}

// RetrieveReport calls POST /bank-data/retrievereport and returns the raw
// report payload (Result). type2 gives the categorised JSON report.
func (c *Client) RetrieveReport(ctx context.Context, txnID string) (*RetrieveReportResponse, int, error) {
	req := RetrieveReportRequest{
		TxnID:         txnID,
		ReportType:    ReportTypeJSON,
		ReportSubtype: ReportSubtypeT2,
	}
	return call(ctx, c, PathRetrieveReport, req, func(raw []byte) (*RetrieveReportResponse, error) {
		var r RetrieveReportResponse
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		// The report body is sometimes inlined rather than wrapped under
		// "result"; in that case carry the raw bytes through as Result.
		if len(r.Result) == 0 && len(raw) > 0 {
			r.Result = raw
		}
		return &r, nil
	})
}

// InstitutionList calls POST /bank-data/institutionlist. Not wired into the
// service yet but exposed for completeness.
func (c *Client) InstitutionList(ctx context.Context) (*InstitutionListResponse, int, error) {
	return call(ctx, c, PathInstitutionList, struct{}{}, func(raw []byte) (*InstitutionListResponse, error) {
		var r InstitutionListResponse
		return &r, json.Unmarshal(raw, &r)
	})
}

// call is the shared POST wrapper (free function — Go disallows generic
// methods): marshal, Basic-auth, send, decode. In stub mode it dispatches to
// the stub helpers instead of HTTP. The decode func lifts the typed response
// out of the raw bytes. The returned int is the upstream HTTP status (200 in
// stub mode).
func call[T any](
	ctx context.Context,
	c *Client,
	path string,
	payload any,
	decode func([]byte) (*T, error),
) (*T, int, error) {
	if c.stub {
		return stubCall[T](path, payload, decode)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal bank-data request: %w", err)
	}

	url := strings.TrimRight(c.cfg.BaseURL, "/") + path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("build bank-data request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.SetBasicAuth(c.cfg.ClientID, c.cfg.ClientSecret)

	start := time.Now()
	resp, err := c.http.Do(httpReq)
	latencyMs := time.Since(start).Milliseconds()
	if err != nil {
		slog.Error("bank-data upstream call failed", "path", path, "latency_ms", latencyMs, "error", err)
		return nil, 0, fmt.Errorf("call bank-data: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read bank-data response: %w", err)
	}

	out, err := decode(raw)
	if err != nil {
		// A non-JSON body is usually an upstream error page; surface the status
		// so the caller can map it.
		slog.Error("bank-data response decode failed",
			"path", path, "upstream_status", resp.StatusCode, "error", err)
		return nil, resp.StatusCode, fmt.Errorf("decode bank-data response: %w", err)
	}
	slog.Info("bank-data upstream response",
		"path", path, "upstream_status", resp.StatusCode, "latency_ms", latencyMs)
	return out, resp.StatusCode, nil
}
