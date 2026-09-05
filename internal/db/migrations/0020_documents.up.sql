-- One row per stored user document — the PAN card today; Aadhaar, bank proofs
-- and whatever else onboarding grows to collect later — rather than file
-- columns bolted onto every table that wants one. The bytes live in the
-- private S3 bucket; the row is the pointer plus enough metadata to describe
-- the file without fetching it. Reads are presigned GETs minted behind the
-- permission appropriate to the document, never the URI itself.
--
-- Deliberately NO uniqueness on (account_id, doc_type): an account may hold
-- many documents of the same kind — several bank statements, a re-uploaded
-- card, both sides of one. Each upload is a new row; which one is "current"
-- for a purpose is that feature's own reference (kyc_records.document_id for
-- the PAN card), not this table's shape. Superseded rows therefore remain as
-- history — these are identity documents, so the pre-launch retention policy
-- must bound how long (DPDP storage limitation; same note as prefill_lookups).
CREATE TABLE documents (
    id          BIGSERIAL PRIMARY KEY,
    account_id  BIGINT       NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    -- What the document is: PAN / AADHAAR / BANK / … Free text by design, so a
    -- new kind is a Go constant, not a migration.
    doc_type    VARCHAR(32)  NOT NULL,
    s3_uri      TEXT         NOT NULL,
    filename    VARCHAR(255) NOT NULL,
    mime_type   VARCHAR(100) NOT NULL,
    size_bytes  BIGINT,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX idx_documents_account ON documents (account_id, doc_type, created_at DESC);

-- The KYC record points at the card that supports its claim. SET NULL rather
-- than CASCADE: losing the file must not delete the KYC decision that cited it.
ALTER TABLE kyc_records
    ADD COLUMN document_id BIGINT REFERENCES documents (id) ON DELETE SET NULL;
