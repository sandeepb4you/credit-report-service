-- Purchasable products and payment orders (Cashfree PG, API 2025-01-01).
--
--   products                the catalog: what can be bought and for how much
--     └── orders            one row per purchase attempt, linked to an account
--   payment_webhook_events  raw Cashfree webhook audit log + idempotency guard

-- ---------------------------------------------------------------------------
-- products: seeded catalog. Prices live here, not in code.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS products (
    code        VARCHAR(50)   PRIMARY KEY,
    name        VARCHAR(100)  NOT NULL,
    amount      NUMERIC(12,2) NOT NULL,
    currency    CHAR(3)       NOT NULL DEFAULT 'INR',
    active      BOOLEAN       NOT NULL DEFAULT TRUE,

    created_at  TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ   NOT NULL DEFAULT now()
);

-- ON CONFLICT keeps the seed idempotent, matching the IF NOT EXISTS guards
-- above: re-running this migration against a database that already has the
-- catalog is a no-op rather than a duplicate-key failure. Existing rows are
-- left alone so locally edited prices survive.
INSERT INTO products (code, name, amount) VALUES
    ('CREDIT_ANALYSIS',          'Credit Report Analysis',  299.00),
    ('BANK_STATEMENT_ANALYSIS',  'Bank Statement Analysis', 299.00),
    ('UPI_STATEMENT_ANALYSIS',   'UPI Statement Analysis',  299.00)
ON CONFLICT (code) DO NOTHING;

-- ---------------------------------------------------------------------------
-- orders: one row per purchase attempt.
--
-- order_uid is our identifier sent to Cashfree as its order_id (Cashfree
-- allows 3-45 chars, alphanumeric/underscore/hyphen — a UUID string fits).
-- The row is inserted BEFORE the Cashfree call (CREATION_REQUESTED) so the
-- uid exists to send; amount/currency are snapshotted from the product so
-- later price changes don't rewrite history.
--
-- Status lifecycle:
--   CREATION_REQUESTED -> ACTIVE            (Cashfree order created)
--                      -> CREATION_FAILED   (Cashfree call failed)
--   ACTIVE             -> PAID | FAILED | EXPIRED | TERMINATED
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS orders (
    id                  BIGSERIAL     PRIMARY KEY,
    order_uid           VARCHAR(45)   NOT NULL UNIQUE,
    account_id          BIGINT        NOT NULL REFERENCES accounts (id),
    product_code        VARCHAR(50)   NOT NULL REFERENCES products (code),

    amount              NUMERIC(12,2) NOT NULL,
    currency            CHAR(3)       NOT NULL DEFAULT 'INR',
    status              VARCHAR(30)   NOT NULL DEFAULT 'CREATION_REQUESTED',

    cf_order_id         VARCHAR(64)   UNIQUE,     -- Cashfree's id for the order
    payment_session_id  TEXT,                     -- frontend opens checkout with this
    cf_payment_id       VARCHAR(64),              -- from the success webhook
    payment_method      VARCHAR(50),              -- upi / credit_card / net_banking ...
    failure_reason      TEXT,
    order_expiry_time   TIMESTAMPTZ,

    paid_at             TIMESTAMPTZ,
    fulfilled_at        TIMESTAMPTZ,              -- when the analysis entitlement was granted

    created_at          TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ   NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_orders_account ON orders (account_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- payment_webhook_events: every received Cashfree webhook, verbatim.
--
-- The UNIQUE idempotency_key makes duplicate webhook delivery a no-op at
-- insert time; the guarded status transitions on orders are the second line
-- of defence (two success webhooks with different keys still fulfil once).
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS payment_webhook_events (
    id               BIGSERIAL    PRIMARY KEY,
    idempotency_key  VARCHAR(128) UNIQUE,        -- x-idempotency-key header (nullable)
    event_type       VARCHAR(64)  NOT NULL,
    order_uid        VARCHAR(45),
    payload          JSONB        NOT NULL,
    processed        BOOLEAN      NOT NULL DEFAULT FALSE,

    received_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_webhook_events_order ON payment_webhook_events (order_uid);
