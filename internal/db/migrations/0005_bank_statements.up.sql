-- ---------------------------------------------------------------------------
-- bank_statements: one row per uploaded bank-statement PDF.
--
-- Analysis runs asynchronously (the first in-process worker pool in the
-- service): the upload handler inserts a 'processing' row and returns its id,
-- then a worker extracts the PDF text and writes the derived analysis back.
-- Clients poll the row by id until status = 'completed' (or 'failed').
--
-- The raw PDF bytes are stored in the row (BYTEA) so re-analysis never needs
-- the original upload again; the extracted text and the derived analysis are
-- kept alongside it. Heavy columns are excluded from list/detail queries by the
-- repository's lightweight column list.
--
-- Mirrors credit_analytics_requests: lift key metadata into columns for cheap
-- filtering, keep the large schema-versioned payload (analysis JSONB) aside.
-- ---------------------------------------------------------------------------
CREATE TABLE bank_statements (
    id                BIGSERIAL    PRIMARY KEY,
    account_id        BIGINT       NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,

    filename          VARCHAR(255) NOT NULL,                 -- original upload name
    mime_type         VARCHAR(100),                          -- e.g. application/pdf

    -- status lifecycle:
    --   processing -> completed   (text extracted + analysis written)
    --             -> failed       (unparseable PDF / worker error; see error_message)
    status            VARCHAR(20)  NOT NULL DEFAULT 'processing',
    CHECK (status IN ('processing', 'completed', 'failed')),

    pdf_bytes         BYTEA        NOT NULL,                 -- raw upload, self-contained
    extracted_text    TEXT,                                  -- PDF text layer, populated by worker
    analysis          JSONB,                                 -- derived metrics (salary, EMI, categories...)
    error_message     TEXT,                                  -- set only when status = 'failed'

    transaction_count INTEGER,                               -- count parsed by the worker
    period_start      DATE,                                  -- earliest transaction date
    period_end        DATE,                                  -- latest transaction date

    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    completed_at      TIMESTAMPTZ                            -- set when status leaves 'processing'
);

CREATE INDEX idx_bank_statements_account ON bank_statements (account_id, created_at DESC);
-- Partial index for startup recovery: rows still 'processing' after a crash are
-- reclaimed by the worker pool on boot (ReclaimStaleProcessing).
CREATE INDEX idx_bank_statements_processing ON bank_statements (status, created_at)
    WHERE status = 'processing';
