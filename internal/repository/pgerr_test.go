package repository

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
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

// TestClassifyPgErr_OtherCodesPassedThrough confirms non-unique driver errors
// and plain errors are returned unchanged (so real DB failures still surface).
func TestClassifyPgErr_OtherCodesPassedThrough(t *testing.T) {
	other := &pgconn.PgError{Code: "23503", Message: "foreign key violation"}
	if got := classifyPgErr(other); got != other {
		t.Errorf("23503 should pass through unchanged, got %v", got)
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
