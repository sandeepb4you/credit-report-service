-- Complete schema for credit_report_service.
--
-- While the product is pre-launch this is kept as a single init migration
-- rather than an incremental chain: the database is rebuilt from scratch, so
-- carrying the history of columns that were added and later dropped buys
-- nothing and makes the real shape harder to read. Change the schema by
-- editing this file and recreating the database. Once there is data worth
-- keeping, freeze this file and start adding 0002, 0003, ... instead.
--
-- Schema is selected via the `search_path=report` query param on the DSN.
-- golang-migrate runs each statement in order and tracks applied versions in
-- its own table, so no IF NOT EXISTS guards are needed.
--
--   accounts                    the person + onboarding lifecycle + profile
--     |-- auth_identities       1..N  how the account authenticates
--     |-- otp_challenges        0..N  transient email/SMS OTP verification
--     |-- kyc_records           0..1  Aadhaar + PAN, gates analysis products
--     |-- sessions              0..N  one per signed-in device
--     |-- credit_analytics_requests   Digitap proxy audit log
--     |-- orders                0..N  purchase attempts
--     +-- coupons               0..N  discount codes this account issued
--
--   products                    the purchasable catalog
--   payment_webhook_events      raw gateway webhook log + idempotency guard
--   coupon_redemptions          links a coupon to the order that used it

-- ---------------------------------------------------------------------------
-- accounts: one row per user.
-- ---------------------------------------------------------------------------
CREATE TABLE accounts (
    id                 BIGSERIAL PRIMARY KEY,

    -- Account-level lifecycle: PENDING (created, no verified contact yet),
    -- ACTIVE (has a verified identity), SUSPENDED, DELETED.
    status             VARCHAR(32)  NOT NULL DEFAULT 'PENDING',

    -- Authorization role: 'user' | 'agent' | 'admin'. Admins are auto-promoted
    -- from the auth.admin-emails allowlist at verify/login; agents are granted
    -- by an admin. The role rides in the JWT and is resolved to a permission
    -- set -- see models.PermissionsFor.
    role               VARCHAR(20)  NOT NULL DEFAULT 'user',

    -- Canonical verified contact points. Nullable until the matching identity
    -- is verified. UNIQUE so two accounts cannot claim the same contact.
    primary_email      VARCHAR(255) UNIQUE,
    primary_phone      VARCHAR(20)  UNIQUE,

    -- Profile step (collected after contact verification).
    first_name         VARCHAR(255),
    last_name          VARCHAR(255),
    date_of_birth      DATE,
    profile_completed  BOOLEAN      NOT NULL DEFAULT false,

    created_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- auth_identities: each way an account can authenticate. One account -> many.
--   google   : provider_subject = Google 'sub'; verified=true on creation.
--   password : provider_subject = email; password_hash set; verified via email OTP.
--   phone    : provider_subject = E.164 phone; verified via SMS OTP.
-- ---------------------------------------------------------------------------
CREATE TABLE auth_identities (
    id                 BIGSERIAL PRIMARY KEY,
    account_id         BIGINT       NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,

    provider           VARCHAR(20)  NOT NULL,   -- 'google' | 'password' | 'phone'
    provider_subject   VARCHAR(255) NOT NULL,   -- google sub | email | phone

    email              VARCHAR(255),            -- google / password
    phone              VARCHAR(20),             -- phone
    password_hash      VARCHAR(255),            -- 'password' only (bcrypt)

    verified           BOOLEAN      NOT NULL DEFAULT false,
    verified_at        TIMESTAMPTZ,

    created_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),

    UNIQUE (provider, provider_subject)
);

CREATE INDEX idx_auth_identities_account ON auth_identities (account_id);
CREATE INDEX idx_auth_identities_email   ON auth_identities (email);
CREATE INDEX idx_auth_identities_phone   ON auth_identities (phone);

-- ---------------------------------------------------------------------------
-- otp_challenges: transient one-time-password verification for email or SMS.
-- account_id is nullable to allow pre-account signup.
-- ---------------------------------------------------------------------------
CREATE TABLE otp_challenges (
    id            BIGSERIAL PRIMARY KEY,
    account_id    BIGINT REFERENCES accounts (id) ON DELETE CASCADE,

    channel       VARCHAR(10)  NOT NULL,   -- 'email' | 'sms'
    destination   VARCHAR(255) NOT NULL,   -- the email or phone the OTP was sent to
    purpose       VARCHAR(32)  NOT NULL,   -- 'signup' | 'login' | 'add_identity' | 'reset'

    otp_hash      VARCHAR(255),            -- bcrypt hash; scrubbed to NULL once consumed
    expires_at    TIMESTAMPTZ  NOT NULL,
    attempts      INTEGER      NOT NULL DEFAULT 0,
    send_count    INTEGER      NOT NULL DEFAULT 0,
    last_sent_at  TIMESTAMPTZ,
    consumed_at   TIMESTAMPTZ,

    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX idx_otp_dest_purpose ON otp_challenges (destination, purpose);
CREATE INDEX idx_otp_account      ON otp_challenges (account_id);

-- ---------------------------------------------------------------------------
-- kyc_records: Aadhaar + PAN verification. A VERIFIED row gates the credit /
-- bank-statement / UPI analysis products.
--
-- COMPLIANCE: never store the raw 12-digit Aadhaar number. Only the last 4
-- digits plus a reference token from an offline eKYC / DigiLocker provider are
-- kept here. PAN and Aadhaar fields are sensitive PII.
-- ---------------------------------------------------------------------------
CREATE TABLE kyc_records (
    id                  BIGSERIAL PRIMARY KEY,
    account_id          BIGINT      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,

    pan_number          VARCHAR(10) NOT NULL,
    pan_name            VARCHAR(255),
    pan_verified        BOOLEAN     NOT NULL DEFAULT false,

    aadhaar_last4       CHAR(4),                 -- last 4 digits ONLY
    aadhaar_reference   VARCHAR(255),            -- token/ref from KYC provider
    aadhaar_pan_linked  BOOLEAN,

    status              VARCHAR(32) NOT NULL DEFAULT 'PENDING',  -- PENDING/VERIFIED/REJECTED
    provider            VARCHAR(32),             -- KYC provider used
    verified_at         TIMESTAMPTZ,

    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (account_id),
    UNIQUE (pan_number)
);

CREATE INDEX idx_kyc_status ON kyc_records (status);

-- ---------------------------------------------------------------------------
-- sessions: one row per signed-in device.
--
-- Auth is split into a short-lived stateless access JWT (minutes) and a
-- long-lived opaque refresh token (weeks) anchored to a row here. The access
-- token stays cheap to verify with no DB read; revocation acts on this table
-- and takes effect within one access-token lifetime.
-- ---------------------------------------------------------------------------
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
    -- the token was stolen -- the session is revoked on sight.
    prev_token_hash     CHAR(64)    UNIQUE,

    -- Device identity. device_id is supplied by the client (X-Device-Id) and
    -- is NOT a security boundary: a hostile client can send any value. It only
    -- groups repeat logins from one physical device so the signed-in devices
    -- list shows one entry per device rather than one per login.
    device_id           VARCHAR(128),
    device_name         VARCHAR(128),
    device_platform     VARCHAR(16),           -- 'ios' | 'android' | 'web'
    user_agent          VARCHAR(512),

    -- Client address. Only meaningful behind a proxy when server.trusted-proxies
    -- names that proxy -- otherwise this is the load balancer, not the user.
    ip                  VARCHAR(45),           -- textual, IPv6-max length

    -- Richer device description, refreshed on every token rotation:
    --   {"client": {...},   -- declared by the app: make, model, OS version
    --    "agent":  {...}}   -- parsed by the server from the User-Agent header
    -- The "client" half is untrusted, self-reported, bounded input: display
    -- only, never queried for authorization.
    device_meta         JSONB,

    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at          TIMESTAMPTZ NOT NULL,
    revoked_at          TIMESTAMPTZ,
    revoked_reason      VARCHAR(32)            -- see models.Revoke* constants
);

-- One live session per (account, device). Revoked rows are excluded so signing
-- back in on the same device inserts cleanly instead of colliding. Sessions
-- with no device_id are excluded too: each login gets its own row.
CREATE UNIQUE INDEX idx_sessions_account_device
    ON sessions (account_id, device_id)
    WHERE device_id IS NOT NULL AND revoked_at IS NULL;

CREATE INDEX idx_sessions_account ON sessions (account_id);
CREATE INDEX idx_sessions_expires ON sessions (expires_at) WHERE revoked_at IS NULL;

-- ---------------------------------------------------------------------------
-- credit_analytics_requests: one row per outbound Digitap call.
--
-- The bureau payload is large and varies between releases, so the request and
-- the full upstream response are kept as JSONB rather than normalized; key
-- metadata is lifted into columns for cheap filtering.
-- ---------------------------------------------------------------------------
CREATE TABLE credit_analytics_requests (
    id              BIGSERIAL PRIMARY KEY,
    account_id      BIGINT      REFERENCES accounts (id) ON DELETE CASCADE,

    client_ref_num  VARCHAR(45) NOT NULL,             -- caller's correlation id
    mobile_no       VARCHAR(20) NOT NULL,             -- subject mobile number

    request_id      VARCHAR(100),                     -- Digitap-assigned request id
    result_code     INTEGER,                          -- 101/102/103, NULL on error
    http_status     INTEGER,                          -- Digitap http_response_code
    message         TEXT,                             -- Digitap message

    request_body    JSONB       NOT NULL,             -- exact payload sent upstream
    response_body   JSONB,                            -- full Digitap response

    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_credit_analytics_account ON credit_analytics_requests (account_id);
CREATE INDEX idx_credit_analytics_created ON credit_analytics_requests (created_at);

-- ---------------------------------------------------------------------------
-- products: seeded catalog. Prices live here, not in code.
-- ---------------------------------------------------------------------------
CREATE TABLE products (
    code        VARCHAR(50)   PRIMARY KEY,
    name        VARCHAR(100)  NOT NULL,
    amount      NUMERIC(12,2) NOT NULL,
    currency    CHAR(3)       NOT NULL DEFAULT 'INR',
    active      BOOLEAN       NOT NULL DEFAULT TRUE,

    created_at  TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ   NOT NULL DEFAULT now()
);

-- ON CONFLICT keeps the seed replayable: re-running this file by hand against
-- a database that already has the catalog is a no-op rather than a duplicate
-- key error, and locally edited prices survive.
INSERT INTO products (code, name, amount) VALUES
    ('CREDIT_ANALYSIS',          'Credit Report Analysis',  299.00),
    ('BANK_STATEMENT_ANALYSIS',  'Bank Statement Analysis', 299.00),
    ('UPI_STATEMENT_ANALYSIS',   'UPI Statement Analysis',  299.00)
ON CONFLICT (code) DO NOTHING;

-- ---------------------------------------------------------------------------
-- orders: one row per purchase attempt.
--
-- order_uid is our identifier sent to Cashfree as its order_id. The row is
-- inserted BEFORE the Cashfree call (CREATION_REQUESTED) so the uid exists to
-- send; amount/currency are snapshotted from the product so later price
-- changes don't rewrite history.
--
-- Status lifecycle:
--   CREATION_REQUESTED -> ACTIVE            (Cashfree order created)
--                      -> CREATION_FAILED   (Cashfree call failed)
--   ACTIVE             -> PAID | FAILED | EXPIRED | TERMINATED
-- ---------------------------------------------------------------------------
CREATE TABLE orders (
    id                  BIGSERIAL     PRIMARY KEY,
    order_uid           VARCHAR(45)   NOT NULL UNIQUE,
    account_id          BIGINT        NOT NULL REFERENCES accounts (id),
    product_code        VARCHAR(50)   NOT NULL REFERENCES products (code),

    -- amount is what the customer is actually charged: the product price minus
    -- any coupon discount. discount_amount and coupon_code are snapshots of
    -- how it got there, so the list price is amount + discount_amount and
    -- later edits to the coupon never rewrite this order. There is
    -- deliberately no FK to coupons: an order's price record should survive a
    -- coupon being deleted. The authoritative link is coupon_redemptions.
    amount              NUMERIC(12,2) NOT NULL,
    discount_amount     NUMERIC(12,2) NOT NULL DEFAULT 0,
    coupon_code         VARCHAR(32),
    currency            CHAR(3)       NOT NULL DEFAULT 'INR',
    status              VARCHAR(30)   NOT NULL DEFAULT 'CREATION_REQUESTED',

    cf_order_id         VARCHAR(64)   UNIQUE,     -- Cashfree's id for the order
    payment_session_id  TEXT,                     -- frontend opens checkout with this
    cf_payment_id       VARCHAR(64),              -- from the success webhook
    payment_method      VARCHAR(50),              -- upi / credit_card / net_banking ...
    failure_reason      TEXT,
    order_expiry_time   TIMESTAMPTZ,

    paid_at             TIMESTAMPTZ,
    fulfilled_at        TIMESTAMPTZ,              -- when the entitlement was granted

    created_at          TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ   NOT NULL DEFAULT now()
);

CREATE INDEX idx_orders_account ON orders (account_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- payment_webhook_events: every received Cashfree webhook, verbatim.
--
-- The UNIQUE idempotency_key makes duplicate webhook delivery a no-op at
-- insert time; the guarded status transitions on orders are the second line
-- of defence (two success webhooks with different keys still fulfil once).
-- ---------------------------------------------------------------------------
CREATE TABLE payment_webhook_events (
    id               BIGSERIAL    PRIMARY KEY,
    idempotency_key  VARCHAR(128) UNIQUE,        -- x-idempotency-key header (nullable)
    event_type       VARCHAR(64)  NOT NULL,
    order_uid        VARCHAR(45),
    payload          JSONB        NOT NULL,
    processed        BOOLEAN      NOT NULL DEFAULT FALSE,

    received_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX idx_webhook_events_order ON payment_webhook_events (order_uid);

-- ---------------------------------------------------------------------------
-- coupons: percentage discounts issued by accounts holding the 'agent' role
-- (or admins).
--
-- The discount is stored as a percentage and applied server-side at order
-- creation. Nothing about the price is ever accepted from the client.
-- ---------------------------------------------------------------------------
CREATE TABLE coupons (
    id                 BIGSERIAL     PRIMARY KEY,

    -- Normalized to upper case on write so lookups are exact and
    -- 'SAVE20' / 'save20' cannot become two different coupons.
    code               VARCHAR(32)   NOT NULL UNIQUE,

    -- The agent (or admin) who issued it. Kept on delete so redemption
    -- history stays attributable; deactivate via revoked_at instead.
    created_by         BIGINT        NOT NULL REFERENCES accounts (id),

    discount_percent   NUMERIC(5,2)  NOT NULL,

    -- NULL scopes the coupon to every product; a code restricts it to one.
    product_code       VARCHAR(50)   REFERENCES products (code),

    -- NULL max_redemptions means unlimited. redemption_count is maintained by
    -- a conditional UPDATE rather than a read-then-write, so concurrent
    -- checkouts cannot push it past the cap -- see CouponRepo.Claim.
    max_redemptions    INTEGER,
    redemption_count   INTEGER       NOT NULL DEFAULT 0,
    per_account_limit  INTEGER       NOT NULL DEFAULT 1,

    valid_from         TIMESTAMPTZ   NOT NULL DEFAULT now(),
    valid_until        TIMESTAMPTZ,
    revoked_at         TIMESTAMPTZ,

    created_at         TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ   NOT NULL DEFAULT now(),

    -- 100% is allowed, but an order that nets to zero is rejected at checkout
    -- because the gateway will not accept it.
    CONSTRAINT coupons_percent_range   CHECK (discount_percent > 0 AND discount_percent <= 100),
    CONSTRAINT coupons_max_positive    CHECK (max_redemptions IS NULL OR max_redemptions > 0),
    CONSTRAINT coupons_count_in_range  CHECK (redemption_count >= 0
                                              AND (max_redemptions IS NULL OR redemption_count <= max_redemptions)),
    CONSTRAINT coupons_per_account_pos CHECK (per_account_limit > 0),
    CONSTRAINT coupons_window_ordered  CHECK (valid_until IS NULL OR valid_until > valid_from)
);

CREATE INDEX idx_coupons_created_by ON coupons (created_by, created_at DESC);

-- ---------------------------------------------------------------------------
-- coupon_redemptions: one row per order that applied a coupon.
--
-- A row is written when the order is created, and released (released_at set,
-- coupon counter decremented) if that order never gets paid. Counting at
-- creation is what makes max_redemptions meaningful -- counting at payment
-- would let any number of unpaid orders hold the same last redemption.
--
-- This is also the referral record: it attributes a paid order to the agent
-- whose code was used.
-- ---------------------------------------------------------------------------
CREATE TABLE coupon_redemptions (
    id              BIGSERIAL     PRIMARY KEY,
    coupon_id       BIGINT        NOT NULL REFERENCES coupons (id),
    account_id      BIGINT        NOT NULL REFERENCES accounts (id),

    -- UNIQUE enforces one coupon per order at the schema level, not just in
    -- service code.
    order_uid       VARCHAR(45)   NOT NULL UNIQUE REFERENCES orders (order_uid),

    -- Snapshot of what the discount was worth, so later edits to the coupon
    -- never rewrite the history of an order.
    discount_amount NUMERIC(12,2) NOT NULL,

    redeemed_at     TIMESTAMPTZ   NOT NULL DEFAULT now(),
    released_at     TIMESTAMPTZ,

    CONSTRAINT coupon_redemptions_amount_positive CHECK (discount_amount >= 0)
);

CREATE INDEX idx_coupon_redemptions_coupon  ON coupon_redemptions (coupon_id);
CREATE INDEX idx_coupon_redemptions_account ON coupon_redemptions (coupon_id, account_id)
    WHERE released_at IS NULL;
