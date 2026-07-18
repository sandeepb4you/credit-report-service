-- Initial schema for credit_report_service
CREATE TABLE IF NOT EXISTS credit_reports (
    id          BIGSERIAL PRIMARY KEY,
    subject_id  VARCHAR(255) NOT NULL UNIQUE,
    score       INTEGER,
    status      VARCHAR(64),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_credit_reports_subject_id ON credit_reports (subject_id);
