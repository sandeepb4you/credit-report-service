package repository

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"credit-report-service/internal/apperr"
)

// TestClassifyPgErr_UniqueViolation confirms a Postgres unique-violation (SQL
// state 23505) is translated into the repo's ErrConflict sentinel so service
// code can branch on it without importing the pg driver.
func TestClassifyPgErr_UniqueViolation(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "23505", Message: "duplicate key"}
	got := classifyPgErr(pgErr)
	if !errors.Is(got, ErrConflict) {
		t.Errorf("23505 -> %v, want ErrConflict", got)
	}
}

// TestClassifyPgErr_ConstraintViolations confirms the constraint codes become
// validation errors (400) rather than falling through to the error handler's
// 500 fallback. Bad input is the caller's problem to fix, not a server fault.
func TestClassifyPgErr_ConstraintViolations(t *testing.T) {
	// Postgres quotes the offending value in its message. Standing in for the
	// PAN or email address this service would put there.
	const secret = "ABCDE1234F"

	for _, code := range []string{"22001", "23514", "23503", "23502"} {
		t.Run(code, func(t *testing.T) {
			pgErr := &pgconn.PgError{
				Code:    code,
				Message: `Key (pan_number)=(` + secret + `) is invalid`,
				Detail:  `Failing row contains (` + secret + `)`,
			}
			got := classifyPgErr(pgErr)

			var v *apperr.Validation
			if !errors.As(got, &v) {
				t.Fatalf("%s -> %T (%v), want *apperr.Validation", code, got, got)
			}
			if strings.Contains(v.Error(), secret) {
				t.Errorf("driver message leaked the offending value: %q", v.Error())
			}
			if v.Msg == "" {
				t.Error("validation error has no message for the caller")
			}
		})
	}
}

// TestClassifyPgErr_UnknownCodesPassedThrough confirms genuine failures still
// surface as-is, so a connection fault is not reported to the caller as a
// validation problem.
func TestClassifyPgErr_UnknownCodesPassedThrough(t *testing.T) {
	// 40001 serialization_failure: a real fault, not bad input.
	other := &pgconn.PgError{Code: "40001", Message: "could not serialize access"}
	if got := classifyPgErr(other); got != other {
		t.Errorf("40001 should pass through unchanged, got %v", got)
	}
	plain := errors.New("network blip")
	if got := classifyPgErr(plain); got != plain {
		t.Errorf("plain error should pass through, got %v", got)
	}
	if got := classifyPgErr(nil); got != nil {
		t.Errorf("nil should stay nil, got %v", got)
	}
}

// TestNilString confirms empty -> nil (so the column DEFAULT applies via
// COALESCE) and non-empty -> a pointer to a copy.
func TestNilString(t *testing.T) {
	if got := nilString(""); got != nil {
		t.Errorf("nilString(\"\") = %v, want nil", got)
	}
	got := nilString("ABCDE1234F")
	if got == nil || *got != "ABCDE1234F" {
		t.Errorf("nilString(non-empty) = %v, want pointer to value", got)
	}
}
