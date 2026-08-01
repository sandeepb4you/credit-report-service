package middleware

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"log/slog"
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

		attrs := []any{
			"method", c.Method(),
			"path", path,
			"status", c.Response().StatusCode(),
			"latency_ms", latencyMs,
		}
		// account_id is only present for routes behind RequireAuth/RequireRole;
		// reading it from Locals avoids re-parsing the JWT here.
		if aid, ok := AccountID(c); ok {
			attrs = append(attrs, "account_id", aid)
		}
		// Capture and mask the request body. Only meaningful for methods that
		// carry one; Fiber buffers it so it's safe to read post-Next().
		if reqBody := maskedBody(c.Body()); len(reqBody) > 0 {
			attrs = append(attrs, "req_body", reqBody)
		}
		// Capture and mask the response body written by the handler.
		if respBody := maskedBody(c.Response().Body()); len(respBody) > 0 {
			attrs = append(attrs, "resp_body", respBody)
		}

		msg := "http request"
		switch code := c.Response().StatusCode(); {
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

// maskedBody masks PII in a body and truncates it for logging. Returns the
// redacted, truncated bytes; returns nil (so the caller can omit the field) when
// the input is empty or whitespace-only.
func maskedBody(body []byte) []byte {
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	masked := maskJSON(body)
	return truncateBytes(masked, maxBodyLogBytes)
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
