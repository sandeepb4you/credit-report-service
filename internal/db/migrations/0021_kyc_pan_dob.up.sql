-- Date of birth on the KYC record itself, beside the PAN it belongs to.
--
-- Two sources, matching how the record gets verified:
--   * automated: the Mobile to Prefill response carries the DOB the bureau
--     holds for the number (already used to backfill accounts.date_of_birth);
--   * manual: the user types it on the PAN card upload screen, so the reviewer
--     can compare it against the card in front of them.
--
-- Kept on kyc_records rather than only on accounts because it is part of the
-- KYC evidence: accounts.date_of_birth is profile data the user can edit,
-- while this column records what verification was based on.
ALTER TABLE kyc_records
    ADD COLUMN pan_date_of_birth DATE;
