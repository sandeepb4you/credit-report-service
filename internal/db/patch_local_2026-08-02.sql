-- One-off patch to bring an already-migrated local database up to the current
-- 0001_init.up.sql, without a rebuild.
--
-- Covers everything schema-related from the 2026-08-02 session:
--   * sessions table (per-device refresh tokens)
--   * agents entity removed
--   * orders gains discount_amount / coupon_code
--   * coupons + coupon_redemptions
--   * schema_migrations reset to 1, since all migrations collapsed into 0001
--
-- Every statement is guarded, so it is safe to run against a database at any
-- point in the old sequence, and safe to run twice. Delete this file once the
-- local DB is patched — 0001_init.up.sql is the single source of truth.
--
-- Run with:
--   psql "$DB_URL" -v ON_ERROR_STOP=1 -f internal/db/patch_local_2026-08-02.sql

BEGIN;

-- The DSN sets this, a bare psql session does not. Without it everything below
-- lands in public while the real tables sit in report.
SET search_path = report;

-- ---------------------------------------------------------------------------
-- 1. accounts: ensure role, drop the agent linkage
-- ---------------------------------------------------------------------------
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS role VARCHAR(20) NOT NULL DEFAULT 'user';
ALTER TABLE accounts DROP COLUMN IF EXISTS agent_id;
ALTER TABLE accounts DROP COLUMN IF EXISTS agent_code_updated;
DROP TABLE IF EXISTS agents;

-- ---------------------------------------------------------------------------
-- 2. sessions: one row per signed-in device
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS sessions (
    id                  BIGSERIAL   PRIMARY KEY,
    account_id          BIGINT      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,

    refresh_token_hash  CHAR(64)    NOT NULL UNIQUE,
    prev_token_hash     CHAR(64)    UNIQUE,

    device_id           VARCHAR(128),
    device_name         VARCHAR(128),
    device_platform     VARCHAR(16),
    user_agent          VARCHAR(512),
    ip                  VARCHAR(45),
    device_meta         JSONB,

    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at          TIMESTAMPTZ NOT NULL,
    revoked_at          TIMESTAMPTZ,
    revoked_reason      VARCHAR(32)
);

-- If sessions predates the device_meta work, add the column.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS device_meta JSONB;

CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_account_device
    ON sessions (account_id, device_id)
    WHERE device_id IS NOT NULL AND revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_sessions_account ON sessions (account_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions (expires_at) WHERE revoked_at IS NULL;

-- ---------------------------------------------------------------------------
-- 3. orders: coupon pricing snapshot
-- ---------------------------------------------------------------------------
ALTER TABLE orders ADD COLUMN IF NOT EXISTS discount_amount NUMERIC(12,2) NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS coupon_code     VARCHAR(32);

-- ---------------------------------------------------------------------------
-- 4. coupons + redemptions
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS coupons (
    id                 BIGSERIAL     PRIMARY KEY,
    code               VARCHAR(32)   NOT NULL UNIQUE,
    created_by         BIGINT        NOT NULL REFERENCES accounts (id),
    discount_percent   NUMERIC(5,2)  NOT NULL,
    product_code       VARCHAR(50)   REFERENCES products (code),

    max_redemptions    INTEGER,
    redemption_count   INTEGER       NOT NULL DEFAULT 0,
    per_account_limit  INTEGER       NOT NULL DEFAULT 1,

    valid_from         TIMESTAMPTZ   NOT NULL DEFAULT now(),
    valid_until        TIMESTAMPTZ,
    revoked_at         TIMESTAMPTZ,

    created_at         TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ   NOT NULL DEFAULT now(),

    CONSTRAINT coupons_percent_range   CHECK (discount_percent > 0 AND discount_percent <= 100),
    CONSTRAINT coupons_max_positive    CHECK (max_redemptions IS NULL OR max_redemptions > 0),
    CONSTRAINT coupons_count_in_range  CHECK (redemption_count >= 0
                                              AND (max_redemptions IS NULL OR redemption_count <= max_redemptions)),
    CONSTRAINT coupons_per_account_pos CHECK (per_account_limit > 0),
    CONSTRAINT coupons_window_ordered  CHECK (valid_until IS NULL OR valid_until > valid_from)
);

CREATE INDEX IF NOT EXISTS idx_coupons_created_by ON coupons (created_by, created_at DESC);

CREATE TABLE IF NOT EXISTS coupon_redemptions (
    id              BIGSERIAL     PRIMARY KEY,
    coupon_id       BIGINT        NOT NULL REFERENCES coupons (id),
    account_id      BIGINT        NOT NULL REFERENCES accounts (id),
    order_uid       VARCHAR(45)   NOT NULL UNIQUE REFERENCES orders (order_uid),
    discount_amount NUMERIC(12,2) NOT NULL,
    redeemed_at     TIMESTAMPTZ   NOT NULL DEFAULT now(),
    released_at     TIMESTAMPTZ,

    CONSTRAINT coupon_redemptions_amount_positive CHECK (discount_amount >= 0)
);

CREATE INDEX IF NOT EXISTS idx_coupon_redemptions_coupon  ON coupon_redemptions (coupon_id);
CREATE INDEX IF NOT EXISTS idx_coupon_redemptions_account ON coupon_redemptions (coupon_id, account_id)
    WHERE released_at IS NULL;

-- ---------------------------------------------------------------------------
-- 5. schema_migrations
--
-- All migrations collapsed into 0001. A database still recording version 6/7/8
-- would never apply a future 0002, because migrate only runs versions greater
-- than the current one. Pin it to 1 so the next migration lands.
-- ---------------------------------------------------------------------------
INSERT INTO schema_migrations (version, dirty)
SELECT 1, false
WHERE NOT EXISTS (SELECT 1 FROM schema_migrations);

UPDATE schema_migrations SET version = 1, dirty = false;

COMMIT;
