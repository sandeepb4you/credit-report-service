// Package digitap is a thin HTTP client for the Digitap Credit Analytics API
// (Digitap Credit Analytics API Doc & Integration Guide V2.7, section 1.4.1).
//
// It only covers the /credit_analytics/request endpoint used by this service.
// When no client credentials are configured (ClientID == ""), New returns a
// stub client that replies with a canned success envelope so the service can
// be built and exercised offline / in CI.
package digitap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	cfg  Config
	http *http.Client
	stub bool
}

// New returns a Digitap client. If cfg.ClientID is empty, a stub client is
// returned: Request never performs I/O and replies with a canned 101 envelope.
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

// Request calls POST <base_url>/credit_analytics/request with the supplied
// payload. The payload is JSON-marshalable (typically json.RawMessage already
// prepared by the caller). It returns the decoded Digitap envelope together
// with the HTTP status of the upstream call.
func (c *Client) Request(ctx context.Context, payload any) (*Response, int, error) {
	if c.stub {
		return c.stubResponse(payload), http.StatusOK, nil
	}

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

	resp, err := c.http.Do(req)
	if err != nil {
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
			return nil, resp.StatusCode, fmt.Errorf("decode digitap response: %w (body: %s)", jErr, truncate(string(raw), 256))
		}
		env = Response{HTTPResponseCode: resp.StatusCode, Message: "upstream returned non-JSON error"}
	}
	if env.HTTPResponseCode == 0 {
		env.HTTPResponseCode = resp.StatusCode
	}
	return &env, resp.StatusCode, nil
}

func (c *Client) stubResponse(payload any) *Response {
	code := ResultCodeRecordFound
	msg := "success"
	return &Response{
		HTTPResponseCode: http.StatusOK,
		RequestID:        "00000000-0000-0000-0000-stub000000000",
		ResultCode:       &code,
		Message:          msg,
		Result:           stubResult(payload),
	}
}

// stubResult mirrors the shape of a successful result.result_json payload just
// enough to be a non-empty placeholder; it is only used by the offline stub.
func stubResult(payload any) json.RawMessage {
	ref := ""
	if b, err := json.Marshal(payload); err == nil {
		var m map[string]any
		if json.Unmarshal(b, &m) == nil {
			if v, ok := m["client_ref_num"].(string); ok {
				ref = v
			}
		}
	}
	b, _ := json.Marshal(map[string]any{
		"result_json": map[string]any{
			"INProfileResponse": map[string]any{
				"stub":           true,
				"client_ref_num": ref,
			},
		},
	})
	return b
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
