-- Registration: users and multi-step registration attempts.
-- Schema lives in the `credit_report` schema (set via currentSchema in JDBC URL).

-- Confirmed, KYC-accepted users.
CREATE TABLE IF NOT EXISTS users (
    id              BIGSERIAL PRIMARY KEY,
    mobile          VARCHAR(20)  NOT NULL UNIQUE,
    email           VARCHAR(255) NOT NULL UNIQUE,
    pan_number      VARCHAR(10)  NOT NULL UNIQUE,
    full_name       VARCHAR(255) NOT NULL,
    date_of_birth   DATE,
    pan_image_path  VARCHAR(1024),
    status          VARCHAR(32)  NOT NULL DEFAULT 'ACTIVE',
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_users_mobile     ON users (mobile);
CREATE INDEX IF NOT EXISTS idx_users_email      ON users (email);
CREATE INDEX IF NOT EXISTS idx_users_pan_number ON users (pan_number);

-- In-flight registration attempts. Each row walks the state machine:
--   STARTED -> OTP_VERIFIED -> PAN_VERIFIED  (terminal success)
-- Any failed terminal state is recorded for audit.
CREATE TABLE IF NOT EXISTS registration_attempts (
    id                  BIGSERIAL PRIMARY KEY,
    mobile              VARCHAR(20)  NOT NULL,
    email               VARCHAR(255) NOT NULL,

    status              VARCHAR(32)  NOT NULL DEFAULT 'STARTED',

    -- OTP tracking
    otp_hash            VARCHAR(255),
    otp_expires_at      TIMESTAMPTZ,
    otp_attempts        INTEGER      NOT NULL DEFAULT 0,
    otp_send_count      INTEGER      NOT NULL DEFAULT 0,
    last_otp_sent_at    TIMESTAMPTZ,

    -- PAN submission (stage 2)
    pan_number          VARCHAR(10),
    pan_name            VARCHAR(255),
    date_of_birth       DATE,
    pan_image_path      VARCHAR(1024),

    -- OCR results for audit / debugging
    ocr_pan_number      VARCHAR(32),
    ocr_pan_name        VARCHAR(255),

    user_id             BIGINT REFERENCES users (id),
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_reg_attempts_mobile ON registration_attempts (mobile);
CREATE INDEX IF NOT EXISTS idx_reg_attempts_email  ON registration_attempts (email);
CREATE INDEX IF NOT EXISTS idx_reg_attempts_status ON registration_attempts (status);
