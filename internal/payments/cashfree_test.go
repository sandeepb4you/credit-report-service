package payments

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"credit-report-service/internal/config"
)

func TestVerifyWebhookSignature(t *testing.T) {
	secret := "test-secret"
	c := NewCashfreeClient(config.CashfreeConfig{Mode: "sandbox", ClientSecret: secret})

	body := []byte(`{"data":{"order":{"order_id":"abc"}},"type":"PAYMENT_SUCCESS_WEBHOOK"}`)
	ts := "1719000000000"

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write(body)
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if !c.VerifyWebhookSignature(ts, body, sig) {
		t.Fatal("expected valid signature to verify")
	}
	if c.VerifyWebhookSignature(ts, body, "bogus") {
		t.Fatal("expected bogus signature to fail")
	}
	if c.VerifyWebhookSignature("999", body, sig) {
		t.Fatal("expected wrong timestamp to fail")
	}
	if c.VerifyWebhookSignature(ts, append(body, ' '), sig) {
		t.Fatal("expected modified body to fail")
	}
}

func TestBaseURLFromMode(t *testing.T) {
	if got := NewCashfreeClient(config.CashfreeConfig{Mode: "sandbox"}).baseURL; got != cashfreeSandboxURL {
		t.Fatalf("sandbox baseURL = %q", got)
	}
	if got := NewCashfreeClient(config.CashfreeConfig{Mode: "production"}).baseURL; got != cashfreeProductionURL {
		t.Fatalf("production baseURL = %q", got)
	}
	if got := NewCashfreeClient(config.CashfreeConfig{Mode: "production", BaseURL: "http://x"}).baseURL; got != "http://x" {
		t.Fatalf("override baseURL = %q", got)
	}
}
