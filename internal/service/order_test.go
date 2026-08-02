package service

import (
	"encoding/json"
	"testing"
)

func TestBuildReturnURL(t *testing.T) {
	cases := []struct{ base, uid, want string }{
		{"", "u1", ""},
		{"https://app.example.com/pay/result?order_id={order_id}", "u1",
			"https://app.example.com/pay/result?order_id=u1"},
		{"https://app.example.com/pay/result", "u1",
			"https://app.example.com/pay/result?order_id=u1"},
		{"https://app.example.com/pay/result?src=web", "u1",
			"https://app.example.com/pay/result?src=web&order_id=u1"},
	}
	for _, c := range cases {
		if got := buildReturnURL(c.base, c.uid); got != c.want {
			t.Errorf("buildReturnURL(%q, %q) = %q, want %q", c.base, c.uid, got, c.want)
		}
	}
}

func TestFlexStringToleratesStringAndNumber(t *testing.T) {
	var env webhookEnvelope
	asNumber := []byte(`{"type":"PAYMENT_SUCCESS_WEBHOOK","data":{"payment":{"cf_payment_id":5114910}}}`)
	if err := json.Unmarshal(asNumber, &env); err != nil {
		t.Fatalf("number cf_payment_id: %v", err)
	}
	if env.Data.Payment.CFPaymentID != "5114910" {
		t.Fatalf("got %q", env.Data.Payment.CFPaymentID)
	}

	asString := []byte(`{"type":"PAYMENT_SUCCESS_WEBHOOK","data":{"payment":{"cf_payment_id":"CFPAY_123"}}}`)
	if err := json.Unmarshal(asString, &env); err != nil {
		t.Fatalf("string cf_payment_id: %v", err)
	}
	if env.Data.Payment.CFPaymentID != "CFPAY_123" {
		t.Fatalf("got %q", env.Data.Payment.CFPaymentID)
	}
}
