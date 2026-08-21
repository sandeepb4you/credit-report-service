package sms

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"credit-report-service/internal/config"
)

func testConfig(baseURL string) config.MSG91Config {
	return config.MSG91Config{
		AuthKey:    "test-auth-key",
		TemplateID: "6a845b1f9fa9adf1da0c06d3",
		SenderID:   "REAOUT",
		OTPVar:     "OTP",
		BaseURL:    baseURL,
		Timeout:    2 * time.Second,
	}
}

// The request MSG91 actually receives is the whole contract: a wrong variable
// name or a "+" left on the number produces a delivered-but-broken SMS, which
// no status code would reveal.
func TestSendOTP_RequestShape(t *testing.T) {
	var gotPath, gotAuthKey, gotContentType string
	var body flowRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthKey = r.Header.Get("authkey")
		gotContentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body is not valid JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"success","message":"5762846b4f8d285d378b4567"}`))
	}))
	defer srv.Close()

	c := NewMSG91Client(testConfig(srv.URL))
	if err := c.SendOTP(context.Background(), "+919876543210", "123456"); err != nil {
		t.Fatalf("SendOTP: unexpected error %v", err)
	}

	if gotPath != "/flow/" {
		t.Errorf("path = %q, want /flow/", gotPath)
	}
	if gotAuthKey != "test-auth-key" {
		t.Errorf("authkey header = %q, want test-auth-key", gotAuthKey)
	}
	if gotContentType != "application/json" {
		t.Errorf("content-type = %q, want application/json", gotContentType)
	}
	if body.TemplateID != "6a845b1f9fa9adf1da0c06d3" {
		t.Errorf("template_id = %q", body.TemplateID)
	}
	if body.Sender != "REAOUT" {
		t.Errorf("sender = %q, want REAOUT", body.Sender)
	}
	if len(body.Recipients) != 1 {
		t.Fatalf("recipients length = %d, want 1", len(body.Recipients))
	}
	// MSG91 wants the country code without the "+".
	if got := body.Recipients[0]["mobiles"]; got != "919876543210" {
		t.Errorf("mobiles = %q, want 919876543210", got)
	}
	if got := body.Recipients[0]["OTP"]; got != "123456" {
		t.Errorf("OTP var = %q, want 123456", got)
	}
	// Nothing else should ride along: an unexpected key means a template
	// variable we never configured.
	if len(body.Recipients[0]) != 2 {
		t.Errorf("recipient has %d keys, want 2: %v", len(body.Recipients[0]), body.Recipients[0])
	}
}

// The app-signature hash is only sent when both the value and its placeholder
// name are configured — half-configured must not put a stray variable on the
// wire, since MSG91 drops unknown ones and the user gets "##HASH##" in the SMS.
func TestSendOTP_AppSignature(t *testing.T) {
	cases := []struct {
		name      string
		signature string
		varName   string
		wantKeys  int
	}{
		{"both set", "FA+9qCX9VSu", "HASH", 3},
		{"value only", "FA+9qCX9VSu", "", 2},
		{"var only", "", "HASH", 2},
		{"neither", "", "", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body flowRequest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &body)
				_, _ = w.Write([]byte(`{"type":"success","message":"ok"}`))
			}))
			defer srv.Close()

			cfg := testConfig(srv.URL)
			cfg.AppSignature = tc.signature
			cfg.AppSignatureVar = tc.varName
			if err := NewMSG91Client(cfg).SendOTP(
				context.Background(), "+919876543210", "123456"); err != nil {
				t.Fatalf("SendOTP: %v", err)
			}
			if got := len(body.Recipients[0]); got != tc.wantKeys {
				t.Errorf("recipient keys = %d, want %d: %v",
					got, tc.wantKeys, body.Recipients[0])
			}
			if tc.wantKeys == 3 && body.Recipients[0]["HASH"] != tc.signature {
				t.Errorf("HASH = %q, want %q", body.Recipients[0]["HASH"], tc.signature)
			}
		})
	}
}

// MSG91 reports application-level failures as HTTP 200 with {"type":"error"}.
// Trusting the status code alone would report a silent success and leave the
// user waiting for an SMS that was never queued.
func TestSendOTP_ErrorBehind200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"type":"error","message":"template not found"}`))
	}))
	defer srv.Close()

	err := NewMSG91Client(testConfig(srv.URL)).SendOTP(
		context.Background(), "+919876543210", "123456")
	if err == nil {
		t.Fatal("SendOTP: want an error for type=error, got nil")
	}
	se, ok := err.(*SendError)
	if !ok {
		t.Fatalf("error type = %T, want *SendError", err)
	}
	if se.Body != "template not found" {
		t.Errorf("SendError.Body = %q, want the provider message", se.Body)
	}
}

func TestSendOTP_HTTPFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"authkey invalid"}`))
	}))
	defer srv.Close()

	err := NewMSG91Client(testConfig(srv.URL)).SendOTP(
		context.Background(), "+919876543210", "123456")
	se, ok := err.(*SendError)
	if !ok {
		t.Fatalf("error = %v (%T), want *SendError", err, err)
	}
	if se.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %d, want 401", se.StatusCode)
	}
}

// A missing template ID is a configuration error, not a network one: fail
// before making a request that cannot succeed.
func TestSendOTP_NoTemplateID(t *testing.T) {
	cfg := testConfig("http://127.0.0.1:1") // never dialled
	cfg.TemplateID = ""
	if err := NewMSG91Client(cfg).SendOTP(
		context.Background(), "+919876543210", "123456"); err == nil {
		t.Fatal("want an error when template-id is empty")
	}
}

func TestNewMSG91Client_Defaults(t *testing.T) {
	c := NewMSG91Client(config.MSG91Config{AuthKey: "k", TemplateID: "t"})
	if c.baseURL != DefaultBaseURL {
		t.Errorf("baseURL = %q, want %q", c.baseURL, DefaultBaseURL)
	}
	if c.otpVar != DefaultOTPVar {
		t.Errorf("otpVar = %q, want %q", c.otpVar, DefaultOTPVar)
	}
	if c.http.Timeout <= 0 {
		t.Error("http timeout must default to a positive value")
	}
	if c.IsStub() {
		t.Error("a configured MSG91 client must not report itself as a stub")
	}
	// A trailing slash on the configured base URL must not double up into
	// "//flow/", which MSG91 answers with a 404.
	withSlash := NewMSG91Client(config.MSG91Config{BaseURL: "https://example.test/api/v5/"})
	if withSlash.baseURL != "https://example.test/api/v5" {
		t.Errorf("baseURL = %q, want the trailing slash trimmed", withSlash.baseURL)
	}
}

func TestMaskPhone(t *testing.T) {
	cases := map[string]string{
		"+919876543210": "+91******3210",
		"9876543210":    "******3210",
		"321":           "****",
		"":              "****",
	}
	for in, want := range cases {
		if got := MaskPhone(in); got != want {
			t.Errorf("MaskPhone(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStubSender(t *testing.T) {
	s := NewStubSender()
	if !s.IsStub() {
		t.Error("StubSender.IsStub() must be true")
	}
	if err := s.SendOTP(context.Background(), "+919876543210", "123456"); err != nil {
		t.Errorf("StubSender.SendOTP must not fail: %v", err)
	}
}
