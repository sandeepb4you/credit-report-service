package middleware

import (
	"strings"
	"testing"

	"github.com/getsentry/sentry-go"
)

// A Sentry event leaves the country, so the scrubber is the last thing standing
// between a customer's PAN and a US-hosted issue tracker. These pin what must
// never survive it.
func TestScrubEvent_DropsRequestHeadersAndCookies(t *testing.T) {
	event := &sentry.Event{
		Request: &sentry.Request{
			URL: "https://api.myscorr.com/api/kyc/pan",
			Headers: map[string]string{
				"Authorization": "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIyNCJ9.abcdefghij",
				"X-Device-Info": `{"model":"iPhone15,3"}`,
			},
			Cookies: "refresh_token=rt_9f8e7d6c5b4a",
			Data:    `{"pan":"ABCDE1234F"}`,
		},
	}

	out := scrubEvent(event)

	if out.Request.Headers != nil {
		t.Errorf("headers survived: %v", out.Request.Headers)
	}
	if out.Request.Cookies != "" {
		t.Errorf("cookies survived: %q", out.Request.Cookies)
	}
	if out.Request.Data != "" {
		t.Errorf("request body survived: %q", out.Request.Data)
	}
}

// An error string is the most common way PII escapes: somebody wraps a bureau
// response or an address into a message and it becomes an exception value.
func TestScrubEvent_MasksPIIInMessagesAndExceptions(t *testing.T) {
	event := &sentry.Event{
		Message: "prefill failed for user@example.com",
		Exception: []sentry.Exception{
			{Value: "pan ABCDE1234F not matched for 9876543210"},
		},
		Breadcrumbs: []*sentry.Breadcrumb{
			{Message: "sent otp to 9876543210", Data: map[string]any{"otp": "1234"}},
		},
		Tags: map[string]string{"destination": "user@example.com"},
	}

	out := scrubEvent(event)

	for _, leaked := range []string{"user@example.com", "ABCDE1234F", "9876543210"} {
		if strings.Contains(out.Message, leaked) ||
			strings.Contains(out.Exception[0].Value, leaked) ||
			strings.Contains(out.Breadcrumbs[0].Message, leaked) ||
			strings.Contains(out.Tags["destination"], leaked) {
			t.Errorf("%q survived scrubbing: %+v", leaked, out)
		}
	}
	// Breadcrumb data is arbitrary key/value from wherever it was recorded, so
	// it goes entirely rather than being walked.
	if out.Breadcrumbs[0].Data != nil {
		t.Errorf("breadcrumb data survived: %v", out.Breadcrumbs[0].Data)
	}
}

// Scrubbing must not make the event useless: the route, the failure and the
// non-sensitive tags are why anyone opens it.
func TestScrubEvent_KeepsWhatMakesTheEventUseful(t *testing.T) {
	event := &sentry.Event{
		Message:   "digitap prefill returned 503",
		Exception: []sentry.Exception{{Type: "*errors.errorString", Value: "upstream unavailable"}},
		Tags: map[string]string{
			"request_id": "edge-7f3a91c2-0042",
			"route":      "/api/kyc/pan",
		},
		Request: &sentry.Request{URL: "https://api.myscorr.com/api/kyc/pan"},
	}

	out := scrubEvent(event)

	if !strings.Contains(out.Message, "digitap prefill returned 503") {
		t.Errorf("message mangled: %q", out.Message)
	}
	if out.Exception[0].Value != "upstream unavailable" {
		t.Errorf("exception value mangled: %q", out.Exception[0].Value)
	}
	if out.Tags["request_id"] != "edge-7f3a91c2-0042" {
		t.Errorf("request id lost: %q", out.Tags["request_id"])
	}
	if out.Tags["route"] != "/api/kyc/pan" {
		t.Errorf("route lost: %q", out.Tags["route"])
	}
	if !strings.Contains(out.Request.URL, "/api/kyc/pan") {
		t.Errorf("url lost: %q", out.Request.URL)
	}
}

// No DSN is the default everywhere except a deployment that opted in. It must
// be a no-op, not an error, or every developer's first run fails at boot.
func TestInitSentry_WithoutADSNIsANoOp(t *testing.T) {
	flush, err := InitSentry(SentryConfig{})
	if err != nil {
		t.Fatalf("empty DSN should not error: %v", err)
	}
	if flush == nil {
		t.Fatal("flush must be callable even when disabled")
	}
	flush()
}
