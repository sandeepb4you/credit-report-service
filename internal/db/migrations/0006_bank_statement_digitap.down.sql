DROP INDEX IF EXISTS idx_bank_statements_request_id;

ALTER TABLE bank_statements
    DROP COLUMN IF EXISTS url_expires_at,
    DROP COLUMN IF EXISTS redirect_url,
    DROP COLUMN IF EXISTS txn_id,
    DROP COLUMN IF EXISTS request_id,
    DROP COLUMN IF EXISTS provider;
