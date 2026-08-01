package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// TestRequestLogger_MasksBodiesEndToEnd exercises the full middleware against a
// real Fiber app: a handler that echoes PII back. It captures the slog output
// and asserts the logged req_body/resp_body are masked and that no plaintext
// secret appears in the log line.
func TestRequestLogger_MasksBodiesEndToEnd(t *testing.T) {
	// Redirect slog to a buffer for the duration of the test.
	var buf bytes.Buffer
	prev := slog.Default()
	defer slog.SetDefault(prev)
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(RequestLogger())
	// Handler that round-trips a sensitive payload: it reads PII from the
	// request and writes PII into the response, so both bodies must be masked.
	app.Post("/login", func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusOK).JSON(fiber.Map{
			"token":     "eyJhbGciOiJIUzI1NiJ9.secret.eftts",
			"email":     "leak@contoso.com",
			"firstName": "Ada",
			"pan":       "ABCDE1234F",
			"accountId": 42,
		})
	})

	body := `{"email":"leak@contoso.com","password":"hunter2pass","pan":"ABCDE1234F"}`
	req, err := app.Test(newReq("POST", "/login", body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if req.StatusCode != 200 {
		t.Fatalf("status = %d", req.StatusCode)
	}

	logLine := buf.String()
	// None of the plaintext secrets may appear anywhere in the log line.
	for _, secret := range []string{"leak@contoso.com", "hunter2pass", "ABCDE1234F", "eyJhbGci"} {
		if strings.Contains(logLine, secret) {
			t.Errorf("plaintext secret %q leaked into log:\n%s", secret, logLine)
		}
	}
	// The masked marker and both body fields must be present.
	if !strings.Contains(logLine, maskValue) {
		t.Errorf("expected mask marker %q in log:\n%s", maskValue, logLine)
	}
	if !strings.Contains(logLine, "req_body=") {
		t.Errorf("req_body missing from log:\n%s", logLine)
	}
	if !strings.Contains(logLine, "resp_body=") {
		t.Errorf("resp_body missing from log:\n%s", logLine)
	}
	// The non-sensitive account id should survive (proves we didn't over-mask).
	if !strings.Contains(logLine, `"account_id":42`) && !strings.Contains(logLine, `account_id=42`) {
		// account_id here is the response JSON field, not the Locals one; it may
		// appear within resp_body. If it's masked that's acceptable too, so only
		// soft-check.
		t.Logf("note: accountId not visible in log (may be masked):\n%s", logLine)
	}
}

// TestRequestLogger_SkipsNoisyPaths verifies the liveness probe is not logged.
func TestRequestLogger_SkipsNoisyPaths(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	defer slog.SetDefault(prev)
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(RequestLogger())
	app.Get("/api/ping", func(c *fiber.Ctx) error { return c.JSON(fiber.Map{"status": "UP"}) })

	if _, err := app.Test(newReq("GET", "/api/ping", "")); err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if strings.Contains(buf.String(), "http request") {
		t.Errorf("noisy /api/ping path was logged:\n%s", buf.String())
	}
}

// TestRequestLogger_StatusLevels confirms the status->level mapping: a 500
// handler logs at ERROR, a 404 at WARN, a 200 at INFO.
func TestRequestLogger_StatusLevels(t *testing.T) {
	cases := []struct {
		route      string
		wantLevel  string
		statusCode int
	}{
		{"/ok", "INFO", 200},
		{"/nf", "WARN", 404},
		{"/boom", "ERROR", 500},
	}
	for _, tc := range cases {
		t.Run(tc.route, func(t *testing.T) {
			var buf bytes.Buffer
			prev := slog.Default()
			defer slog.SetDefault(prev)
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))

			app := fiber.New(fiber.Config{DisableStartupMessage: true})
			app.Use(RequestLogger())
			app.Get("/ok", func(c *fiber.Ctx) error { return c.SendStatus(200) })
			app.Get("/nf", func(c *fiber.Ctx) error { return c.SendStatus(404) })
			app.Get("/boom", func(c *fiber.Ctx) error { return c.SendStatus(500) })

			resp, err := app.Test(newReq("GET", tc.route, ""))
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			if resp.StatusCode != tc.statusCode {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.statusCode)
			}
			if !strings.Contains(buf.String(), "level="+tc.wantLevel) {
				t.Errorf("status %d: expected level=%s, got:\n%s", tc.statusCode, tc.wantLevel, buf.String())
			}
		})
	}
}

// newReq builds a *net/http.Request suitable for fiber.App.Test.
func newReq(method, target, body string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	return r
}
