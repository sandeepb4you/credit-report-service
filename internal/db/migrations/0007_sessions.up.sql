-- Sessions: one row per signed-in device.
--
-- Auth is split into a short-lived stateless access JWT (minutes) and a
-- long-lived opaque refresh token (weeks) anchored to a row here. The access
-- token stays cheap to verify with no DB read; revocation acts on this table
-- and takes effect within one access-token lifetime.
--
--   accounts
--     └── sessions   0..N  one per device the account is signed in on

CREATE TABLE sessions (
    id                  BIGSERIAL   PRIMARY KEY,
    account_id          BIGINT      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,

    -- The refresh token is 32 random bytes, shown to the client exactly once.
    -- Only its SHA-256 hex digest is stored, so a database leak yields no
    -- usable credentials. SHA-256 rather than bcrypt is correct here: the
    -- input is full-entropy random (nothing to brute-force) and the lookup
    -- must stay indexable.
    refresh_token_hash  CHAR(64)    NOT NULL UNIQUE,

    -- The digest this session issued immediately before the current one.
    -- Presenting it means an already-rotated token was replayed, which implies
    -- the token was stolen -- the session is revoked on sight. See
    -- service.SessionService.Refresh.
    prev_token_hash     CHAR(64)    UNIQUE,

    -- Device identity. device_id is supplied by the client (X-Device-Id) and
    -- is NOT a security boundary: a hostile client can send any value. It only
    -- groups repeat logins from one physical device so the "signed-in devices"
    -- list shows one entry per device rather than one per login. Uniqueness is
    -- scoped per account, so no client can collide with another account's row.
    device_id           VARCHAR(128),
    device_name         VARCHAR(128),
    device_platform     VARCHAR(16),           -- 'ios' | 'android' | 'web'
    user_agent          VARCHAR(512),

    -- Client address. Only meaningful behind a proxy when server.trusted-proxies
    -- names that proxy -- otherwise this is the load balancer, not the user.
    ip                  VARCHAR(45),           -- textual, IPv6-max length

    -- Richer device description, refreshed on every token rotation:
    --
    --   {"client": {...},   -- declared by the app: make, model, OS version, ...
    --    "agent":  {...}}   -- parsed by the server from the User-Agent header
    --
    -- JSONB rather than columns because the useful fields differ per platform
    -- (a phone reports make/model/OS build, a browser reports engine/OS) and
    -- because clients add their own keys without needing a migration. The
    -- "client" half is untrusted, self-reported, bounded input -- display only,
    -- never queried for authorization. See middleware.Device.
    device_meta         JSONB,

    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at          TIMESTAMPTZ NOT NULL,
    revoked_at          TIMESTAMPTZ,
    revoked_reason      VARCHAR(32)            -- see models.Revoke* constants
);

-- One live session per (account, device). Revoked rows are excluded so signing
-- back in on the same device inserts cleanly instead of colliding, and so the
-- revoked history stays queryable. Sessions with no device_id (clients that
-- don't send X-Device-Id) are excluded too: each login gets its own row.
CREATE UNIQUE INDEX idx_sessions_account_device
    ON sessions (account_id, device_id)
    WHERE device_id IS NOT NULL AND revoked_at IS NULL;

CREATE INDEX idx_sessions_account ON sessions (account_id);
CREATE INDEX idx_sessions_expires ON sessions (expires_at) WHERE revoked_at IS NULL;
