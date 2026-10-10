-- FCM push tokens, living on the session row rather than in a device table of
-- their own: a token belongs to "this app install, signed in as this account",
-- which is exactly what a session already is. Every sign-out path (logout,
-- password reset, account deletion/reset) revokes sessions, so "stop notifying
-- a signed-out device" needs no second lifecycle — the push fan-out only reads
-- live rows. Uninstall has no callback anywhere; it is detected at send time
-- (FCM answers UNREGISTERED) and the token is cleared then.
ALTER TABLE sessions
    ADD COLUMN IF NOT EXISTS fcm_token            TEXT,
    ADD COLUMN IF NOT EXISTS fcm_token_updated_at TIMESTAMPTZ;

COMMENT ON COLUMN sessions.fcm_token IS
    'FCM registration token for the device this session lives on; NULL = no push. Cleared when FCM reports it UNREGISTERED.';

-- One token, one owner. Signing out and back in mints a NEW session on the same
-- device with the same FCM token, so registration STEALS the token from whatever
-- row still holds it (see SessionRepo.SetPushToken) — without that, the dead
-- session still claims the token and every notification sends twice.
CREATE UNIQUE INDEX IF NOT EXISTS ux_sessions_fcm_token
    ON sessions (fcm_token) WHERE fcm_token IS NOT NULL;

-- The fan-out read: every live token for one account.
CREATE INDEX IF NOT EXISTS idx_sessions_push
    ON sessions (account_id) WHERE revoked_at IS NULL AND fcm_token IS NOT NULL;
