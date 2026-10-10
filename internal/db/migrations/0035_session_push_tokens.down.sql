DROP INDEX IF EXISTS idx_sessions_push;
DROP INDEX IF EXISTS ux_sessions_fcm_token;
ALTER TABLE sessions
    DROP COLUMN IF EXISTS fcm_token,
    DROP COLUMN IF EXISTS fcm_token_updated_at;
