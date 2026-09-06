-- ---------------------------------------------------------------------------
-- signup_tokens: the grant handed out after an email-signup OTP has been
-- verified, and redeemed by POST /api/auth/signup/complete.
--
-- The sibling of password_reset_tokens, with one structural difference that is
-- the whole point of the table: there is no account yet. The redesigned signup
-- proves the address BEFORE a password is chosen, so at the moment this row is
-- written nothing has been created — no accounts row, no auth_identities row,
-- nothing for a foreign key to point at. The grant is therefore keyed on the
-- normalized email, and /signup/complete is what finally creates the account.
--
-- WHY a separate credential rather than re-sending the OTP with the password:
-- exactly as for password resets. The client would otherwise hold a live OTP
-- for as long as the user takes to choose a password, and the server could not
-- tell a resumed flow from a replayed one. A single-use token with its own
-- short expiry makes "this code was already checked" an explicit server fact.
--
-- Only the SHA-256 hex digest is stored, as for sessions.refresh_token_hash
-- and password_reset_tokens.token_hash: the token is 32 random bytes, so there
-- is nothing to brute-force and a database leak yields no usable credential.
--
-- consumed_at is set by a compare-and-set (WHERE consumed_at IS NULL), so two
-- concurrent redemptions cannot both create an account.
-- ---------------------------------------------------------------------------
CREATE TABLE signup_tokens (
    id          BIGSERIAL    PRIMARY KEY,

    -- Normalized (lower-cased, trimmed) email the code was proven against.
    -- Not a foreign key, and not unique: an abandoned signup leaves a row
    -- behind, and the address must stay free for someone to try again.
    email       VARCHAR(255) NOT NULL,

    token_hash  CHAR(64)     NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ  NOT NULL,
    consumed_at TIMESTAMPTZ,

    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- Issuing a new grant invalidates the address's outstanding ones, which scans
-- by email.
CREATE INDEX idx_signup_token_email ON signup_tokens (email)
    WHERE consumed_at IS NULL;
