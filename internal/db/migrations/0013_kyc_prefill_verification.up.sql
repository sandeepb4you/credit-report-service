-- PAN verification moved from an admin's manual review to an automated check
-- against Digitap's Mobile to Prefill API, which answers whether the submitted
-- PAN and name are the ones registered against the account's mobile number.
--
-- provider_ref stores the provider's request_id for the deciding lookup: the
-- handle their support needs to trace one decision months later. It is an
-- opaque id, not personal data.
ALTER TABLE kyc_records
    ADD COLUMN IF NOT EXISTS provider_ref           VARCHAR(64),
    ADD COLUMN IF NOT EXISTS verification_attempts  INT NOT NULL DEFAULT 0;

-- The attempt counter is a brute-force guard: PAN plus a name is guessable for
-- a known person, and without a cap an attacker holding a stolen handset could
-- grind candidate PANs against the provider at our expense.
COMMENT ON COLUMN kyc_records.verification_attempts IS
    'Failed provider verification attempts since the last PAN change; capped by registration.pan.max-verification-attempts';
