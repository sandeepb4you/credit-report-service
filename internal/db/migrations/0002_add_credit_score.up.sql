-- ---------------------------------------------------------------------------
-- Lift the bureau score out of the JSONB response into its own column.
--
-- The full report is already stored verbatim in response_body, but the score
-- is the one field the reports list needs cheaply and per-row, so it is
-- extracted at write time (SCORE.BureauScore) and persisted here. Nullable:
-- failed pulls and no-record responses (result_code 102/103) carry no score.
-- ---------------------------------------------------------------------------
ALTER TABLE credit_analytics_requests
    ADD COLUMN credit_score INTEGER;
