package middleware

import (
	"bytes"
	"log/slog"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
)

// FILE DATA is exempt from logging; fields are not. A multipart upload logs as
// its form fields (masked like any other body) with the file part reduced to
// its size — the PAN-card upload used to put 55KB of raw JPEG bytes into the
// log line, and a file cannot be redacted, only omitted. Binary RESPONSES (the
// document relay returns image/PDF bytes inline) are all file, so they reduce
// to a size marker outright.
func TestRequestLogger_FileDataIsOmittedButFieldsStillLog(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	defer slog.SetDefault(prev)
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	jpegBytes := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte{0xAB}, 512)...)

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(RequestLogger())
	// Echo the shape of the real endpoints: a multipart upload in, image bytes out.
	app.Post("/api/kyc/pan/document", func(c *fiber.Ctx) error {
		c.Set(fiber.HeaderContentType, "image/jpeg")
		return c.Send(jpegBytes)
	})

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("pan", "ABCDE1234F"); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteField("dob", "2000-01-01"); err != nil {
		t.Fatal(err)
	}
	fw, err := w.CreateFormFile("file", "test-pan-card.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(jpegBytes); err != nil {
		t.Fatal(err)
	}
	w.Close()

	req := httptest.NewRequest("POST", "/api/kyc/pan/document", &body)
	req.Header.Set(fiber.HeaderContentType, w.FormDataContentType())
	if _, err := app.Test(req); err != nil {
		t.Fatalf("request failed: %v", err)
	}

	logLine := buf.String()
	// Sensitive field VALUES are masked, never skipped, and the file bytes and
	// filename (itself PII — users name files after themselves) are absent.
	if strings.Contains(logLine, "ABCDE1234F") {
		t.Errorf("multipart pan field leaked into log:\n%s", logLine)
	}
	if strings.Contains(logLine, "2000-01-01") {
		t.Errorf("multipart dob field leaked into log:\n%s", logLine)
	}
	if strings.Contains(logLine, "test-pan-card.jpg") {
		t.Errorf("upload filename leaked into log:\n%s", logLine)
	}
	if strings.Contains(logLine, "Content-Disposition") || strings.Contains(logLine, `\xff\xd8`) {
		t.Errorf("raw multipart/file bytes leaked into log:\n%s", logLine)
	}
	// The FIELDS still log — names visible, values masked — and the file part
	// is reduced to its size, so the line still says what moved.
	if !strings.Contains(logLine, "pan="+maskValue) || !strings.Contains(logLine, "dob="+maskValue) {
		t.Errorf("expected masked pan/dob fields in req_body:\n%s", logLine)
	}
	if !strings.Contains(logLine, "file=[file, 516 bytes]") {
		t.Errorf("expected the file part reduced to its size in req_body:\n%s", logLine)
	}
	if !strings.Contains(logLine, "[image/jpeg") {
		t.Errorf("expected an image summary placeholder in resp_body:\n%s", logLine)
	}
}

// JSON stays logged (masked) — the placeholder rule must not eat the bodies the
// log exists for.
func TestRequestLogger_JsonBodiesStillLogMasked(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	defer slog.SetDefault(prev)
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(RequestLogger())
	app.Post("/api/thing", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "PENDING"})
	})

	if _, err := app.Test(newReq("POST", "/api/thing", `{"pan":"ABCDE1234F","note":"hello"}`)); err != nil {
		t.Fatalf("request failed: %v", err)
	}

	logLine := buf.String()
	if !strings.Contains(logLine, "req_body=") || !strings.Contains(logLine, "hello") {
		t.Errorf("JSON request body should still be logged (masked):\n%s", logLine)
	}
	if strings.Contains(logLine, "ABCDE1234F") {
		t.Errorf("pan leaked from JSON body:\n%s", logLine)
	}
	if !strings.Contains(logLine, "PENDING") {
		t.Errorf("JSON response body should still be logged:\n%s", logLine)
	}
}

// An error returned as an apperr must log at the level of the status the CLIENT
// will see, not the stale pre-error-handler 200. Fiber runs the error handler
// after this middleware unwinds, so the response code says 200 while the log
// line's own status attribute already says 404 — and the level used to follow
// the former, filing every handled error under Info.
func TestRequestLogger_HandledErrorsLogAtTheirRealLevel(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	defer slog.SetDefault(prev)
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
		ErrorHandler:          apperr.ErrorHandler,
	})
	app.Use(RequestLogger())
	app.Get("/api/thing", func(c *fiber.Ctx) error {
		return apperr.NewNotFound("no report yet")
	})

	if _, err := app.Test(httptest.NewRequest("GET", "/api/thing", nil)); err != nil {
		t.Fatalf("request failed: %v", err)
	}

	logLine := buf.String()
	if !strings.Contains(logLine, "status=404") {
		t.Errorf("expected status=404 in log:\n%s", logLine)
	}
	if !strings.Contains(logLine, "level=WARN") {
		t.Errorf("a 404 must log at WARN, got:\n%s", logLine)
	}
}
