package payments

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyWebhookSignature(t *testing.T) {
	c := NewRazorpayClient("sandbox", RazorpayCredentials{
		KeyID: "rzp_test_x", KeySecret: "key-secret", WebhookSecret: "hook-secret",
	}, "", 0)
	body := []byte(`{"event":"order.paid","payload":{}}`)

	if !c.VerifyWebhookSignature(body, sign("hook-secret", body)) {
		t.Fatal("a delivery signed with the webhook secret must verify")
	}
	if c.VerifyWebhookSignature(body, sign("key-secret", body)) {
		t.Fatal("the KEY secret is not what Razorpay signs webhooks with")
	}
	if c.VerifyWebhookSignature(append(body, ' '), sign("hook-secret", body)) {
		t.Fatal("a modified body must fail")
	}
	if c.VerifyWebhookSignature(body, "") {
		t.Fatal("a missing signature must fail")
	}
}

// With no webhook secret configured, an HMAC under the empty key is something
// anyone can compute; it must not verify.
func TestVerifyWebhookSignatureWithoutSecretRejectsEverything(t *testing.T) {
	c := NewRazorpayClient("sandbox", RazorpayCredentials{KeyID: "rzp_test_x", KeySecret: "s"}, "", 0)
	body := []byte(`{"event":"order.paid"}`)
	if c.VerifyWebhookSignature(body, sign("", body)) {
		t.Fatal("an empty-key signature verified")
	}
}

func TestToMinorUnitsRounds(t *testing.T) {
	for in, want := range map[float64]int64{299: 29900, 299.99: 29999, 0.1 + 0.2: 30, 999: 99900} {
		if got := ToMinorUnits(in); got != want {
			t.Errorf("ToMinorUnits(%v) = %d, want %d", in, got, want)
		}
	}
}

// The wire contract with Razorpay: paise, our order uid as the receipt, basic
// auth with the key pair, and the order's status mapped onto ours.
func TestCreateAndGetOrder(t *testing.T) {
	var created map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "rzp_test_x" || pass != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/orders":
			_ = json.NewDecoder(r.Body).Decode(&created)
			_, _ = w.Write([]byte(`{"id":"order_ABC","receipt":"uid-1","status":"created","amount":29900}`))
		case r.Method == http.MethodGet && r.URL.Path == "/orders/order_ABC":
			_, _ = w.Write([]byte(`{"id":"order_ABC","receipt":"uid-1","status":"paid"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewRazorpayClient("sandbox", RazorpayCredentials{KeyID: "rzp_test_x", KeySecret: "secret"}, srv.URL, 0)
	res, err := c.CreateOrder(context.Background(), CreateOrderParams{
		OrderID: "uid-1", Amount: 299, Currency: "INR", OrderNote: "myScorr Starter",
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if res.GatewayOrderID != "order_ABC" || res.Status != StatusActive {
		t.Errorf("create result = %+v", res)
	}
	if created["amount"] != float64(29900) || created["receipt"] != "uid-1" || created["currency"] != "INR" {
		t.Errorf("create body = %v", created)
	}

	got, err := c.GetOrder(context.Background(), "order_ABC")
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if got.Status != StatusPaid {
		t.Errorf("paid order status = %q, want PAID", got.Status)
	}
}

// Orders from the previous gateway carry its ids in the same column; they must
// not be sent to Razorpay (or into a URL) at all.
func TestGetOrderRefusesForeignIDs(t *testing.T) {
	c := NewRazorpayClient("sandbox", RazorpayCredentials{KeyID: "rzp_test_x", KeySecret: "s"}, "http://127.0.0.1:1", 0)
	for _, id := range []string{"5114915423", "", "order_x/../payments"} {
		if _, err := c.GetOrder(context.Background(), id); err == nil {
			t.Errorf("GetOrder(%q) did not refuse", id)
		}
	}
}

func TestGetPaymentPicksTheCapturedAttempt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/orders/order_ABC/payments":
			_, _ = w.Write([]byte(`{"items":[
				{"id":"pay_1","status":"failed","method":"card"},
				{"id":"pay_2","status":"captured","method":"card","card_id":"card_9"}]}`))
		case "/payments/pay_2":
			if r.URL.Query().Get("expand[]") != "card" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"id":"pay_2","status":"captured","method":"card",
				"card":{"network":"Visa","last4":"4417","type":"credit"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewRazorpayClient("sandbox", RazorpayCredentials{KeyID: "rzp_test_x", KeySecret: "s"}, srv.URL, 0)
	d, err := c.GetPayment(context.Background(), "order_ABC")
	if err != nil {
		t.Fatalf("GetPayment: %v", err)
	}
	if d.PaymentID != "pay_2" || d.Label() != "Card · Visa ending 4417" {
		t.Errorf("details = %+v, label %q", d, d.Label())
	}
}

func TestParseWebhookOrderPaid(t *testing.T) {
	body := []byte(`{"entity":"event","event":"order.paid","created_at":1719000000,
	  "payload":{
	    "payment":{"entity":{"id":"pay_X","order_id":"order_ABC","status":"captured","method":"upi",
	      "email":"asha@example.com","contact":"+919000000601","vpa":"asha@okhdfc",
	      "acquirer_data":{"rrn":"412345678901"}}},
	    "order":{"entity":{"id":"order_ABC","receipt":"uid-1","status":"paid"}}}}`)
	ev, err := ParseWebhook(body)
	if err != nil {
		t.Fatalf("ParseWebhook: %v", err)
	}
	if ev.Kind != WebhookPaid || ev.OrderUID != "uid-1" || ev.GatewayOrderID != "order_ABC" ||
		ev.PaymentID != "pay_X" || ev.PaymentGroup != "upi" || ev.PaidAt.Unix() != 1719000000 {
		t.Errorf("event = %+v", ev)
	}
	label, ref := ev.Payment.Reference()
	if label != "UTR" || ref != "412345678901" {
		t.Errorf("reference = %s %s, want the RRN as the UTR", label, ref)
	}
	if strings.Contains(strings.ToLower(ev.Payment.Label()), "okhdfc") {
		t.Errorf("label leaks the UPI handle: %q", ev.Payment.Label())
	}
}

func TestParseWebhookPaymentFailedLeavesTheOrderPayable(t *testing.T) {
	ev, err := ParseWebhook([]byte(`{"event":"payment.failed","payload":{"payment":{"entity":{
		"id":"pay_Y","order_id":"order_ABC","status":"failed","method":"card",
		"error_description":"Payment was declined by the bank"}}}}`))
	if err != nil {
		t.Fatalf("ParseWebhook: %v", err)
	}
	if ev.Kind != WebhookPaymentFailed || ev.GatewayOrderID != "order_ABC" || ev.OrderUID != "" ||
		ev.FailureReason != "Payment was declined by the bank" {
		t.Errorf("event = %+v", ev)
	}
}

func TestParseWebhookRejectsGarbage(t *testing.T) {
	for _, b := range []string{``, `{}`, `not json`} {
		if _, err := ParseWebhook([]byte(b)); err == nil {
			t.Errorf("ParseWebhook(%q) accepted", b)
		}
	}
}
