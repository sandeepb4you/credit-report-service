package repository

import (
	"context"
	"errors"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"credit-report-service/internal/models"
)

// SessionRepo is the data access layer for sessions: one row per signed-in
// device. Refresh-token digests are written and matched here but never
// selected out — callers get a models.Session with no credential material.
type SessionRepo struct{ pool *pgxpool.Pool }

func NewSessionRepo(pool *pgxpool.Pool) *SessionRepo { return &SessionRepo{pool: pool} }

const sessionCols = `id, account_id, device_id, device_name, device_platform,
    user_agent, ip, device_meta, created_at, last_used_at, expires_at,
    revoked_at, revoked_reason`

// Create opens a session for a device.
//
// Re-login from a device that already has a live session updates that row in
// place (new token digest, refreshed metadata) rather than inserting a second
// one, so the device list shows one entry per device. That is what the partial
// unique index on (account_id, device_id) is for. Clients that send no
// device_id fall outside the index and get a fresh row per login.
func (r *SessionRepo) Create(
	ctx context.Context, s *models.Session, tokenHash string,
) error {
	return pgxscan.Get(ctx, r.pool, s,
		`INSERT INTO sessions
		     (account_id, refresh_token_hash, device_id, device_name,
		      device_platform, user_agent, ip, device_meta, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 ON CONFLICT (account_id, device_id)
		     WHERE device_id IS NOT NULL AND revoked_at IS NULL
		 DO UPDATE SET
		     refresh_token_hash = EXCLUDED.refresh_token_hash,
		     prev_token_hash    = NULL,
		     device_name        = EXCLUDED.device_name,
		     device_platform    = EXCLUDED.device_platform,
		     user_agent         = EXCLUDED.user_agent,
		     ip                 = EXCLUDED.ip,
		     device_meta        = EXCLUDED.device_meta,
		     expires_at         = EXCLUDED.expires_at,
		     last_used_at       = now()
		 RETURNING `+sessionCols,
		s.AccountID, tokenHash, s.DeviceID, s.DeviceName,
		s.DevicePlatform, s.UserAgent, s.IP, s.DeviceMeta, s.ExpiresAt)
}

// FindLiveByHash returns the live (unrevoked, unexpired) session holding this
// refresh-token digest.
func (r *SessionRepo) FindLiveByHash(ctx context.Context, tokenHash string) (*models.Session, error) {
	var s models.Session
	err := pgxscan.Get(ctx, r.pool, &s,
		`SELECT `+sessionCols+` FROM sessions
		 WHERE refresh_token_hash = $1 AND revoked_at IS NULL AND expires_at > now()`,
		tokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &s, err
}

// FindByPrevHash returns the session whose *previous* token digest matches,
// regardless of state. A hit means an already-rotated refresh token was
// presented — i.e. replay — and the caller must revoke the session.
func (r *SessionRepo) FindByPrevHash(ctx context.Context, tokenHash string) (*models.Session, error) {
	var s models.Session
	err := pgxscan.Get(ctx, r.pool, &s,
		`SELECT `+sessionCols+` FROM sessions WHERE prev_token_hash = $1`, tokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &s, err
}

// Rotate swaps in a new refresh-token digest, demoting the current one to
// prev_token_hash so a replay of it is detectable. The compare-and-set on
// refresh_token_hash makes concurrent refreshes safe: only the first wins, and
// the loser sees ErrNotFound rather than silently issuing a second token.
//
// The volatile device fields are refreshed from the calling request, so
// last_used_at and the IP / user agent / device_meta beside it always describe
// the same moment. Each one falls back to the stored value when the client
// sends nothing, so a sparse refresh call never blanks out a populated row.
func (r *SessionRepo) Rotate(
	ctx context.Context, sessionID int64, oldHash, newHash string,
	expiresAt time.Time, dev *models.Session,
) (*models.Session, error) {
	var s models.Session
	err := pgxscan.Get(ctx, r.pool, &s,
		`UPDATE sessions
		    SET refresh_token_hash = $3,
		        prev_token_hash    = $2,
		        expires_at         = $4,
		        device_name        = COALESCE($5, device_name),
		        user_agent         = COALESCE($6, user_agent),
		        ip                 = COALESCE($7, ip),
		        device_meta        = COALESCE($8, device_meta),
		        last_used_at       = now()
		  WHERE id = $1
		    AND refresh_token_hash = $2
		    AND revoked_at IS NULL
		RETURNING `+sessionCols,
		sessionID, oldHash, newHash, expiresAt,
		dev.DeviceName, dev.UserAgent, dev.IP, dev.DeviceMeta)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &s, err
}

// ListLive returns an account's live sessions, most recently used first.
func (r *SessionRepo) ListLive(ctx context.Context, accountID int64) ([]*models.Session, error) {
	out := []*models.Session{}
	err := pgxscan.Select(ctx, r.pool, &out,
		`SELECT `+sessionCols+` FROM sessions
		  WHERE account_id = $1 AND revoked_at IS NULL AND expires_at > now()
		  ORDER BY last_used_at DESC`,
		accountID)
	return out, err
}

// Revoke kills one session. It is scoped by account_id so an account can only
// ever revoke its own sessions, even if it guesses another id. Returns false
// when nothing matched (wrong owner, already revoked, or no such session).
func (r *SessionRepo) Revoke(
	ctx context.Context, accountID, sessionID int64, reason string,
) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE sessions SET revoked_at = now(), revoked_reason = $3
		  WHERE id = $1 AND account_id = $2 AND revoked_at IS NULL`,
		sessionID, accountID, reason)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// RevokeAll kills every live session for an account except keepID (pass 0 to
// revoke all of them). Returns the number revoked.
func (r *SessionRepo) RevokeAll(
	ctx context.Context, accountID, keepID int64, reason string,
) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE sessions SET revoked_at = now(), revoked_reason = $3
		  WHERE account_id = $1 AND revoked_at IS NULL AND id <> $2`,
		accountID, keepID, reason)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// DeleteExpired purges sessions that expired or were revoked before the cutoff.
// Revoked rows are kept until the cutoff so token-reuse forensics survive a
// little past the event.
func (r *SessionRepo) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM sessions WHERE expires_at < $1 OR revoked_at < $1`, before)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ---- push tokens ------------------------------------------------------------

// SetPushToken registers the device's FCM token on the caller's session, and
// STEALS it from any other session still holding it first. Signing out and back
// in mints a new session on the same physical device with the same FCM token;
// without the steal the dead row still claims it and every notification sends
// twice (or lands attributed to the previous account on a shared device).
// One transaction, so the token is never on two live rows or zero rows between
// the two statements.
func (r *SessionRepo) SetPushToken(ctx context.Context, sessionID int64, token string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`UPDATE sessions SET fcm_token = NULL, fcm_token_updated_at = now()
		  WHERE fcm_token = $1 AND id <> $2`, token, sessionID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx,
		`UPDATE sessions SET fcm_token = $2, fcm_token_updated_at = now()
		  WHERE id = $1 AND revoked_at IS NULL`, sessionID, token)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound // revoked or unknown session — a dead login must not re-arm pushes
	}
	return tx.Commit(ctx)
}

// ClearPushToken detaches the caller's token (explicit client opt-out).
func (r *SessionRepo) ClearPushToken(ctx context.Context, sessionID int64) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE sessions SET fcm_token = NULL, fcm_token_updated_at = now()
		  WHERE id = $1`, sessionID)
	return err
}

// LivePushTokens returns every live token for an account — the notification
// fan-out read. Revoked sessions are excluded by definition, which is the whole
// design: logout silences a device with no push-specific bookkeeping.
func (r *SessionRepo) LivePushTokens(ctx context.Context, accountID int64) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT fcm_token FROM sessions
		  WHERE account_id = $1 AND revoked_at IS NULL AND expires_at > now()
		    AND fcm_token IS NOT NULL`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tokens []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// DropPushToken removes a token wherever it is — the reaction to FCM reporting
// it UNREGISTERED (app uninstalled, or the token rotated away under us).
func (r *SessionRepo) DropPushToken(ctx context.Context, token string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE sessions SET fcm_token = NULL, fcm_token_updated_at = now()
		  WHERE fcm_token = $1`, token)
	return err
}

// ExpireStalePushTokens clears tokens not refreshed since cutoff, per
// Firebase's staleness guidance — a token the app has not confirmed in months
// belongs to a device that likely cleared data or went inactive.
func (r *SessionRepo) ExpireStalePushTokens(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`UPDATE sessions SET fcm_token = NULL, fcm_token_updated_at = now()
		  WHERE fcm_token IS NOT NULL AND fcm_token_updated_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
