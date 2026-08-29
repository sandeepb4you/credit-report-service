DROP INDEX IF EXISTS idx_credit_analytics_reused_from;

ALTER TABLE credit_analytics_requests
    DROP COLUMN IF EXISTS data_fetched_at,
    DROP COLUMN IF EXISTS reused_from_report_id;
