package digitap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Mobile to Prefill API (Digitap Mobile to Prefill - API Doc v1.4, 17 Apr 2026).
//
// Given a mobile number the API returns the identity attributes registered
// against it — name, DOB, PAN, email, addresses. This service uses it for one
// thing only: confirming that the PAN and name a user typed at signup belong to
// the mobile number they just proved control of over SMS OTP.
//
// It is a DIFFERENT product from the Credit Analytics API in client.go: a
// different endpoint, and per the doc a separately provisioned client id — do
// not assume one set of credentials unlocks both.

// PrefillRequestPath is the endpoint relative to the configured base URL.
const PrefillRequestPath = "/mobile_prefill/request"

// Result codes, section 1.4.2. 101 is the only one carrying a result body.
const (
	PrefillFound       = 101 // record found
	PrefillNoRecord    = 102 // no record against this mobile
	PrefillNameMissing = 103 // record exists, no name against the mobile
)

// Errors the caller is expected to branch on. Everything else surfaces as a
// wrapped transport/decode error.
var (
	// ErrPrefillAuth — bad credentials (401). A deploy-time misconfiguration,
	// not something a user can fix by retyping.
	ErrPrefillAuth = errors.New("digitap prefill: client authentication failed")
	// ErrPrefillServiceDisabled — the name-lookup service (or a requested
	// option) is not provisioned on this client id: 401 "name look-up service
	// is not enabled", or a 403 naming the option. Provisioning is per client id,
	// so credentials that work elsewhere can still fail here.
	ErrPrefillServiceDisabled = errors.New("digitap prefill: service not enabled for this client id")
	// ErrPrefillIPNotAllowed — 403 "Forbidden: IP not allowed". Digitap
	// allowlists caller IPs, so this says nothing about the credentials or the
	// contract: the same pair works from an allowlisted address and fails from
	// anywhere else. It is what a deployment hits after being developed against
	// a whitelisted office or laptop IP.
	//
	// Separate from ErrPrefillServiceDisabled because the two need opposite
	// actions and the wrong label sends you to the wrong conversation: a
	// provisioning gap is a question about entitlements, an IP block is "here is
	// my server's address, please add it".
	ErrPrefillIPNotAllowed = errors.New("digitap prefill: caller IP is not allowlisted by the provider")
	// ErrPrefillSource — 503 upstream data-source failure. The doc is explicit:
	// DO NOT retry this one.
	ErrPrefillSource = errors.New("digitap prefill: source error")
)

// mobileDigits matches the 10-digit national number the API expects. The spec
// permits a +91/0 prefix but the plain form is unambiguous, so the client
// normalizes to it.
var mobileDigits = regexp.MustCompile(`^[6-9]\d{9}$`)

// PrefillConfig configures the prefill client. An empty ClientID selects the
// offline stub, mirroring the Credit Analytics client's behaviour.
type PrefillConfig struct {
	BaseURL      string
	ClientID     string
	ClientSecret string
	Timeout      time.Duration
}

// PrefillDocument is one entry of result.official_documents.
type PrefillDocument struct {
	Document   string `json:"document"`
	DocumentID string `json:"document_id"`
}

// PrefillResult is the subset of result this service consumes. The API returns
// considerably more (addresses, alternate contacts, employment, a bureau
// score); those are deliberately not decoded — collecting personal data we have
// no use for is exactly what DPDP purpose-limitation prohibits.
type PrefillResult struct {
	Name              string            `json:"name"`
	DOB               string            `json:"dob"`
	PAN               string            `json:"pan"`
	OfficialDocuments []PrefillDocument `json:"official_documents"`
}

// PANFromDocuments returns the PAN listed in official_documents, which some
// responses populate when the top-level pan field is empty.
func (r *PrefillResult) PANFromDocuments() string {
	for _, d := range r.OfficialDocuments {
		if strings.EqualFold(d.Document, "PAN") {
			return d.DocumentID
		}
	}
	return ""
}

// BestPAN prefers the top-level pan and falls back to official_documents.
func (r *PrefillResult) BestPAN() string {
	if strings.TrimSpace(r.PAN) != "" {
		return r.PAN
	}
	return r.PANFromDocuments()
}

// PrefillOutcome is one lookup's answer. Result is nil for every code except
// PrefillFound.
type PrefillOutcome struct {
	ResultCode int
	RequestID  string
	Message    string
	Result     *PrefillResult
}

// PrefillClient calls the Mobile to Prefill API.
type PrefillClient struct {
	cfg  PrefillConfig
	http *http.Client
	stub bool
}

// NewPrefill returns a prefill client, or an offline stub when no client id is
// configured. Callers should check IsStub before treating a match as proof of
// identity.
func NewPrefill(cfg PrefillConfig) *PrefillClient {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &PrefillClient{
		cfg:  cfg,
		http: &http.Client{Timeout: timeout},
		stub: strings.TrimSpace(cfg.ClientID) == "",
	}
}

// IsStub reports whether this client answers from the offline stub.
func (c *PrefillClient) IsStub() bool { return c.stub }

// StubName and StubPAN are what the offline stub claims is registered against
// every mobile number. They are fixed rather than echoed back from the request
// on purpose: a stub that agrees with whatever the user typed would rubber-stamp
// the comparison this whole flow exists to perform, and the first time anyone
// noticed would be in production. Enter these to pass PAN verification locally.
const (
	StubName = "JOHN DOE"
	StubPAN  = "ABCDE1234F"
)

// Lookup asks which identity is registered against mobile. clientRef is echoed
// back by the API and appears in Digitap's logs; it must be unique per request
// and match ^[a-zA-Z0-9 _-]*$ (max 45 chars).
//
// name_lookup is always 1: Digitap resolves the name from the mobile number and
// tells us what it found. Passing our own first/last name instead (name_lookup
// 0) would make the API search for the name we supplied, so a "match" would
// only confirm that our own input was echoed back — useless as verification.
func (c *PrefillClient) Lookup(ctx context.Context, clientRef, mobile string) (*PrefillOutcome, error) {
	national, err := NationalMobile(mobile)
	if err != nil {
		return nil, err
	}

	if c.stub {
		slog.Debug("digitap prefill stub response (no credentials configured)", "client_ref_num", clientRef)
		return &PrefillOutcome{
			ResultCode: PrefillFound,
			RequestID:  "stub-" + clientRef,
			Message:    "success (stub)",
			Result:     &PrefillResult{Name: StubName, PAN: StubPAN},
		}, nil
	}

	payload := map[string]any{
		"client_ref_num": clientRef,
		"mobile_no":      national,
		"name_lookup":    1,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal prefill request: %w", err)
	}

	url := strings.TrimRight(c.cfg.BaseURL, "/") + PrefillRequestPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build prefill request: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	req.SetBasicAuth(c.cfg.ClientID, c.cfg.ClientSecret)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call prefill: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read prefill response: %w", err)
	}

	var env struct {
		HTTPResponseCode int             `json:"http_response_code"`
		ClientRefNum     string          `json:"client_ref_num"`
		RequestID        string          `json:"request_id"`
		ResultCode       *int            `json:"result_code"`
		Message          string          `json:"message"`
		Result           json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		// Never log raw: an error body can carry the mobile number back.
		return nil, fmt.Errorf("decode prefill response (http %d): %w", resp.StatusCode, err)
	}

	// The envelope carries its own status, and the doc's failure examples pair
	// it with the matching HTTP code — branch on the transport status, which is
	// the one a proxy or WAF can also produce.
	switch resp.StatusCode {
	case http.StatusOK:
		// fall through
	case http.StatusUnauthorized:
		if strings.Contains(strings.ToLower(env.Message), "look-up") {
			return nil, fmt.Errorf("%w: %s", ErrPrefillServiceDisabled, env.Message)
		}
		return nil, ErrPrefillAuth
	case http.StatusForbidden:
		// Digitap returns 403 both for an IP that is not allowlisted and for an
		// un-provisioned option. Only the message distinguishes them, and they
		// need opposite fixes, so branch on it rather than labelling every 403 a
		// provisioning problem.
		if strings.Contains(strings.ToLower(env.Message), "ip not allowed") {
			return nil, fmt.Errorf("%w (upstream said: %s)", ErrPrefillIPNotAllowed, env.Message)
		}
		return nil, fmt.Errorf("%w: %s", ErrPrefillServiceDisabled, env.Message)
	case http.StatusServiceUnavailable:
		return nil, ErrPrefillSource
	default:
		return nil, fmt.Errorf("digitap prefill: http %d: %s", resp.StatusCode, env.Message)
	}

	if env.ResultCode == nil {
		return nil, fmt.Errorf("digitap prefill: no result_code in a 200 response: %s", env.Message)
	}

	out := &PrefillOutcome{
		ResultCode: *env.ResultCode,
		RequestID:  env.RequestID,
		Message:    env.Message,
	}
	if out.ResultCode == PrefillFound && len(env.Result) > 0 {
		var r PrefillResult
		if err := json.Unmarshal(env.Result, &r); err != nil {
			return nil, fmt.Errorf("decode prefill result: %w", err)
		}
		out.Result = &r
	}
	return out, nil
}

// NationalMobile reduces a stored E.164 number ("+919876543210") to the bare
// ten digits the API expects. It rejects anything that is not a valid Indian
// mobile rather than sending a malformed number and paying for a 400.
func NationalMobile(mobile string) (string, error) {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, mobile)

	// Trim the country code only from a 12-digit number: a bare 10-digit
	// number can legitimately begin with 91, and trimming that looks up a
	// different person entirely.
	if len(digits) == 12 && strings.HasPrefix(digits, "91") {
		digits = digits[2:]
	}
	if len(digits) == 11 && strings.HasPrefix(digits, "0") {
		digits = digits[1:]
	}
	if !mobileDigits.MatchString(digits) {
		return "", fmt.Errorf("digitap prefill: %q is not a valid Indian mobile number", maskMobile(digits))
	}
	return digits, nil
}

// maskMobile keeps an error message diagnosable without putting a full mobile
// number into a log line.
func maskMobile(s string) string {
	if len(s) <= 4 {
		return strings.Repeat("*", len(s))
	}
	return strings.Repeat("*", len(s)-4) + s[len(s)-4:]
}
