package repository

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// classifyPgErr translates driver-level errors into the repository's
// sentinel errors so callers don't need to import pgconn.
//
//   - unique-violation (23505) -> ErrConflict
//   - anything else             -> returned unchanged
func classifyPgErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrConflict
	}
	return err
}

// nilString returns nil for an empty string so INSERTs can fall back to a
// column DEFAULT via COALESCE, and non-empty values pass through as a pointer.
func nilString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
