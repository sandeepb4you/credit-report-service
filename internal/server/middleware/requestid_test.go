package middleware

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
)

// An id supplied by nginx or the app is kept, so a trace that started upstream
// survives into these logs.
func TestRequestID_HonoursASaneInboundID(t *testing.T) {
	app, seen := requestIDApp()

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(RequestIDHeader, "edge-7f3a91c2-0042")
	res, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	if *seen != "edge-7f3a91c2-0042" {
		t.Errorf("inbound id not used: %q", *seen)
	}
	if got := res.Header.Get(RequestIDHeader); got != "edge-7f3a91c2-0042" {
		t.Errorf("id not echoed on the response: %q", got)
	}
}

// The header is attacker-controlled and lands in a log line. A caller who can
// put a newline or a quote in there can forge log entries, so anything that is
// not plainly an id is replaced rather than sanitised.
func TestRequestID_ReplacesAnythingUnconvincing(t *testing.T) {
	cases := map[string]string{
		"log injection":  "abcdefgh\ntime=2026-01-01 level=INFO msg=\"nothing happened\"",
		"quotes":         "abc\"def\"ghi",
		"too short":      "abc",
		"too long":       strings.Repeat("a", 65),
		"empty":          "",
		"spaces":         "abc def ghi",
		"control chars":  "abcdefgh\x00ij",
		"path traversal": "../../etc/passwd",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			app, seen := requestIDApp()
			req := httptest.NewRequest("GET", "/", nil)
			if in != "" {
				req.Header.Set(RequestIDHeader, in)
			}
			if _, err := app.Test(req, -1); err != nil {
				t.Fatalf("request: %v", err)
			}
			if *seen == in {
				t.Errorf("kept an unacceptable id: %q", *seen)
			}
			if len(*seen) < 8 {
				t.Errorf("no replacement id generated: %q", *seen)
			}
		})
	}
}

// Every request gets one, and two requests do not share it.
func TestRequestID_IsGeneratedAndUnique(t *testing.T) {
	app, seen := requestIDApp()

	if _, err := app.Test(httptest.NewRequest("GET", "/", nil), -1); err != nil {
		t.Fatalf("request: %v", err)
	}
	first := *seen
	if _, err := app.Test(httptest.NewRequest("GET", "/", nil), -1); err != nil {
		t.Fatalf("request: %v", err)
	}

	if first == "" || *seen == "" {
		t.Fatal("no id generated")
	}
	if first == *seen {
		t.Errorf("two requests shared an id: %q", first)
	}
}

// The point of returning it: a user quoting the id from a failure gives support
// the one key that finds the log line and the Sentry event.
func TestRequestID_ReachesTheErrorEnvelope(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: apperr.ErrorHandler})
	app.Use(RequestID())
	app.Get("/boom", func(c *fiber.Ctx) error {
		return apperr.NewNotFound("Report not found")
	})

	req := httptest.NewRequest("GET", "/boom", nil)
	req.Header.Set(RequestIDHeader, "trace-0123456789")
	res, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	body, _ := io.ReadAll(res.Body)

	if !strings.Contains(string(body), `"requestId":"trace-0123456789"`) {
		t.Errorf("envelope carries no request id:\n%s", body)
	}
}

// requestIDApp returns an app with the middleware mounted and a pointer to the
// id the handler saw.
func requestIDApp() (*fiber.App, *string) {
	var seen string
	app := fiber.New()
	app.Use(RequestID())
	app.Get("/", func(c *fiber.Ctx) error {
		seen = RequestIDOf(c)
		return c.SendStatus(fiber.StatusOK)
	})
	return app, &seen
}
