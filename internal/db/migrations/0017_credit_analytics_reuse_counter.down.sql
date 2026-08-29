DROP INDEX IF EXISTS idx_credit_analytics_reused;

ALTER TABLE credit_analytics_requests
    DROP COLUMN IF EXISTS last_reused_at,
    DROP COLUMN IF EXISTS reuse_count;
