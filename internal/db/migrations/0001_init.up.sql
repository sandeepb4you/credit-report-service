-- Initial schema for credit_report_service.
-- Schema is selected via the `currentSchema=credit_report` JDBC/PG SEARCH_PATH.
-- golang-migrate runs each statement in order; no IF NOT EXISTS guard needed
-- because migrate tracks applied versions in its own table.

CREATE TABLE credit_reports (
    id          BIGSERIAL PRIMARY KEY,
    subject_id  VARCHAR(255) NOT NULL UNIQUE,
    score       INTEGER,
    status      VARCHAR(64),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_credit_reports_subject_id ON credit_reports (subject_id);
