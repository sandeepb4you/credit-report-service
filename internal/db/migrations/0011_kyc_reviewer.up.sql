-- ---------------------------------------------------------------------------
-- Record who made the KYC decision, not just what it was.
--
-- Verifying a PAN is a manual, privileged judgement, so the row must say which
-- admin made it and when. Without this an incorrectly verified account is
-- untraceable — there is no way to tell which reviewer to retrain, or whether a
-- verification predates someone losing their admin role.
--
-- reviewed_at is separate from updated_at because updated_at also moves when
-- the applicant re-submits a PAN; this is specifically the time of the last
-- reviewer decision. Both are cleared on re-submission: the new PAN has not
-- been reviewed by anyone.
--
-- ON DELETE SET NULL rather than CASCADE: losing a reviewer's account must not
-- delete the KYC records they approved.
-- ---------------------------------------------------------------------------
ALTER TABLE kyc_records
    ADD COLUMN IF NOT EXISTS reviewed_by_account_id BIGINT
        REFERENCES accounts (id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS reviewed_at TIMESTAMPTZ;

-- Answering "what has this reviewer decided?" scans by reviewer.
CREATE INDEX IF NOT EXISTS idx_kyc_records_reviewed_by
    ON kyc_records (reviewed_by_account_id)
    WHERE reviewed_by_account_id IS NOT NULL;
