// Package apperr defines sentinel application errors and the centralised
// error-to-HTTP translation used by the Fiber handlers.
//
// Each public function returns `error` and wraps domain failures in one of the
// typed errors declared here. The Fiber error handler in this package maps
// them to the same JSON envelope Spring's GlobalExceptionHandler produced:
//
//	{"status":int, "error":string, "message":string, "details":any, "timestamp":RFC3339}
package apperr

import (
	"errors"
	"log/slog"
	"time"

	"github.com/gofiber/fiber/v2"
)

// ---- Typed errors -------------------------------------------------------

// NotFound maps to HTTP 404.
type NotFound struct{ Msg string }

func (e *NotFound) Error() string { return e.Msg }

// Validation maps to HTTP 400. Details is the per-field map produced by
// request-body validation failures.
type Validation struct {
	Msg     string
	Details map[string]string
}

func (e *Validation) Error() string { return e.Msg }

// OtpFailure maps to HTTP 400 (wrong / expired / locked OTP).
type OtpFailure struct{ Msg string }

func (e *OtpFailure) Error() string { return e.Msg }

// Conflict maps to HTTP 409 (wrong lifecycle stage or duplicate user).
type Conflict struct{ Msg string }

func (e *Conflict) Error() string { return e.Msg }

// Unauthorized maps to HTTP 401 (missing / invalid / expired credentials).
type Unauthorized struct{ Msg string }

func (e *Unauthorized) Error() string { return e.Msg }

// Forbidden maps to HTTP 403 (authenticated but lacking the required role).
type Forbidden struct{ Msg string }

func (e *Forbidden) Error() string { return e.Msg }

// PanFailure maps to HTTP 422 (PAN format / OCR mismatch).
type PanFailure struct{ Msg string }

func (e *PanFailure) Error() string { return e.Msg }

// PaymentRequired maps to HTTP 402: the caller is authenticated and permitted,
// but the thing they asked for has to be bought first and their account holds no
// unspent purchase of it.
//
// Its own status rather than 403, because the two mean opposite things to a
// client. 403 is "you may not have this", which is final and should show an
// error. 402 is "you may have this once you pay", which the app answers by
// opening the paywall — a distinction the user experiences as a dead end versus
// a next step.
type PaymentRequired struct{ Msg string }

func (e *PaymentRequired) Error() string { return e.Msg }

// PayloadTooLarge maps to HTTP 413.
type PayloadTooLarge struct{ Msg string }

func (e *PayloadTooLarge) Error() string { return e.Msg }

// ServiceUnavailable maps to HTTP 503 (a required provider/config is missing).
type ServiceUnavailable struct{ Msg string }

func (e *ServiceUnavailable) Error() string { return e.Msg }

// BadGateway maps to HTTP 502 (an upstream dependency, e.g. the payment
// gateway, failed).
type BadGateway struct{ Msg string }

func (e *BadGateway) Error() string { return e.Msg }

// ---- Constructors -------------------------------------------------------

func NewNotFound(msg string) error   { return &NotFound{Msg: msg} }
func NewValidation(msg string) error { return &Validation{Msg: msg} }
func NewValidationWith(msg string, d map[string]string) error {
	return &Validation{Msg: msg, Details: d}
}
func NewOtpFailure(msg string) error         { return &OtpFailure{Msg: msg} }
func NewConflict(msg string) error           { return &Conflict{Msg: msg} }
func NewUnauthorized(msg string) error       { return &Unauthorized{Msg: msg} }
func NewForbidden(msg string) error          { return &Forbidden{Msg: msg} }
func NewPanFailure(msg string) error         { return &PanFailure{Msg: msg} }
func NewPaymentRequired(msg string) error    { return &PaymentRequired{Msg: msg} }
func NewPayloadTooLarge(msg string) error    { return &PayloadTooLarge{Msg: msg} }
func NewServiceUnavailable(msg string) error { return &ServiceUnavailable{Msg: msg} }
func NewBadGateway(msg string) error         { return &BadGateway{Msg: msg} }

// As lets callers test for a typed error without importing this package's
// concrete types: errors.As(err, &target) where target is *apperr.Conflict etc.
var _ = errors.As

// ---- Fiber error handler ------------------------------------------------

// ErrorHandler is wired as app.Config.ErrorHandler. It returns the same JSON
// envelope for every error.
// StatusFor reports the HTTP status, title, message and per-field details that
// [ErrorHandler] will produce for err.
//
// The message comes from the typed error's own field rather than err.Error(),
// so wrapping an apperr (fmt.Errorf("context: %w", ...)) cannot leak the
// wrapping prefix into the response body.
//
// Exported so the request logger can name the status a failed request will
// actually get. Fiber runs the error handler AFTER the middleware chain
// unwinds, so a logger reading c.Response().StatusCode() sees the untouched
// default 200 — every failing request was logged as a success, which is exactly
// backwards for the requests you go to the log to find.
//
// One mapping shared by both, rather than a second copy in the middleware that
// would drift the first time a status changed here.
func StatusFor(err error) (status int, title, msg string, details map[string]string) {
	var (
		nf  *NotFound
		v   *Validation
		of  *OtpFailure
		cf  *Conflict
		ua  *Unauthorized
		fb  *Forbidden
		pf  *PanFailure
		pr  *PaymentRequired
		ptl *PayloadTooLarge
		su  *ServiceUnavailable
		bg  *BadGateway
	)

	switch {
	case err == nil:
		return 0, "", "", nil
	case errors.As(err, &nf):
		return 404, "Not Found", nf.Msg, nil
	case errors.As(err, &v):
		return 400, "Bad Request", v.Msg, v.Details
	case errors.As(err, &of):
		return 400, "Bad Request", of.Msg, nil
	case errors.As(err, &cf):
		return 409, "Conflict", cf.Msg, nil
	case errors.As(err, &ua):
		return 401, "Unauthorized", ua.Msg, nil
	case errors.As(err, &fb):
		return 403, "Forbidden", fb.Msg, nil
	case errors.As(err, &pf):
		return 422, "Unprocessable Entity", pf.Msg, nil
	case errors.As(err, &pr):
		return 402, "Payment Required", pr.Msg, nil
	case errors.As(err, &ptl):
		return 413, "Payload Too Large", ptl.Msg, nil
	case errors.As(err, &su):
		return 503, "Service Unavailable", su.Msg, nil
	case errors.As(err, &bg):
		return 502, "Bad Gateway", bg.Msg, nil
	case isFiberBodyLimit(err):
		// Fiber's body limit error isn't exported; detect by code.
		return 413, "Payload Too Large", "Uploaded image exceeds the maximum allowed size", nil
	}

	// fiber.Error pass-through (covers 4xx the framework raises itself).
	var fe *fiber.Error
	if errors.As(err, &fe) {
		return fe.Code, "Error", fe.Message, nil
	}
	// Deliberately generic: nothing mapped this, so its text could be anything.
	return 500, "Internal Server Error", "Unexpected error", nil
}

func ErrorHandler(c *fiber.Ctx, err error) error {
	status, title, msg, details := StatusFor(err)
	if status == 500 {
		// Nothing mapped it, so this is a bug rather than a handled condition.
		// StatusFor already substituted text that is safe to return.
		slog.Error("unhandled error",
			"method", c.Method(),
			"path", c.Path(),
			"error", err,
		)
	}
	return writeError(c, status, title, msg, details)
}

func isFiberBodyLimit(err error) bool {
	var fe *fiber.Error
	if errors.As(err, &fe) && fe.Code == 413 {
		return true
	}
	return false
}

// ErrorBody is the JSON envelope returned for every error response. It is
// exported so Swagger annotations can reference it via `@Failure ... apperr.ErrorBody`.
type ErrorBody struct {
	Status    int         `json:"status"`
	Error     string      `json:"error"`
	Message   string      `json:"message"`
	Details   interface{} `json:"details,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
}

func writeError(c *fiber.Ctx, status int, errName, msg string, details interface{}) error {
	return c.Status(status).JSON(ErrorBody{
		Status:    status,
		Error:     errName,
		Message:   msg,
		Details:   details,
		Timestamp: time.Now().UTC(),
	})
}
