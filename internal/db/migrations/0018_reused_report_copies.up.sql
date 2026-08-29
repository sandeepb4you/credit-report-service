-- A refresh inside the reuse window now writes its own row, populated from the
-- stored report instead of from Digitap.
--
-- Returning the earlier row was cheaper but left the user's history not matching
-- what they did: they bought a check, the check was honoured, and "past score
-- checks" did not grow. A purchase that leaves no trace where the user looks for
-- it reads as a lost payment.
--
-- data_fetched_at is when the data in this row actually came off the bureau, as
-- distinct from created_at, which is when this row was written. On a live pull
-- they are the same instant. On a copy, data_fetched_at is inherited from the
-- source, and that inheritance is the whole safety property here: the reuse
-- window is measured against it, so a copy can never restart the clock. Without
-- it, refreshing every six days would keep minting rows that each looked fresh
-- while the underlying pull aged indefinitely, and Digitap would never be called
-- again.
--
-- reused_from_report_id names the pull the data came from, and points at the
-- ORIGINAL rather than the row copied from, so lineage stays one level deep and
-- "how many times was this pull served" is a single query. NULL means this row is
-- a real bureau call.
ALTER TABLE credit_analytics_requests
    ADD COLUMN IF NOT EXISTS reused_from_report_id BIGINT
        REFERENCES credit_analytics_requests (id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS data_fetched_at TIMESTAMPTZ;

-- Every existing row is a live pull, so its data was fetched when it was written.
UPDATE credit_analytics_requests SET data_fetched_at = created_at WHERE data_fetched_at IS NULL;

ALTER TABLE credit_analytics_requests ALTER COLUMN data_fetched_at SET DEFAULT now();
ALTER TABLE credit_analytics_requests ALTER COLUMN data_fetched_at SET NOT NULL;

COMMENT ON COLUMN credit_analytics_requests.data_fetched_at IS
    'When the data in this row came off the bureau. Equals created_at for a live pull; inherited from the source for a reused copy, so the reuse window cannot be restarted by copying.';
COMMENT ON COLUMN credit_analytics_requests.reused_from_report_id IS
    'The live pull this row copied its data from. NULL means this row IS a live pull.';

CREATE INDEX IF NOT EXISTS idx_credit_analytics_reused_from
    ON credit_analytics_requests (reused_from_report_id)
    WHERE reused_from_report_id IS NOT NULL;
