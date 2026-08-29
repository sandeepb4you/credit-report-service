-- Make report reuse answerable after the fact.
--
-- The reuse path (CreditAnalyticsService.reusableReport) deliberately does no
-- work: it reads the account's most recent successful report and returns it,
-- writing nothing. That is the point — it exists to avoid a billed bureau call
-- for an answer we already hold — but it left reuse invisible the moment it
-- happened. The only evidence was an INFO log line and a response header, on a
-- host whose logs are a rolling json-file window shipped nowhere, so "was this
-- user's report reused?" could only be answered by someone already watching.
--
-- These two columns cost one primary-key UPDATE per reuse, against the HTTP
-- call to Digitap they replace. That buys the questions worth asking: how often
-- reuse fires, how much vendor spend it saves, whether the reuse window is set
-- to the right length, and — for support — why a user's score did not change
-- when they asked for a new check.
ALTER TABLE credit_analytics_requests
    ADD COLUMN IF NOT EXISTS reuse_count    INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_reused_at TIMESTAMPTZ;

COMMENT ON COLUMN credit_analytics_requests.reuse_count IS
    'Times this stored report was served in place of a fresh bureau pull. 0 = never reused.';
COMMENT ON COLUMN credit_analytics_requests.last_reused_at IS
    'When it was last served that way; NULL while reuse_count is 0.';

-- Existing rows correctly start at 0: reuse was introduced with this feature,
-- so no earlier row was ever served that way. No backfill is possible or needed.

-- Partial: the overwhelming majority of rows are never reused, and the queries
-- worth running ("what got reused, and when") only ever look at the ones that were.
CREATE INDEX IF NOT EXISTS idx_credit_analytics_reused
    ON credit_analytics_requests (last_reused_at DESC)
    WHERE reuse_count > 0;
