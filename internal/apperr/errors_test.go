package apperr

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// TestTypedErrors confirms each typed error carries its message via Error().
func TestTypedErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		msg  string
	}{
		{"NotFound", NewNotFound("missing"), "missing"},
		{"Validation", NewValidation("bad input"), "bad input"},
		{"OtpFailure", NewOtpFailure("bad otp"), "bad otp"},
		{"Conflict", NewConflict("dup"), "dup"},
		{"Unauthorized", NewUnauthorized("nope"), "nope"},
		{"Forbidden", NewForbidden("no admin"), "no admin"},
		{"PanFailure", NewPanFailure("bad pan"), "bad pan"},
		{"PayloadTooLarge", NewPayloadTooLarge("too big"), "too big"},
		{"ServiceUnavailable", NewServiceUnavailable("down"), "down"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err.Error() != tc.msg {
				t.Errorf("%s.Error() = %q, want %q", tc.name, tc.err.Error(), tc.msg)
			}
		})
	}
}

// TestValidationWithDetails confirms NewValidationWith carries the details map.
func TestValidationWithDetails(t *testing.T) {
	d := map[string]string{"email": "required", "password": "too short"}
	err := NewValidationWith("Validation failed", d)
	var v *Validation
	if !errors.As(err, &v) {
		t.Fatalf("expected *Validation, got %T", err)
	}
	if v.Msg != "Validation failed" {
		t.Errorf("Msg = %q", v.Msg)
	}
	if v.Details["email"] != "required" {
		t.Errorf("details not carried: %v", v.Details)
	}
}

// TestErrorsAs confirms each constructor returns an error that errors.As can
// identify as its concrete type — the contract the package doc promises.
func TestErrorsAs(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"NotFound", NewNotFound("x")},
		{"Validation", NewValidation("x")},
		{"OtpFailure", NewOtpFailure("x")},
		{"Conflict", NewConflict("x")},
		{"Unauthorized", NewUnauthorized("x")},
		{"Forbidden", NewForbidden("x")},
		{"PanFailure", NewPanFailure("x")},
		{"PayloadTooLarge", NewPayloadTooLarge("x")},
		{"ServiceUnavailable", NewServiceUnavailable("x")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// errors.As to a pointer of pointer must succeed for the matching type.
			var nf *NotFound
			var v *Validation
			var of *OtpFailure
			var cf *Conflict
			var ua *Unauthorized
			var fb *Forbidden
			var pf *PanFailure
			var ptl *PayloadTooLarge
			var su *ServiceUnavailable
			matched := false
			switch tc.name {
			case "NotFound":
				matched = errors.As(tc.err, &nf)
			case "Validation":
				matched = errors.As(tc.err, &v)
			case "OtpFailure":
				matched = errors.As(tc.err, &of)
			case "Conflict":
				matched = errors.As(tc.err, &cf)
			case "Unauthorized":
				matched = errors.As(tc.err, &ua)
			case "Forbidden":
				matched = errors.As(tc.err, &fb)
			case "PanFailure":
				matched = errors.As(tc.err, &pf)
			case "PayloadTooLarge":
				matched = errors.As(tc.err, &ptl)
			case "ServiceUnavailable":
				matched = errors.As(tc.err, &su)
			}
			if !matched {
				t.Errorf("errors.As failed to identify %s", tc.name)
			}
		})
	}
}

// TestErrorHandler_StatusMapping drives the central ErrorHandler with each
// typed error and asserts the HTTP status and JSON envelope it produces.
func TestErrorHandler_StatusMapping(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantError  string
	}{
		{"NotFound", NewNotFound("no row"), 404, "Not Found"},
		{"Validation", NewValidation("bad"), 400, "Bad Request"},
		{"ValidationWithDetails", NewValidationWith("bad", map[string]string{"f": "x"}), 400, "Bad Request"},
		{"OtpFailure", NewOtpFailure("expired"), 400, "Bad Request"},
		{"Conflict", NewConflict("dup"), 409, "Conflict"},
		{"Unauthorized", NewUnauthorized("no auth"), 401, "Unauthorized"},
		{"Forbidden", NewForbidden("no role"), 403, "Forbidden"},
		{"PanFailure", NewPanFailure("bad pan"), 422, "Unprocessable Entity"},
		{"PayloadTooLarge", NewPayloadTooLarge("big"), 413, "Payload Too Large"},
		{"ServiceUnavailable", NewServiceUnavailable("down"), 503, "Service Unavailable"},
		{"FiberError", fiber.NewError(fiber.StatusTeapot, "tea"), 418, "Error"},
		{"Unhandled", errors.New("boom"), 500, "Internal Server Error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := fiber.New(fiber.Config{DisableStartupMessage: true, ErrorHandler: ErrorHandler})
			app.Get("/x", func(c *fiber.Ctx) error { return tc.err })

			resp, err := app.Test(httpReq("GET", "/x"))
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			var body ErrorBody
			if err := decodeBody(resp, &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if body.Status != tc.wantStatus {
				t.Errorf("envelope status = %d, want %d", body.Status, tc.wantStatus)
			}
			if body.Error != tc.wantError {
				t.Errorf("envelope error = %q, want %q", body.Error, tc.wantError)
			}
		})
	}
}

// TestErrorHandler_DetailsSurface confirms the per-field details map from a
// Validation error is serialized into the response body.
func TestErrorHandler_DetailsSurface(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true, ErrorHandler: ErrorHandler})
	app.Get("/x", func(c *fiber.Ctx) error {
		return NewValidationWith("Validation failed", map[string]string{"email": "required"})
	})

	resp, err := app.Test(httpReq("GET", "/x"))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	var body ErrorBody
	_ = decodeBody(resp, &body)
	if body.Details == nil {
		t.Fatal("expected details map in response, got nil")
	}
	if body.Details.(map[string]any)["email"] != "required" {
		t.Errorf("details.email = %v", body.Details)
	}
}

// TestErrorHandler_UnhandledDoesNotLeakMessage confirms a plain (untyped) error
// is reported as a generic 500 without its internal message leaking.
func TestErrorHandler_UnhandledDoesNotLeakMessage(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true, ErrorHandler: ErrorHandler})
	app.Get("/x", func(c *fiber.Ctx) error { return errors.New("secret internal detail") })

	resp, err := app.Test(httpReq("GET", "/x"))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	var body ErrorBody
	_ = decodeBody(resp, &body)
	if body.Message != "Unexpected error" {
		t.Errorf("unhandled error leaked internals: %q", body.Message)
	}
}

// TestIsFiberBodyLimit confirms the heuristic that maps Fiber's 413 to our
// PayloadTooLarge shape.
func TestIsFiberBodyLimit(t *testing.T) {
	if !isFiberBodyLimit(fiber.NewError(fiber.StatusRequestEntityTooLarge, "big")) {
		t.Error("expected 413 fiber error to be detected as body limit")
	}
	if isFiberBodyLimit(fiber.NewError(fiber.StatusBadRequest, "bad")) {
		t.Error("400 fiber error should not be detected as body limit")
	}
	if isFiberBodyLimit(errors.New("not fiber")) {
		t.Error("plain error should not be detected as body limit")
	}
}

// ---- helpers ----

func httpReq(method, target string) *http.Request {
	req, _ := http.NewRequest(method, target, nil)
	return req
}

func decodeBody(resp *http.Response, out any) error {
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	return dec.Decode(out)
}
