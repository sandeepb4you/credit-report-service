package sms

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

	"credit-report-service/internal/config"
)

// DefaultBaseURL is the MSG91 API root. Config.BaseURL exists to point the
// client at a mock server in tests.
const DefaultBaseURL = "https://control.msg91.com/api/v5"

// DefaultOTPVar is the template variable the code is substituted into. MSG91
// templates name their placeholders (##OTP##), and the recipient object keys
// must match those names exactly — they are case-sensitive.
const DefaultOTPVar = "OTP"

// MSG91Client sends SMS through MSG91's v5 Flow API
// (POST /api/v5/flow/, authkey header).
//
// The Flow API only fills variables in a template that has already been
// approved on India's DLT registry — the message wording lives at MSG91, not
// here. That is why this client takes a template ID and a variable name rather
// than message text: there is no way to send arbitrary copy, by design.
type MSG91Client struct {
	cfg     config.MSG91Config
	baseURL string
	otpVar  string
	http    *http.Client
}

// NewMSG91Client builds the HTTP-backed sender. Call it only when AuthKey is
// set; an empty key means the caller should use NewStubSender instead.
func NewMSG91Client(cfg config.MSG91Config) *MSG91Client {
	base := strings.TrimSuffix(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	otpVar := strings.TrimSpace(cfg.OTPVar)
	if otpVar == "" {
		otpVar = DefaultOTPVar
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &MSG91Client{
		cfg:     cfg,
		baseURL: base,
		otpVar:  otpVar,
		http:    &http.Client{Timeout: timeout},
	}
}

func (c *MSG91Client) IsStub() bool { return false }

// flowRequest is the POST /flow/ body. Recipients is a list of maps because the
// template variables are named by the template, not by us — there is no fixed
// struct that fits every account's template.
type flowRequest struct {
	TemplateID string              `json:"template_id"`
	Sender     string              `json:"sender,omitempty"`
	ShortURL   string              `json:"short_url,omitempty"`
	Recipients []map[string]string `json:"recipients"`
}

// flowResponse is MSG91's reply. Note "type": the API answers HTTP 200 with
// {"type":"error"} for application-level failures (unknown template, blocked
// number, no balance), so the status code alone is not a success signal.
type flowResponse struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// SendOTP fills the configured template with otp and posts it to MSG91.
func (c *MSG91Client) SendOTP(ctx context.Context, phone, otp string) error {
	if c.cfg.TemplateID == "" {
		return fmt.Errorf("msg91: template-id is not configured")
	}

	recipient := map[string]string{
		// MSG91 wants the country code with no "+": 919876543210.
		"mobiles": strings.TrimPrefix(phone, "+"),
		c.otpVar:  otp,
	}
	// Optional trailing variable carrying the Android app-signature hash, which
	// Google's SMS Retriever requires the message to END with before it will
	// auto-read the code. Only sent when the template actually has the
	// placeholder — an unexpected variable is silently dropped by MSG91, but a
	// missing one leaves "##HASH##" visible in the delivered message.
	if c.cfg.AppSignature != "" && c.cfg.AppSignatureVar != "" {
		recipient[c.cfg.AppSignatureVar] = c.cfg.AppSignature
	}

	body := flowRequest{
		TemplateID: c.cfg.TemplateID,
		Sender:     c.cfg.SenderID,
		// "0" = don't rewrite links. There are none in an OTP template, and a
		// shortened link would push the message past the 140-byte ceiling the
		// SMS Retriever format needs.
		ShortURL:   "0",
		Recipients: []map[string]string{recipient},
	}

	var resp flowResponse
	if err := c.do(ctx, "/flow/", body, &resp); err != nil {
		slog.Error("sms otp send failed",
			"destination", MaskPhone(phone), "error", err)
		return err
	}
	if !strings.EqualFold(resp.Type, "success") {
		// Application-level rejection behind a 200. Surface it as a send error
		// so the caller treats it like any other provider failure.
		err := &SendError{StatusCode: http.StatusOK, Body: resp.Message}
		slog.Error("sms otp rejected by provider",
			"destination", MaskPhone(phone), "error", err)
		return err
	}

	// request_id, not the code: never log the OTP on a real send.
	slog.Info("sms otp dispatched",
		"destination", MaskPhone(phone), "request_id", resp.Message)
	return nil
}

func (c *MSG91Client) do(ctx context.Context, path string, body, out interface{}) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("authkey", c.cfg.AuthKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("msg91 POST %s: %w", path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("msg91 POST %s: read body: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &SendError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}
	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("msg91 POST %s: decode response: %w", path, err)
		}
	}
	return nil
}
