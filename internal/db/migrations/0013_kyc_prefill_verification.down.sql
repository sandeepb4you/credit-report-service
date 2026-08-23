ALTER TABLE kyc_records
    DROP COLUMN IF EXISTS provider_ref,
    DROP COLUMN IF EXISTS verification_attempts;
