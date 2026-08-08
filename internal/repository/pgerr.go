package repository

import (
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgconn"

	"credit-report-service/internal/apperr"
)

// Postgres SQLSTATE codes this layer knows how to answer for.
const (
	pgUniqueViolation     = "23505"
	pgStringTooLong       = "22001"
	pgCheckViolation      = "23514"
	pgForeignKeyViolation = "23503"
	pgNotNullViolation    = "23502"
)

// classifyPgErr translates driver-level errors into errors the HTTP layer can
// render. Without it a constraint violation reaches the central error handler
// unrecognised and becomes a 500 — telling the caller the server broke, when
// in fact their input was rejected.
//
//   - 23505 unique_violation  -> ErrConflict, so the caller can attach its own
//     wording ("PAN is already linked to another account")
//   - 22001 / 23514 / 23503 / 23502 -> a validation error (400). There is no
//     useful per-call-site wording for these, so they are mapped here rather
//     than at every call site.
//   - anything else -> returned unchanged
//
// The database's own message is never forwarded: it can quote the offending
// value, which for this service means a PAN or an email address in an HTTP
// response body. The details go to the log instead.
func classifyPgErr(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}

	if pgErr.Code == pgUniqueViolation {
		return ErrConflict
	}

	msg, ok := constraintMessage(pgErr.Code)
	if !ok {
		return err
	}
	// Reaching the database means a check the service layer should have done
	// first was missing. Log it so the gap is visible even though the caller
	// gets a clean 400.
	slog.Warn("constraint violation reached the database",
		"code", pgErr.Code,
		"constraint", pgErr.ConstraintName,
		"table", pgErr.TableName,
		"column", pgErr.ColumnName,
	)
	return apperr.NewValidation(msg)
}

// constraintMessage maps a SQLSTATE to caller-safe wording. The bool is false
// for codes this layer does not claim, which are returned unchanged.
func constraintMessage(code string) (string, bool) {
	switch code {
	case pgStringTooLong:
		return "A submitted value is too long", true
	case pgCheckViolation:
		return "A submitted value is not allowed", true
	case pgForeignKeyViolation:
		return "A referenced record does not exist", true
	case pgNotNullViolation:
		return "A required value is missing", true
	}
	return "", false
}

// nilInt64 returns nil for a non-positive id so it is written as SQL NULL.
// Used for the KYC reviewer: an automated (demo-mode) verification has no human
// behind it, and recording NULL says that honestly instead of attributing the
// decision to account 0 or to the applicant themselves.
func nilInt64(v int64) *int64 {
	if v <= 0 {
		return nil
	}
	return &v
}

// nilString returns nil for an empty string so INSERTs can fall back to a
// column DEFAULT via COALESCE, and non-empty values pass through as a pointer.
func nilString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
