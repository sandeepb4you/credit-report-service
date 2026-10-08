package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// StartImpersonation records an admin opening a read-only view of another
// account and returns the audit row's id, which the view's token carries.
func (r *AccountRepo) StartImpersonation(ctx context.Context, adminID, targetID int64, expiresAt time.Time) (int64, error) {
	var id int64
	err := r.pool.QueryRow(ctx,
		`INSERT INTO admin_impersonations (admin_account_id, target_account_id, expires_at)
		 VALUES ($1, $2, $3) RETURNING id`, adminID, targetID, expiresAt).Scan(&id)
	return id, err
}

// EndImpersonation closes a view early. Only the admin who opened it can end
// it; ErrNotFound covers someone else's row and an unknown id alike. Ending an
// already-ended view is not an error — Exit may be pressed twice.
func (r *AccountRepo) EndImpersonation(ctx context.Context, id, adminID int64) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE admin_impersonations
		    SET ended_at = COALESCE(ended_at, now())
		  WHERE id = $1 AND admin_account_id = $2`, id, adminID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ImpersonationLive reports whether a view's token may still be used: its row
// exists, has not been ended, and has not expired. Checked on every request a
// view token makes, so ending a view takes effect immediately.
func (r *AccountRepo) ImpersonationLive(ctx context.Context, id int64) (bool, error) {
	var live bool
	err := r.pool.QueryRow(ctx,
		`SELECT ended_at IS NULL AND expires_at > now()
		   FROM admin_impersonations WHERE id = $1`, id).Scan(&live)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return live, err
}
