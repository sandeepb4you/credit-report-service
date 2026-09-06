package middleware

import (
	"bytes"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"strconv"
	"strings"
	"time"

	"credit-report-service/internal/apperr"
	"github.com/gofiber/fiber/v2"
)

// noisyPaths are skipped by the request logger to avoid log spam. These are
// non-business traffic: the liveness probe and the Swagger UI assets.
var noisyPaths = map[string]bool{
	"/api/ping": true,
}

// noisyPathPrefixes are path prefixes skipped for the same reason as noisyPaths
// (the Swagger UI serves many static assets per page load).
var noisyPathPrefixes = []string{
	"/swagger",
}

// maxBodyLogBytes caps how much of a request/response body is logged. The
// credit report response is ~15KB; logging it whole would bloat every line, so
// bodies are truncated after PII masking. The full body still goes to the
// client/DB — this only limits the log line.
const maxBodyLogBytes = 1024

// RequestLogger emits one structured log line per HTTP request after the
// handler runs. It records:
//   - method, path (route pattern when available), status, latency_ms
//   - account_id when the request was authenticated (from c.Locals)
//   - request body and response body, both PII-masked and truncated
//
// PII policy: request/response bodies run through maskJSON, which replaces
// sensitive fields (email, phone, PAN, password, otp, token, names, DOB,
// aadhaar, etc.) with "********" and also scrubs recognizable PAN / email /
// JWT shapes as defense-in-depth. Bodies are then truncated to keep log lines
// bounded. The raw Authorization header is never logged.
//
// Status -> level: 2xx/3xx Info, 4xx Warn, 5xx Error.
func RequestLogger() fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Skip noisy endpoints before timing them.
		if isNoisy(c.Path()) {
			return c.Next()
		}

		start := time.Now()
		err := c.Next()
		latencyMs := time.Since(start).Milliseconds()

		// Prefer the registered route pattern so /reports/42 logs as
		// /api/credit-analytics/reports/:id; fall back to the raw path if no
		// route matched (e.g. 404 on an unknown path).
		path := c.Path()
		if c.Route() != nil && c.Route().Path != "" {
			path = c.Route().Path
		}

		// Fiber runs the error handler after this middleware unwinds, so on a
		// failed request the response still carries the default 200. Ask apperr
		// what the status WILL be instead of logging a success for a request that
		// is about to fail — the same mapping the error handler uses.
		status := c.Response().StatusCode()
		if err != nil {
			if mapped, _, _, _ := apperr.StatusFor(err); mapped != 0 {
				status = mapped
			}
		}

		attrs := []any{
			"method", c.Method(),
			"path", path,
			"status", status,
			"latency_ms", latencyMs,
		}
		// account_id is only present for routes behind RequireAuth/RequireRole;
		// reading it from Locals avoids re-parsing the JWT here.
		if aid, ok := AccountID(c); ok {
			attrs = append(attrs, "account_id", aid)
		}
		// Capture and mask the request body. Only meaningful for methods that
		// carry one; Fiber buffers it so it's safe to read post-Next().
		if reqBody := loggableBody(c.Get(fiber.HeaderContentType), c.Body()); len(reqBody) > 0 {
			attrs = append(attrs, "req_body", reqBody)
		}
		// Capture and mask the response body written by the handler.
		if respBody := loggableBody(
			string(c.Response().Header.ContentType()), c.Response().Body()); len(respBody) > 0 {
			attrs = append(attrs, "resp_body", respBody)
		}

		msg := "http request"
		// Level from the SAME resolved status logged above — the raw response code
		// is still the pre-error-handler 200 whenever the handler returned an
		// apperr, which had every handled error logging at Info.
		switch code := status; {
		case code >= 500:
			slog.Error(msg, attrs...)
		case code >= 400:
			slog.Warn(msg, attrs...)
		default:
			slog.Info(msg, attrs...)
		}
		return err
	}
}

// loggableBody masks and truncates a body for the log, with one exemption:
// FILE DATA. A file's bytes cannot be redacted, only omitted — logging a JPEG
// "masked" is still logging the JPEG — so a multipart body is re-rendered as
// its form fields with each file part reduced to its size, and a body that IS
// a file (the relayed card image / report PDF on the download endpoints) is
// reduced to "[type, N bytes]". Everything that is a field stays logged and
// goes through the same masking as any other body: sensitive names by key,
// PAN/email/phone shapes as backstop.
func loggableBody(contentType string, body []byte) []byte {
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	switch {
	case strings.HasPrefix(ct, "multipart/"):
		return truncateBytes(summarizeMultipart(contentType, body), maxBodyLogBytes)
	case ct == "" || ct == "application/json" || strings.HasPrefix(ct, "text/") ||
		strings.HasSuffix(ct, "+json"):
		// Empty stays loggable: every API body carries a content type, so blank
		// means a bare client we still want visibility on — and maskJSON's
		// shape-based scrubbing still applies.
		return truncateBytes(maskJSON(body), maxBodyLogBytes)
	default:
		// Not a container of fields at all — image/*, application/pdf,
		// octet-stream. The whole body is the file.
		return []byte("[" + ct + ", " + strconv.Itoa(len(body)) + " bytes]")
	}
}

// summarizeMultipart renders a multipart body as "name=value" fields, replacing
// each file part's content with its size. Field values are masked by the same
// rules as JSON fields (sensitive key names, then value shapes), so nothing is
// skipped that could have been logged — only the bytes that could not.
// A body that cannot be parsed falls back to a whole-body size marker rather
// than to the raw bytes: the parse failing is not a reason to leak the file.
func summarizeMultipart(contentType string, body []byte) []byte {
	fallback := []byte("[multipart, " + strconv.Itoa(len(body)) + " bytes]")
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil || params["boundary"] == "" {
		return fallback
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	var out strings.Builder
	out.WriteString("multipart{")
	for first := true; ; first = false {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fallback
		}
		if !first {
			out.WriteString(", ")
		}
		if part.FileName() != "" {
			// The file: size only. Its NAME is omitted too, deliberately — the
			// masking policy already treats stored filenames as PII
			// (documentFileName is a sensitive key), and users name files after
			// themselves.
			n, _ := io.Copy(io.Discard, part)
			out.WriteString(part.FormName() + "=[file, " + strconv.FormatInt(n, 10) + " bytes]")
			continue
		}
		value, _ := io.ReadAll(io.LimitReader(part, 256))
		if isSensitiveKey(part.FormName()) {
			out.WriteString(part.FormName() + "=" + maskValue)
		} else {
			// maskShapes catches a sensitive VALUE under an innocent name, the
			// same backstop maskRaw applies to unparseable bodies.
			out.WriteString(part.FormName() + "=" + maskShapes(string(value)))
		}
	}
	out.WriteString("}")
	return []byte(out.String())
}

// isNoisy reports whether a path should be skipped by the request logger.
func isNoisy(path string) bool {
	if noisyPaths[path] {
		return true
	}
	for _, p := range noisyPathPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}
