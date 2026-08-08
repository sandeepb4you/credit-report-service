-- ---------------------------------------------------------------------------
-- Record why a KYC submission was rejected.
--
-- The admin desk can already approve a PAN (status VERIFIED); rejecting it
-- needs somewhere to put the reason, otherwise the applicant is told "no" with
-- no way to work out what to fix. Nullable: only REJECTED rows carry one, and
-- re-submitting a PAN clears it along with the rest of the verification state.
-- ---------------------------------------------------------------------------
ALTER TABLE kyc_records
    ADD COLUMN IF NOT EXISTS rejection_reason TEXT;
