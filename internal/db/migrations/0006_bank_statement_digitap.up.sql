-- ---------------------------------------------------------------------------
-- bank_statements: Digitap Bank-Data (redirect/upload) flow columns.
--
-- Migration 0005 introduced the local-upload flow (client POSTs a PDF, we parse
-- it). This migration adds the columns the Digitap Bank Data PDF UI flow needs:
-- the user uploads their statement to Digitap's UI, Digitap calls us back, and
-- we fetch the generated report. provider records which flow a row belongs to
-- ('local' for the in-process analyzer, 'digitap' for the Digitap flow).
--
-- request_id / txn_id are Digitap's correlation ids: Generate URL returns
-- request_id; the transaction-complete callback and status-check return txn_id.
-- The request_id partial index makes webhook→row lookup (the callback carries
-- request_id) an index seek rather than a scan.
-- ---------------------------------------------------------------------------
ALTER TABLE bank_statements
    ADD COLUMN provider       VARCHAR(10)  NOT NULL DEFAULT 'local'
        CHECK (provider IN ('local', 'digitap')),
    ADD COLUMN request_id     VARCHAR(100),                 -- Digitap Generate-URL correlation id
    ADD COLUMN txn_id         VARCHAR(100),                 -- Digitap transaction id
    ADD COLUMN redirect_url   TEXT,                         -- Digitap UI URL handed to the client
    ADD COLUMN url_expires_at TIMESTAMPTZ;                  -- Generate-URL `expires`

CREATE INDEX idx_bank_statements_request_id
    ON bank_statements (request_id)
    WHERE request_id IS NOT NULL;
