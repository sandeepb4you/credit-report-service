-- ---------------------------------------------------------------------------
-- password_reset_tokens: the grant handed out after a "forgot password" OTP
-- has been verified, and redeemed by POST /api/auth/password/reset.
--
-- WHY a separate credential instead of resetting on the OTP itself: the reset
-- is a two-screen flow (enter code -> choose a new password), and re-sending
-- the OTP with the new password would mean the client holds a working OTP for
-- as long as the user takes to type a password, with no way to tell a resumed
-- flow from a replayed one. A single-use token with its own short expiry makes
-- "this code was already checked" an explicit, revocable server-side fact.
--
-- Only the SHA-256 hex digest is stored, exactly as for sessions.refresh_token
-- _hash: the token is 32 random bytes, so there is nothing to brute-force and
-- the lookup stays indexable. A database leak yields no usable credential.
--
-- consumed_at is set by a compare-and-set (WHERE consumed_at IS NULL), so two
-- concurrent redemptions cannot both change the password.
-- ---------------------------------------------------------------------------
CREATE TABLE password_reset_tokens (
    id          BIGSERIAL   PRIMARY KEY,
    account_id  BIGINT      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,

    token_hash  CHAR(64)    NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,

    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Issuing a new grant invalidates the account's outstanding ones, which scans
-- by account.
CREATE INDEX idx_password_reset_account ON password_reset_tokens (account_id)
    WHERE consumed_at IS NULL;
