DROP INDEX IF EXISTS idx_kyc_records_reviewed_by;

ALTER TABLE kyc_records
    DROP COLUMN IF EXISTS reviewed_by_account_id,
    DROP COLUMN IF EXISTS reviewed_at;
