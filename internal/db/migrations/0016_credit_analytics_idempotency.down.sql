DROP INDEX IF EXISTS ux_credit_analytics_idempotency;

ALTER TABLE credit_analytics_requests
    DROP COLUMN IF EXISTS idempotency_key;
