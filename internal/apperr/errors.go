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
	"fmt"
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

// PanFailure maps to HTTP 422 (PAN format / OCR mismatch).
type PanFailure struct{ Msg string }

func (e *PanFailure) Error() string { return e.Msg }

// PayloadTooLarge maps to HTTP 413.
type PayloadTooLarge struct{ Msg string }

func (e *PayloadTooLarge) Error() string { return e.Msg }

// ---- Constructors -------------------------------------------------------

func NewNotFound(msg string) error                    { return &NotFound{Msg: msg} }
func NewValidation(msg string) error                  { return &Validation{Msg: msg} }
func NewValidationWith(msg string, d map[string]string) error {
	return &Validation{Msg: msg, Details: d}
}
func NewOtpFailure(msg string) error                  { return &OtpFailure{Msg: msg} }
func NewConflict(msg string) error                    { return &Conflict{Msg: msg} }
func NewUnauthorized(msg string) error                { return &Unauthorized{Msg: msg} }
func NewPanFailure(msg string) error                  { return &PanFailure{Msg: msg} }
func NewPayloadTooLarge(msg string) error             { return &PayloadTooLarge{Msg: msg} }

// As lets callers test for a typed error without importing this package's
// concrete types: errors.As(err, &target) where target is *apperr.Conflict etc.
var _ = errors.As

// ---- Fiber error handler ------------------------------------------------

// ErrorHandler is wired as app.Config.ErrorHandler. It returns the same JSON
// envelope for every error.
func ErrorHandler(c *fiber.Ctx, err error) error {
	var (
		nf  *NotFound
		v   *Validation
		of  *OtpFailure
		cf  *Conflict
		ua  *Unauthorized
		pf  *PanFailure
		ptl *PayloadTooLarge
	)

	switch {
	case errors.As(err, &nf):
		return writeError(c, 404, "Not Found", nf.Msg, nil)
	case errors.As(err, &v):
		return writeError(c, 400, "Bad Request", v.Msg, v.Details)
	case errors.As(err, &of):
		return writeError(c, 400, "Bad Request", of.Msg, nil)
	case errors.As(err, &cf):
		return writeError(c, 409, "Conflict", cf.Msg, nil)
	case errors.As(err, &ua):
		return writeError(c, 401, "Unauthorized", ua.Msg, nil)
	case errors.As(err, &pf):
		return writeError(c, 422, "Unprocessable Entity", pf.Msg, nil)
	case errors.As(err, &ptl):
		return writeError(c, 413, "Payload Too Large", ptl.Msg, nil)
	case isFiberBodyLimit(err):
		// Fiber's body limit error isn't exported; detect by message.
		return writeError(c, 413, "Payload Too Large",
			"Uploaded image exceeds the maximum allowed size", nil)
	}

	// fiber.Error pass-through (covers 4xx the framework raises itself).
	var fe *fiber.Error
	if errors.As(err, &fe) {
		return writeError(c, fe.Code, "Error", fe.Message, nil)
	}

	// Fallback: log and return 500 without leaking internals.
	fmt.Printf("[error] unhandled: %v\n", err)
	return writeError(c, 500, "Internal Server Error", "Unexpected error", nil)
}

func isFiberBodyLimit(err error) bool {
	var fe *fiber.Error
	if errors.As(err, &fe) && fe.Code == 413 {
		return true
	}
	return false
}

type errorBody struct {
	Status    int         `json:"status"`
	Error     string      `json:"error"`
	Message   string      `json:"message"`
	Details   interface{} `json:"details,omitempty"`
	Timestamp time.Time   `json:"timestamp"`
}

func writeError(c *fiber.Ctx, status int, errName, msg string, details interface{}) error {
	return c.Status(status).JSON(errorBody{
		Status:    status,
		Error:     errName,
		Message:   msg,
		Details:   details,
		Timestamp: time.Now().UTC(),
	})
}
