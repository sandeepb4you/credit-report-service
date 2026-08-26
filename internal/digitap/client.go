// Package digitap is a thin HTTP client for the Digitap Credit Analytics API
// (Digitap Credit Analytics API Doc & Integration Guide V2.7, section 1.4.1).
//
// It only covers the /credit_analytics/request endpoint used by this service.
// When no client credentials are configured (ClientID == ""), New returns a
// stub client that replies with a synthesized success envelope containing a
// realistic, randomized Experian-style INProfileResponse report (see mock.go).
// This lets the service return rich data offline / in CI / while the Digitap
// UAT endpoint is unavailable.
package digitap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

// Result codes documented in section 1.4.2.5 of the API spec.
const (
	ResultCodeRecordFound = 101 // record found successfully
	ResultCodeRetry       = 102 // need to call API again / no record
	ResultCodeNameMissing = 103 // name not found against mobile
)

// RequestPath is the credit-analytics request endpoint relative to BaseURL.
const RequestPath = "/credit_analytics/request"

// Config configures the Digitap client.
type Config struct {
	BaseURL      string
	ClientID     string
	ClientSecret string
	Timeout      time.Duration

	// LogRequestCurl writes every outgoing request as a runnable curl command.
	// Developer machines only — see config.DigitapConfig.LogRequestCurl for
	// what it exposes and the boot guard that keeps it off a deployment.
	LogRequestCurl bool
	// CurlOut is where that command goes. Nil means os.Stderr; tests inject a
	// buffer.
	CurlOut io.Writer
}

// Response is the Digitap response envelope (section 1.4.2). Result carries the
// full upstream payload verbatim; callers that only need top-level metadata use
// the scalar fields.
type Response struct {
	HTTPResponseCode int             `json:"http_response_code"`
	ClientRefNum     string          `json:"client_ref_num"`
	RequestID        string          `json:"request_id"`
	ResultCode       *int            `json:"result_code"`
	Message          string          `json:"message"`
	Result           json.RawMessage `json:"result"`
}

// Client calls the Digitap Credit Analytics API. The zero value is not usable;
// construct one with New.
type Client struct {
	cfg     Config
	http    *http.Client
	stub    bool
	curlOut io.Writer
}

// New returns a Digitap client. If cfg.ClientID is empty, a stub client is
// returned: Request never performs I/O and replies with a canned 101 envelope.
func New(cfg Config) *Client {
	c := &Client{
		cfg:     cfg,
		http:    &http.Client{Timeout: cfg.Timeout},
		curlOut: cfg.CurlOut,
	}
	if c.curlOut == nil {
		c.curlOut = os.Stderr
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

// curlCommand renders a request as a runnable curl command, so an upstream
// failure can be reproduced by hand without rebuilding the payload.
//
// -u is used rather than a pre-encoded Authorization header because that is the
// form a person can edit: curl base64s "id:secret" itself, producing the exact
// same header SetBasicAuth does. Nothing here is redacted — a redacted command
// will not run, which would defeat the only reason to log one.
func curlCommand(url, clientID, clientSecret string, body []byte) string {
	return fmt.Sprintf("curl -X POST %s -u %s -H 'Content-Type: application/json' -d %s",
		shellQuote(url),
		shellQuote(clientID+":"+clientSecret),
		shellQuote(string(body)),
	)
}

// shellQuote wraps s in single quotes for a POSIX shell, escaping any single
// quote inside it by closing, escaping, and reopening the quoted run. Needed
// because the JSON body is full of double quotes and could contain an
// apostrophe in a name.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Request calls POST <base_url>/credit_analytics/request with the supplied
// payload. The payload is JSON-marshalable (typically json.RawMessage already
// prepared by the caller). It returns the decoded Digitap envelope together
// with the HTTP status of the upstream call.
func (c *Client) Request(ctx context.Context, payload any) (*Response, int, error) {
	if c.stub {
		slog.Debug("digitap stub response (no live upstream configured)", "client_ref_num", clientRefOf(payload))
		return c.stubResponse(payload), http.StatusOK, nil
	}

	refNum := clientRefOf(payload)

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal digitap request: %w", err)
	}

	url := strings.TrimRight(c.cfg.BaseURL, "/") + RequestPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("build digitap request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.cfg.ClientID, c.cfg.ClientSecret)

	if c.cfg.LogRequestCurl {
		// Written raw rather than through slog. Any slog handler quotes an
		// attribute value and escapes the quotes inside it, so the JSON body
		// comes out as {\"pan\":\"...\"} and the command cannot be pasted into a
		// shell — which is the only reason to emit one. The banner carries the
		// warning that a log level otherwise would.
		fmt.Fprintf(c.curlOut,
			"\n--- digitap request %s — CONTAINS PAN, NAME, MOBILE AND THE CLIENT SECRET ---\n%s\n--- end digitap request ---\n\n",
			refNum, curlCommand(url, c.cfg.ClientID, c.cfg.ClientSecret, body))
	}

	slog.Debug("calling digitap upstream", "client_ref_num", refNum, "url", url)
	start := time.Now()
	resp, err := c.http.Do(req)
	latencyMs := time.Since(start).Milliseconds()
	if err != nil {
		slog.Error("digitap upstream call failed",
			"client_ref_num", refNum,
			"latency_ms", latencyMs,
			"error", err,
		)
		return nil, 0, fmt.Errorf("call digitap: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read digitap response: %w", err)
	}

	// The Digitap envelope is JSON at every documented HTTP status (incl. 4xx/5xx
	// error bodies), so always try to decode. If decoding fails on a non-2xx, fall
	// back to a minimal envelope carrying the upstream status.
	var env Response
	if jErr := json.Unmarshal(raw, &env); jErr != nil {
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			slog.Error("digitap response decode failed",
				"client_ref_num", refNum,
				"upstream_status", resp.StatusCode,
				"latency_ms", latencyMs,
				"error", jErr,
			)
			return nil, resp.StatusCode, fmt.Errorf("decode digitap response: %w (body: %s)", jErr, truncate(string(raw), 256))
		}
		env = Response{HTTPResponseCode: resp.StatusCode, Message: "upstream returned non-JSON error"}
	}
	if env.HTTPResponseCode == 0 {
		env.HTTPResponseCode = resp.StatusCode
	}
	slog.Info("digitap upstream response",
		"client_ref_num", refNum,
		"upstream_status", resp.StatusCode,
		"result_code", env.ResultCode,
		"latency_ms", latencyMs,
	)
	return &env, resp.StatusCode, nil
}

// clientRefOf extracts the client_ref_num correlation id from an opaque payload
// via a JSON round-trip, for log correlation. Returns "" if absent or if the
// payload isn't the expected shape.
func clientRefOf(payload any) string {
	b, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return ""
	}
	if v, ok := m["client_ref_num"].(string); ok {
		return v
	}
	return ""
}

func (c *Client) stubResponse(payload any) *Response {
	code := ResultCodeRecordFound
	msg := "success"
	return &Response{
		HTTPResponseCode: http.StatusOK,
		RequestID:        "00000000-0000-0000-0000-stub000000000",
		ResultCode:       &code,
		Message:          msg,
		Result:           generateReport(payload),
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
