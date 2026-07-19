package repository

import "errors"

// ErrNotFound is returned by repository Get/Find methods when a row is absent.
// Service-layer code maps this to apperr.NewNotFound.
var ErrNotFound = errors.New("not found")

// ErrConflict indicates a unique-constraint violation surfaced via the DB driver.
// Service-layer code maps this to apperr.NewConflict when relevant.
var ErrConflict = errors.New("conflict")
