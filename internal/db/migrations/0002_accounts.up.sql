-- Accounts, authentication identities, OTP challenges, and KYC.
--
-- Design: identity (how you log in) is separated from the account (who you are)
-- from KYC (regulated Aadhaar/PAN verification). Multiple signup methods
-- (Google OAuth, email+password, phone OTP) collapse into a single account.
--
--   accounts        the person + onboarding lifecycle + profile
--     ├── auth_identities  1..N  how the account authenticates
--     ├── otp_challenges   0..N  transient email/SMS OTP verification
--     └── kyc_records      0..1  Aadhaar + PAN, gates analysis products

-- ---------------------------------------------------------------------------
-- accounts: one row per user.
-- ---------------------------------------------------------------------------
CREATE TABLE accounts (
    id                 BIGSERIAL PRIMARY KEY,

    -- Account-level lifecycle: PENDING (created, no verified contact yet),
    -- ACTIVE (has a verified identity), SUSPENDED, DELETED.
    status             VARCHAR(32)  NOT NULL DEFAULT 'PENDING',

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
-- Generalizes the old registration_attempts OTP tracking across both channels
-- and multiple purposes. account_id is nullable to allow pre-account signup.
-- ---------------------------------------------------------------------------
CREATE TABLE otp_challenges (
    id            BIGSERIAL PRIMARY KEY,
    account_id    BIGINT REFERENCES accounts (id) ON DELETE CASCADE,

    channel       VARCHAR(10)  NOT NULL,   -- 'email' | 'sms'
    destination   VARCHAR(255) NOT NULL,   -- the email or phone the OTP was sent to
    purpose       VARCHAR(32)  NOT NULL,   -- 'signup' | 'login' | 'add_identity' | 'reset'

    otp_hash      VARCHAR(255) NOT NULL,   -- bcrypt hash of the code
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
