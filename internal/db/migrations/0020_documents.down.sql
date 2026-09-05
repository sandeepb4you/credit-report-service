ALTER TABLE kyc_records
    DROP COLUMN IF EXISTS document_id;
DROP TABLE IF EXISTS documents;
