-- Recreates the credit_reports table as it existed in 0001_init.up.sql.
-- Data dropped by the up migration is NOT restored.
CREATE TABLE credit_reports (
    id          BIGSERIAL PRIMARY KEY,
    subject_id  VARCHAR(255) NOT NULL UNIQUE,
    score       INTEGER,
    status      VARCHAR(64),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_credit_reports_subject_id ON credit_reports (subject_id);
