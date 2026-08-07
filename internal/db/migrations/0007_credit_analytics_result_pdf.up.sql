-- ---------------------------------------------------------------------------
-- credit_analytics_requests: result_pdf_url — the permanent Utho object URL.
--
-- Digitap's report_type 3 returns result_pdf: a short-lived (1 hour) URL for the
-- generated PDF. We download it and re-upload to Utho object storage, storing
-- the permanent Utho URL here. Mirrors migration 0002 (which lifted the bureau
-- score out of the JSONB response into its own column for cheap per-row access).
--
-- Nullable: the upload is best-effort and asynchronous, so the column is null
-- between row creation and upload completion, and stays null if the upload
-- fails (the raw Digitap response_body, including the source URL, is already
-- stored). Pointer-typed in the model so null is distinguishable from empty.
-- ---------------------------------------------------------------------------
ALTER TABLE credit_analytics_requests
    ADD COLUMN result_pdf_url TEXT;
